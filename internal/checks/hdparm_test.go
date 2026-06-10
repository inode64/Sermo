package checks

import (
	"context"
	"slices"
	"testing"
	"time"

	"sermo/internal/execx"
)

const hdparmSample = "/dev/sda:\n" +
	" Timing cached reads:   18000 MB in  2.00 seconds = 9000.00 MB/sec\n" +
	" Timing buffered disk reads: 500 MB in  3.00 seconds = 166.67 MB/sec\n"

func TestParseHdparm(t *testing.T) {
	v, err := parseHdparm(hdparmSample)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if v["cached"] != 9000.0 || v["read"] != 166.67 {
		t.Fatalf("got %v, want cached 9000 read 166.67", v)
	}
	// GB amount in the "… in …" part must not be mistaken for the MB/sec rate.
	v2, err := parseHdparm(" Timing buffered disk reads: 2 GB in 3.00 seconds = 700.50 MB/sec\n")
	if err != nil || v2["read"] != 700.5 {
		t.Fatalf("GB variant: %v, %v", v2, err)
	}
	if _, err := parseHdparm("no timing here\n"); err == nil {
		t.Fatal("output without a timing line must error")
	}
}

// recordingRunner captures the argv it was asked to run and returns a fixed
// stdout, so a test can assert which hdparm flags were used.
type recordingRunner struct {
	out  string
	args []string
}

func (r *recordingRunner) Run(_ context.Context, name string, args ...string) (execx.Result, error) {
	r.args = append([]string{name}, args...)
	return execx.Result{Stdout: r.out}, nil
}

func TestHdparmRunsOnlyNeededTimings(t *testing.T) {
	cases := []struct {
		name      string
		preds     []hdparmPred
		wantFlags []string
		noFlags   []string
	}{
		{"read only runs -t", []hdparmPred{{"read", ">", 0}}, []string{"-t"}, []string{"-T"}},
		{"cached only runs -T", []hdparmPred{{"cached", ">", 0}}, []string{"-T"}, []string{"-t"}},
		{"both run -t and -T", []hdparmPred{{"read", ">", 0}, {"cached", ">", 0}}, []string{"-t", "-T"}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rr := &recordingRunner{out: hdparmSample}
			chk := hdparmCheck{base: base{name: "d", timeout: time.Second}, runner: rr, device: "/dev/sda", preds: c.preds}
			chk.Run(context.Background())
			for _, f := range c.wantFlags {
				if !slices.Contains(rr.args, f) {
					t.Errorf("flag %q missing in %v", f, rr.args)
				}
			}
			for _, f := range c.noFlags {
				if slices.Contains(rr.args, f) {
					t.Errorf("flag %q must not be run in %v", f, rr.args)
				}
			}
			if !slices.Contains(rr.args, "/dev/sda") {
				t.Errorf("device missing in %v", rr.args)
			}
		})
	}
}

func TestHdparmThresholds(t *testing.T) {
	degraded := " Timing buffered disk reads: 100 MB in 3.00 seconds = 50.00 MB/sec\n"
	healthy := " Timing buffered disk reads: 500 MB in 3.00 seconds = 200.00 MB/sec\n"
	pred := []hdparmPred{{"read", "<", 100}} // alert condition: read below 100 MB/s

	// Degraded: read=50 < 100 -> the alert condition holds -> OK (fires as a watch).
	c := hdparmCheck{base: base{name: "d", timeout: time.Second}, runner: fakeRunner{execx.Result{Stdout: degraded}}, device: "/dev/sda", preds: pred}
	if res := c.Run(context.Background()); !res.OK {
		t.Errorf("read 50 < 100 should meet the alert condition: %s", res.Message)
	} else if res.Data["read"] != 50.0 {
		t.Errorf("Data[read] = %v, want 50", res.Data["read"])
	}

	// Healthy: read=200, not below 100 -> condition not met.
	c = hdparmCheck{base: base{name: "d", timeout: time.Second}, runner: fakeRunner{execx.Result{Stdout: healthy}}, device: "/dev/sda", preds: pred}
	if res := c.Run(context.Background()); res.OK {
		t.Error("read 200 must not meet the read<100 alert condition")
	}
}

func TestHdparmCheckError(t *testing.T) {
	c := hdparmCheck{
		base:   base{name: "d", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stderr: "/dev/sda: Permission denied\n", ExitCode: 1}},
		device: "/dev/sda",
		preds:  []hdparmPred{{"read", "<", 100}},
	}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("an hdparm error must fail the check")
	}
	if res.Message == "" {
		t.Error("failure message should carry the hdparm error")
	}
}

func TestParseHdparmPreds(t *testing.T) {
	if _, err := parseHdparmPreds(map[string]any{}); err == nil {
		t.Error("no predicate must error")
	}
	preds, err := parseHdparmPreds(map[string]any{"read": map[string]any{"op": "<", "value": 100}})
	if err != nil || len(preds) != 1 || preds[0].field != "read" || preds[0].value != 100 {
		t.Fatalf("preds = %v, err = %v", preds, err)
	}
	if _, err := parseHdparmPreds(map[string]any{"cached": map[string]any{"op": "=>", "value": 1}}); err == nil {
		t.Error("an invalid op must error")
	}
}
