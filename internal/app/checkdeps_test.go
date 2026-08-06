package app

import (
	"errors"
	"testing"
	"time"

	"sermo/internal/checks"
)

func TestCheckDepsFromAppDepsPreservesSamplerBundle(t *testing.T) {
	wantErr := errors.New("sampled")
	deps := Deps{
		Samplers: checks.Samplers{
			MemorySampler: func() (checks.MemorySample, error) { return checks.MemorySample{}, wantErr },
			UsersSampler:  func() (int, error) { return 7, wantErr },
		},
	}
	got := checkDepsFromAppDeps(deps, checks.Deps{Service: "web", DefaultTimeout: time.Second})
	if got.Service != "web" || got.DefaultTimeout != time.Second {
		t.Fatalf("base deps not preserved: %+v", got)
	}
	if _, err := got.MemorySampler(); !errors.Is(err, wantErr) {
		t.Fatalf("memory sampler error = %v, want shared bundle sampler", err)
	}
	if users, err := got.UsersSampler(); users != 7 || !errors.Is(err, wantErr) {
		t.Fatalf("users sampler = %d, %v; want shared bundle sampler", users, err)
	}
}
