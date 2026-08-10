package checks

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

type glusterClusterRunner struct {
	results     map[string]execx.Result
	err         error
	calls       []string
	hasDeadline bool
}

func (r *glusterClusterRunner) Run(ctx context.Context, name string, args ...string) (execx.Result, error) {
	_, r.hasDeadline = ctx.Deadline()
	call := name + " " + strings.Join(args, " ")
	r.calls = append(r.calls, call)
	if result, ok := r.results[call]; ok {
		return result, nil
	}
	return execx.Result{ExitCode: execx.ExitCodeRunFailure}, r.err
}

func glusterXML(body string) execx.Result {
	return execx.Result{ExitCode: glusterCLIExitSuccess, Stdout: "<cliOutput><opRet>0</opRet>" + body + "</cliOutput>"}
}

func glusterClusterResults() map[string]execx.Result {
	return map[string]execx.Result{
		"gluster --mode=script --xml peer status": glusterXML(`
<peerStatus><peer><hostname>zeus</hostname><connected>1</connected><state>3</state><stateStr>Peer in Cluster</stateStr></peer></peerStatus>`),
		"gluster --mode=script --xml volume info": glusterXML(`
<volInfo><volumes><volume><name>images</name><status>1</status><statusStr>Started</statusStr><brickCount>2</brickCount></volume></volumes></volInfo>`),
		"gluster --mode=script --xml volume status": glusterXML(`
<volStatus><volumes><volume><volName>images</volName>
  <node><hostname>sirio</hostname><path>/bricks/images0</path><status>1</status></node>
  <node><hostname>zeus</hostname><path>/bricks/images0</path><status>1</status></node>
  <node><hostname>Self-heal Daemon</hostname><path>sirio</path><status>1</status></node>
</volume></volumes></volStatus>`),
		"gluster --mode=script --xml volume heal images info": glusterXML(`
<healInfo><bricks><brick><name>sirio:/bricks/images0</name><status>Connected</status><numberOfEntries>0</numberOfEntries></brick></bricks></healInfo>`),
		"gluster --mode=script --xml volume heal images info split-brain": glusterXML(`
<healInfo><bricks><brick><name>sirio:/bricks/images0</name><status>Connected</status><numberOfEntries>0</numberOfEntries></brick></bricks></healInfo>`),
	}
}

func glusterClusterExpectation() map[string]any {
	return map[string]any{
		CheckKeyPeers: []any{"zeus"},
		CheckKeyVolumes: map[string]any{
			"images": map[string]any{
				CheckKeyBricks:               2,
				CheckKeySelfHeal:             true,
				CheckKeyMaxHealEntries:       0,
				CheckKeyMaxSplitBrainEntries: 0,
			},
		},
	}
}

func TestGlusterClusterCheckHealthy(t *testing.T) {
	runner := &glusterClusterRunner{results: glusterClusterResults()}
	peers, volumes, err := parseGlusterClusterConfig(glusterClusterExpectation())
	if err != nil {
		t.Fatal(err)
	}
	check := glusterClusterCheck{base: base{name: "cluster", timeout: time.Second}, runner: runner, peers: peers, volumes: volumes}
	result := check.Run(context.Background())
	if !result.OK || result.Unavailable {
		t.Fatalf("healthy result = %+v", result)
	}
	if !runner.hasDeadline {
		t.Fatal("gluster CLI commands did not receive the check deadline")
	}
	if got, want := len(runner.calls), 5; got != want {
		t.Fatalf("gluster CLI calls = %d, want %d: %v", got, want, runner.calls)
	}
	for _, want := range []string{
		"gluster --mode=script --xml peer status",
		"gluster --mode=script --xml volume info",
		"gluster --mode=script --xml volume status",
		"gluster --mode=script --xml volume heal images info",
		"gluster --mode=script --xml volume heal images info split-brain",
	} {
		if !strings.Contains(strings.Join(runner.calls, "\n"), want) {
			t.Errorf("missing fixed read-only call %q in %v", want, runner.calls)
		}
	}
	for key, want := range map[string]int{
		DataKeyGlusterPeersConnected:    1,
		DataKeyGlusterPeersExpected:     1,
		DataKeyGlusterVolumesStarted:    1,
		DataKeyGlusterVolumesExpected:   1,
		DataKeyGlusterBricksOnline:      2,
		DataKeyGlusterBricksExpected:    2,
		DataKeyGlusterSelfHealOnline:    1,
		DataKeyGlusterSelfHealTotal:     1,
		DataKeyGlusterHealEntries:       0,
		DataKeyGlusterSplitBrainEntries: 0,
	} {
		if got := result.Data[key]; got != want {
			t.Errorf("data[%s] = %v, want %d", key, got, want)
		}
	}
}

func TestGlusterClusterCheckReportsTopologyFailures(t *testing.T) {
	results := glusterClusterResults()
	results["gluster --mode=script --xml peer status"] = glusterXML(`
<peerStatus><peer><hostname>zeus</hostname><connected>0</connected><state>2</state><stateStr>Peer Rejected</stateStr></peer></peerStatus>`)
	results["gluster --mode=script --xml volume info"] = glusterXML(`
<volInfo><volumes><volume><name>images</name><status>0</status><statusStr>Stopped</statusStr><brickCount>1</brickCount></volume></volumes></volInfo>`)
	results["gluster --mode=script --xml volume status"] = glusterXML(`
<volStatus><volumes><volume><volName>images</volName>
  <node><hostname>sirio</hostname><path>/bricks/images0</path><status>0</status></node>
</volume></volumes></volStatus>`)
	runner := &glusterClusterRunner{results: results}
	peers, volumes, err := parseGlusterClusterConfig(glusterClusterExpectation())
	if err != nil {
		t.Fatal(err)
	}
	result := (glusterClusterCheck{base: base{name: "cluster", timeout: time.Second}, runner: runner, peers: peers, volumes: volumes}).Run(context.Background())
	if result.OK || result.Unavailable {
		t.Fatalf("topology result = %+v, want healthy failure", result)
	}
	for _, want := range []string{"peer zeus is disconnected", "volume images is not started", "volume images has 1 bricks (want 2)", "brick sirio:/bricks/images0 is offline", "has no self-heal daemon"} {
		if !strings.Contains(result.Message, want) {
			t.Errorf("message %q does not contain %q", result.Message, want)
		}
	}
	if issues, ok := result.Data[DataKeyGlusterIssues].([]string); !ok || len(issues) < 5 {
		t.Errorf("issue data = %#v, want topology details", result.Data[DataKeyGlusterIssues])
	}
}

func TestGlusterClusterCheckCommandFailureIsUnavailable(t *testing.T) {
	runner := &glusterClusterRunner{results: map[string]execx.Result{}, err: errors.New("gluster binary not found")}
	result := (glusterClusterCheck{base: base{name: "cluster", timeout: time.Second}, runner: runner, peers: []string{"zeus"}}).Run(context.Background())
	if result.OK || !result.Unavailable || !strings.Contains(result.Message, "gluster binary not found") {
		t.Fatalf("command failure = %+v", result)
	}
}

func TestBuildGlusterClusterCheckRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{name: "empty", entry: map[string]any{}, want: "requires peers and/or volumes"},
		{name: "unsafe volume", entry: map[string]any{CheckKeyVolumes: map[string]any{"bad/name": map[string]any{CheckKeyBricks: 1}}}, want: "unsafe name"},
		{name: "bad heal limit", entry: map[string]any{CheckKeyVolumes: map[string]any{"images": map[string]any{CheckKeyBricks: 1, CheckKeyMaxHealEntries: -1}}}, want: "non-negative integer"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, warning := buildGlusterClusterCheck(base{name: "cluster", timeout: time.Second}, test.entry, fakeRunner{})
			if !strings.Contains(warning, test.want) {
				t.Fatalf("warning = %q, want %q", warning, test.want)
			}
		})
	}
}

func TestGlusterClusterVolumeSchema(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		valid bool
	}{
		{name: "volume letters digits punctuation", value: "images_raid5.2026-08", valid: true},
		{name: "volume slash", value: "images/raid5", valid: false},
		{name: "volume empty", value: "", valid: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsGlusterVolumeName(test.value); got != test.valid {
				t.Errorf("IsGlusterVolumeName(%q) = %t, want %t", test.value, got, test.valid)
			}
		})
	}

	for _, test := range []struct {
		field string
		valid bool
	}{
		{field: CheckKeyBricks, valid: true},
		{field: CheckKeySelfHeal, valid: true},
		{field: CheckKeyMaxHealEntries, valid: true},
		{field: CheckKeyMaxSplitBrainEntries, valid: true},
		{field: "unexpected", valid: false},
	} {
		t.Run("field "+test.field, func(t *testing.T) {
			if got := IsGlusterClusterVolumeField(test.field); got != test.valid {
				t.Errorf("IsGlusterClusterVolumeField(%q) = %t, want %t", test.field, got, test.valid)
			}
		})
	}
}

func TestParseGlusterPeersNormalizesOnce(t *testing.T) {
	tests := []struct {
		name    string
		raw     any
		want    []string
		wantErr string
	}{
		{name: "sorts trimmed names", raw: []any{" zeus", "apolo "}, want: []string{"apolo", "zeus"}},
		{name: "duplicates after trimming", raw: []any{"zeus", " zeus "}, wantErr: "duplicate"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			peers, err := parseGlusterPeers(test.raw)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("parseGlusterPeers(%#v) error = %v, want %q", test.raw, err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(peers, test.want) {
				t.Errorf("parseGlusterPeers(%#v) = %v, want %v", test.raw, peers, test.want)
			}
		})
	}
}
