package app

import (
	"context"
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
			CertSampler: func(context.Context, string, string, string, bool) (checks.CertSample, error) {
				return checks.CertSample{Fingerprint: "shared"}, wantErr
			},
			SizeSampler: func(context.Context, string, bool) (int64, error) { return 42, wantErr },
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
	if cert, err := got.CertSampler(context.Background(), "host", "443", "host", true); cert.Fingerprint != "shared" || !errors.Is(err, wantErr) {
		t.Fatalf("cert sampler = %+v, %v; want shared bundle sampler", cert, err)
	}
	if size, err := got.SizeSampler(context.Background(), "/data", false); size != 42 || !errors.Is(err, wantErr) {
		t.Fatalf("size sampler = %d, %v; want shared bundle sampler", size, err)
	}
}
