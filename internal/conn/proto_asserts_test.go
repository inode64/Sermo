package conn

import (
	"context"
	"testing"
)

// assertProbeExtra probes proto against a local server on port and asserts the
// Extra value recorded under key.
func assertProbeExtra(t *testing.T, proto Protocol, port int, key, want string) {
	t.Helper()
	res, err := proto.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Extra[key] != want {
		t.Fatalf("%s = %q, want %q", key, res.Extra[key], want)
	}
}

// runMapCases exercises a pure mapping function over an input→want table.
func runMapCases[K, V comparable](t *testing.T, fnName string, fn func(K) V, cases map[K]V) {
	t.Helper()
	for in, want := range cases {
		if got := fn(in); got != want {
			t.Errorf("%s(%v) = %v, want %v", fnName, in, got, want)
		}
	}
}
