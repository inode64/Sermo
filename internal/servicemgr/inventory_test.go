package servicemgr

import (
	"slices"
	"testing"
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
	if got := ParseOpenRCActiveUnits(stdout); !slices.Equal(got, []string{"sshd"}) {
		t.Fatalf("ParseOpenRCActiveUnits() = %v, want [sshd]", got)
	}
}
