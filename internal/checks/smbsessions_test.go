package checks

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
	"sermo/internal/process"
)

const smbSessionsFixture = `{"sessions":{
 "11":{"session_id":"11","server_id":{"pid":"101","task_id":"0","vnn":"4294967295","unique_id":"18446744073709551615"},"username":"alice","remote_machine":"192.0.2.4","hostname":"ipv4:192.0.2.4:50000"},
 "12":{"session_id":"12","server_id":{"pid":"101","task_id":"0","vnn":"4294967295","unique_id":"18446744073709551615"},"username":"bob","remote_machine":"192.0.2.4","hostname":"ipv4:192.0.2.4:50000"}
}}`

func smbFixtureIdentity(pid int) (process.Identity, bool) {
	return process.Identity{PID: pid, StartTicks: 1234, StartTicksOK: true, State: "S"}, true
}

func TestSMBSessionsGroupsWorkersAndBindsAllLogins(t *testing.T) {
	sessions, err := parseSMBSessions(t.Context(), smbSessionsFixture, smbFixtureIdentity)
	if err != nil || len(sessions) != 1 {
		t.Fatalf("sessions=%+v err=%v", sessions, err)
	}
	want := sessions[0]
	if want.PID != 101 || want.User != "alice, bob" || want.Client != "192.0.2.4" || want.SessionCount != 2 || want.StartTicks != 1234 || want.Identity == "" {
		t.Fatalf("group=%+v", want)
	}
	for _, change := range []struct{ from, to string }{
		{"bob", "carol"}, {"192.0.2.4", "192.0.2.5"}, {"18446744073709551615", "18446744073709551614"},
		{`"12"`, `"13"`}, {"50000", "50001"},
	} {
		changed, err := parseSMBSessions(t.Context(), strings.ReplaceAll(smbSessionsFixture, change.from, change.to), smbFixtureIdentity)
		if err != nil || changed[0].Identity == want.Identity {
			t.Fatalf("change=%+v sessions=%+v err=%v", change, changed, err)
		}
	}
	changed, err := parseSMBSessions(t.Context(), smbSessionsFixture, func(pid int) (process.Identity, bool) {
		id, ok := smbFixtureIdentity(pid)
		id.StartTicks++
		return id, ok
	})
	if err != nil || changed[0].Identity == want.Identity {
		t.Fatalf("reused PID=%+v err=%v", changed, err)
	}
}

func TestSMBSessionsMissingGenerationAndClusterNeverBindLocalPIDs(t *testing.T) {
	for _, name := range []string{"cluster", "missing", "zombie", "unknown ticks"} {
		t.Run(name, func(t *testing.T) {
			fixture := smbSessionsFixture
			if name == "cluster" {
				fixture = strings.ReplaceAll(fixture, smbLocalVNN, "2")
			}
			calls := 0
			sessions, err := parseSMBSessions(t.Context(), fixture, func(pid int) (process.Identity, bool) {
				calls++
				id, ok := smbFixtureIdentity(pid)
				switch name {
				case "missing":
					ok = false
				case "zombie":
					id.State = process.ProcStateZombie
				case "unknown ticks":
					id.StartTicksOK = false
				}
				return id, ok
			})
			if err != nil || len(sessions) != 1 || sessions[0].Identity != "" || sessions[0].StartTicks != 0 || name == "cluster" && calls != 0 {
				t.Fatalf("sessions=%+v err=%v calls=%d", sessions, err, calls)
			}
		})
	}
}

func TestSMBSessionsMalformedOutputIsNotAnEmptyInventory(t *testing.T) {
	for _, output := range []string{"", "{}", `{"sessions":null}`, `{"sessions":[]}`, smbSessionsFixture + "truncated",
		strings.ReplaceAll(smbSessionsFixture, `"pid":"101"`, `"pid":"1"`),
		strings.ReplaceAll(smbSessionsFixture, `"session_id":"12"`, `"session_id":"13"`),
		strings.ReplaceAll(smbSessionsFixture, `"username":"bob"`, `"username":""`),
		strings.Replace(smbSessionsFixture, "18446744073709551615", "123", 1),
		strings.ReplaceAll(smbSessionsFixture, "18446744073709551615", "invalid"),
	} {
		if _, err := parseSMBSessions(t.Context(), output, smbFixtureIdentity); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
	if sessions, err := parseSMBSessions(t.Context(), `{"sessions":{}}`, smbFixtureIdentity); err != nil || len(sessions) != 0 {
		t.Fatalf("empty sessions=%+v err=%v", sessions, err)
	}
}

func TestSMBSessionsCheckUsesBoundedReadOnlyCommand(t *testing.T) {
	runner := execxtest.Fixed(execx.Result{Stdout: smbSessionsFixture}, nil)
	built, warnings := Build(map[string]any{"sessions": map[string]any{
		CheckKeyType: CheckTypeSMBSessions, CheckKeyBinary: "/usr/bin/smbstatus", CheckKeyPath: "/etc/samba/instance.conf",
		CheckKeyCount: map[string]any{CheckKeyOp: ">=", CheckKeyValue: 2},
	}}, Deps{DefaultTimeout: time.Second, Runner: runner, SMBSessionIdentity: smbFixtureIdentity})
	if len(warnings) != 0 || len(built) != 1 {
		t.Fatalf("build=%+v warnings=%v", built, warnings)
	}
	result := built[0].Check.Run(t.Context())
	if !result.OK || !result.Condition || result.Unavailable || result.Data[DataKeyCount] != 2 {
		t.Fatalf("result=%+v", result)
	}
	calls := runner.Calls()
	if len(calls) != 1 || !runner.SawDeadline() || !reflect.DeepEqual(calls[0].Args, []string{"--json", "--processes", "--configfile=/etc/samba/instance.conf"}) {
		t.Fatalf("calls=%+v deadline=%v", calls, runner.SawDeadline())
	}
}

func TestSMBSessionsCommandFailureAndCancellation(t *testing.T) {
	for _, name := range []string{"nonzero", "not started", "cancelled", "oversized"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			result := execx.Result{Stdout: `{"sessions":{}}`}
			var runErr error
			switch name {
			case "nonzero":
				result.ExitCode = 1
			case "not started":
				result.ExitCode = execx.ExitCodeRunFailure
				runErr = errors.New("missing smbstatus")
			case "cancelled":
				cancel()
			case "oversized":
				result.Stdout += strings.Repeat(" ", execx.MaxCommandOutput)
			}
			_, err := SampleSMBSessions(ctx, execxtest.Fixed(result, runErr), SMBSessionConfig{Binary: "/usr/bin/smbstatus"}, smbFixtureIdentity)
			if err == nil {
				t.Fatal("command failure became an empty success")
			}
		})
	}
}

func TestSMBClientIP(t *testing.T) {
	for _, tt := range []struct {
		login smbLogin
		want  string
	}{
		{smbLogin{Remote: "2001:db8::1"}, "2001:db8::1"},
		{smbLogin{Remote: "client", Hostname: "ipv6:[2001:db8::1]:1234"}, "2001:db8::1"},
		{smbLogin{Remote: "client", Hostname: "ipv4:192.0.2.1:1234"}, "192.0.2.1"},
	} {
		if got := smbClientIP(tt.login); got != tt.want {
			t.Fatalf("IP=%q want %q", got, tt.want)
		}
	}
}

func TestSMBSessionsTimeoutIsUnavailable(t *testing.T) {
	c := smbSessionsCheck{name: "sessions", timeout: time.Millisecond, config: SMBSessionConfig{Binary: "/usr/bin/smbstatus"}, runner: slowRunner{}}
	result := c.Run(t.Context())
	if !result.Unavailable || result.OK || result.Data[DataKeyCount] != nil || !strings.Contains(result.Message, "1ms") {
		t.Fatalf("result=%+v", result)
	}
}
