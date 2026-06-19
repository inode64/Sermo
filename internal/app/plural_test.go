package app

import "testing"

func TestPluralSuffix(t *testing.T) {
	cases := []struct {
		count int
		word  string
		want  string
	}{
		{1, "process", ""},
		{2, "process", "es"},   // processes
		{0, "address", "es"},   // addresses
		{3, "mountpoint", "s"}, // mountpoints, not mountpointes
		{1, "mountpoint", ""},
		{2, "box", "es"},   // boxes
		{2, "dish", "es"},  // dishes
		{2, "watch", "es"}, // watches
		{2, "port", "s"},   // ports
	}
	for _, c := range cases {
		if got := pluralSuffix(c.count, c.word); got != c.want {
			t.Errorf("pluralSuffix(%d, %q) = %q, want %q (%s%s)", c.count, c.word, got, c.want, c.word, c.want)
		}
	}
}
