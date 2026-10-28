package servicemgr

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

// A failed-unit listing is not restricted to .service: a failed mount or timer
// is a fault too, and older systemd prints the status bullet as its own field
// even with --plain.
func TestParseSystemdFailedUnits(t *testing.T) {
	stdout := "UNIT LOAD ACTIVE SUB DESCRIPTION\n" +
		"backup_kvm.service loaded failed failed Backup de las maquinas virtuales\n" +
		"● cleanup.timer loaded failed failed Nightly cleanup\n" +
		"srv-data.mount loaded failed failed /srv/data\n" +
		"●\n"
	want := []string{"backup_kvm.service", "cleanup.timer", "srv-data.mount"}
	if got := ParseSystemdFailedUnits(stdout); !slices.Equal(got, want) {
		t.Fatalf("ParseSystemdFailedUnits() = %v, want %v", got, want)
	}
	if got := ParseSystemdFailedUnits(""); got != nil {
		t.Fatalf("empty listing = %v, want nil", got)
	}
}

// ParseSystemdActiveUnits keeps its .service-only contract: the failed listing
// widened the shared parser, not that one.
func TestParseSystemdActiveUnitsStaysServiceOnly(t *testing.T) {
	stdout := "nginx.service loaded active running nginx\n" +
		"srv-data.mount loaded active mounted /srv/data\n"
	want := []string{"nginx.service"}
	if got := ParseSystemdActiveUnits(stdout); !slices.Equal(got, want) {
		t.Fatalf("ParseSystemdActiveUnits() = %v, want %v", got, want)
	}
}

// `crashed` is OpenRC's failure state, and only service runlevels are scanned —
// the same gating the active listing uses.
func TestParseOpenRCFailedUnits(t *testing.T) {
	stdout := "Runlevel: default\n" +
		" sshd                    [  started  ]\n" +
		" crashed-reporter        [  started  ]\n" +
		" backup                  [  crashed  ]\n" +
		" nfs                     [  stopped  ]\n" +
		"Dynamic Runlevel: manual\n" +
		" custom                  [  crashed  ]\n" +
		"Runlevel: shutdown\n" +
		" killprocs               [  crashed  ]\n"
	want := []string{"backup", "custom"}
	if got := ParseOpenRCFailedUnits(stdout); !slices.Equal(got, want) {
		t.Fatalf("ParseOpenRCFailedUnits() = %v, want %v", got, want)
	}
	if got := ParseOpenRCActiveUnits(stdout); !slices.Equal(got, []string{"sshd", "crashed-reporter"}) {
		t.Fatalf("ParseOpenRCActiveUnits() = %v, want [sshd crashed-reporter]", got)
	}
}

// OpenRC records each started service as an entry of its started directory,
// a symlink to the init script; the listing is the entry names, crashed
// daemons included, and a missing directory is an error rather than no units.
func TestListOpenRCStartedUnits(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"php-fpm8.4", "sshd", "symfony-messenger_default_async.1"} {
		if err := os.Symlink(filepath.Join("/etc/init.d", name), filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"php-fpm8.4", "sshd", "symfony-messenger_default_async.1"}
	if got, err := listOpenRCStartedUnits(dir); err != nil || !slices.Equal(got, want) {
		t.Fatalf("listOpenRCStartedUnits() = %v, %v; want %v", got, err, want)
	}
	if _, err := listOpenRCStartedUnits(filepath.Join(dir, "missing")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing started dir err = %v, want not exist", err)
	}
}

// The snapshot lists the active units once per TTL however many units are
// asked about, answers only units the listing names as active, and a failed
// listing answers nothing until the next refresh.
func TestActiveUnitSnapshotRefreshesOncePerTTL(t *testing.T) {
	runner := &execxtest.Runner{ByLine: map[string]execx.Result{
		"rc-status --all": {Stdout: "Runlevel: default\n sshd [  started  ]\n backup [  crashed  ]\n nfs [  stopped  ]\n"},
	}}
	now := time.Unix(1000, 0)
	s := NewActiveUnitSnapshot(BackendOpenRC, runner, 15*time.Second, time.Second)
	s.now = func() time.Time { return now }

	for unit, want := range map[string]bool{"sshd": true, "backup": false, "nfs": false, "ghost": false} {
		if got := s.Active(t.Context(), unit); got != want {
			t.Fatalf("Active(%s) = %v, want %v", unit, got, want)
		}
	}
	if n := runner.Count("rc-status", "--all"); n != 1 {
		t.Fatalf("listings within one TTL = %d, want 1", n)
	}

	now = now.Add(15 * time.Second)
	runner.ByLine["rc-status --all"] = execx.Result{ExitCode: 1}
	runner.Errs = map[string]error{"rc-status": errors.New("rc-status failed")}
	if s.Active(t.Context(), "sshd") {
		t.Fatal("a failed listing must not answer active")
	}
	if s.Active(t.Context(), "sshd") || runner.Count("rc-status", "--all") != 2 {
		t.Fatalf("listings after a failure = %d, want 2 (no retry before the TTL)", runner.Count("rc-status", "--all"))
	}
}

// systemd names are normalized before the lookup, and concurrent callers share
// one listing.
func TestActiveUnitSnapshotNormalizesAndSharesOneListing(t *testing.T) {
	runner := &execxtest.Runner{ByName: map[string]execx.Result{
		"systemctl": {Stdout: "nginx.service loaded active running nginx\n"},
	}}
	s := NewActiveUnitSnapshot(BackendSystemd, runner, time.Minute, time.Second)
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if !s.Active(t.Context(), "nginx") {
				t.Error("Active(nginx) = false, want the listed nginx.service")
			}
		})
	}
	wg.Wait()
	if n := len(runner.Calls()); n != 1 {
		t.Fatalf("listings = %d, want 1 shared by concurrent callers", n)
	}
	var nilSnapshot *ActiveUnitSnapshot
	if nilSnapshot.Active(t.Context(), "nginx") {
		t.Fatal("a nil snapshot must answer nothing")
	}
}
