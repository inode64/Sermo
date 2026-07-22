package cliutil

import (
	"errors"
	"testing"
)

func TestNormalizePflagError(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{name: "unknown flag rewritten", in: "unknown flag: --bogus", want: "unknown flag --bogus"},
		{name: "unknown shorthand kept", in: "unknown shorthand flag: 'x' in -x", want: "unknown shorthand flag: 'x' in -x"},
		{name: "other error unchanged", in: "invalid argument", want: "invalid argument"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := NormalizePflagError(errors.New(tc.in))
			if got.Error() != tc.want {
				t.Fatalf("NormalizePflagError(%q) = %q, want %q", tc.in, got.Error(), tc.want)
			}
		})
	}
}

func TestNormalizePflagErrorKeepsIdentity(t *testing.T) {
	err := errors.New("permission denied")
	if got := NormalizePflagError(err); !errors.Is(got, err) {
		t.Fatalf("NormalizePflagError changed identity of a non-pflag error: %v", got)
	}
}
