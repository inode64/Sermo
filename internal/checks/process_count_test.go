package checks

import (
	"context"
	"strings"
	"testing"
)

func TestProcessCountThreshold(t *testing.T) {
	counter := func(user, exe, exeDir string) (int, bool) { return 5, true }
	over := processCountCheck{name: "p", preds: []levelPred{{"count", ">", 2}}, count: counter}
	res := over.Run(context.Background())
	if !res.OK {
		t.Fatalf("5 processes should breach > 2, got %q", res.Message)
	}
	if res.Data["count"].(int) != 5 || res.Data["value"].(float64) != 5 {
		t.Fatalf("unexpected data: %+v", res.Data)
	}

	under := processCountCheck{name: "p", preds: []levelPred{{"count", ">", 9}}, count: counter}
	if under.Run(context.Background()).OK {
		t.Fatal("5 processes should not breach > 9")
	}
}

func TestProcessCountPassesFilterAndScopeMessage(t *testing.T) {
	var gotUser, gotExe, gotDir string
	counter := func(user, exe, exeDir string) (int, bool) {
		gotUser, gotExe, gotDir = user, exe, exeDir
		return 1, true
	}
	c := processCountCheck{
		name:   "p",
		preds:  []levelPred{{"count", ">", 0}},
		user:   "www-data",
		exeDir: "/usr/sbin",
		count:  counter,
	}
	res := c.Run(context.Background())
	if gotUser != "www-data" || gotExe != "" || gotDir != "/usr/sbin" {
		t.Fatalf("filter not passed through: user=%q exe=%q dir=%q", gotUser, gotExe, gotDir)
	}
	if !strings.Contains(res.Message, "user=www-data") || !strings.Contains(res.Message, "exe under /usr/sbin") {
		t.Fatalf("scope message missing filters: %q", res.Message)
	}
}

// A count that proves nothing must reach a guard as unavailable: read as zero it
// would let a block rule on active jobs authorize the stop it exists to deny.
func TestProcessCountUnknownIsUnavailable(t *testing.T) {
	c := processCountCheck{
		name:  "p",
		preds: []levelPred{{"count", ">", 1}},
		count: func(string, string, string) (int, bool) { return 0, false },
	}
	res := c.Run(context.Background())
	if !res.Unavailable || res.OK {
		t.Fatalf("result = %+v, want unavailable", res)
	}
	if !strings.Contains(res.Message, "unknown") {
		t.Fatalf("message = %q, want it to say the count is unknown", res.Message)
	}
}

func TestBuildProcessCountCheck(t *testing.T) {
	// 200 processes satisfies > 100; a threshold-less process_count check warns.
	assertBuildThresholdFires(t, "process_count",
		map[string]any{"user": "www-data", "count": map[string]any{"op": ">", "value": 100.0}},
		Deps{ProcessCount: func(string, string, string) (int, bool) { return 200, true }})
}
