package app

import "testing"

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"1024", 1024},
		{"1K", 1024},
		{"1k", 1024},
		{"500M", 500 * 1024 * 1024},
		{"5G", 5 * 1024 * 1024 * 1024},
		{"2T", 2 * 1024 * 1024 * 1024 * 1024},
		{"1.5G", 1610612736}, // 1.5 * 2^30
		{" 5G ", 5 * 1024 * 1024 * 1024},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if err != nil {
			t.Errorf("parseSize(%q) error = %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"", "G", "5X", "abc", "-5G"} {
		if _, err := parseSize(bad); err == nil {
			t.Errorf("parseSize(%q) = nil error, want failure", bad)
		}
	}
}
