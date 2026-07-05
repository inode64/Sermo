package mountctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FstabEntries parses /etc/fstab lines by field count: a 2-field line is a valid
// entry (no fstype/options), and the optional 3rd/4th fields must be guarded
// (a weakened bound would index past the slice). Comments and short lines skip.
func TestFstabEntries(t *testing.T) {
	p := filepath.Join(t.TempDir(), "fstab")
	content := strings.Join([]string{
		"# a comment",
		"",
		"/dev/sda1 /boot",               // exactly 2 fields -> kept, no fstype/options
		"/dev/sda2 / ext4",              // 3 fields -> fstype only
		"/dev/sda3 /home ext4 defaults", // 4 fields -> fstype + options
		"badline",                       // 1 field -> skipped
	}, "\n")
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	entries, err := FstabEntries(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(entries), entries)
	}
	if e := entries[0]; e.Source != "/dev/sda1" || e.Path != "/boot" || e.FSType != "" || e.Options != "" {
		t.Errorf("2-field entry = %+v, want source/boot with empty fstype+options", e)
	}
	if e := entries[1]; e.Path != "/" || e.FSType != "ext4" || e.Options != "" {
		t.Errorf("3-field entry = %+v, want / ext4 with empty options", e)
	}
	if e := entries[2]; e.Path != "/home" || e.FSType != "ext4" || e.Options != "defaults" {
		t.Errorf("4-field entry = %+v, want /home ext4 defaults", e)
	}
}
