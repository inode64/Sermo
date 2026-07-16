package checks

import (
	"context"
	"strings"
	"testing"
)

func TestProcessCountThreshold(t *testing.T) {
	counter := func(user, exe, exeDir string) int { return 5 }
	over := processCountCheck{base: base{name: "p"}, preds: []levelPred{{"count", ">", 2}}, count: counter}
	res := over.Run(context.Background())
	if !res.OK {
		t.Fatalf("5 processes should breach > 2, got %q", res.Message)
	}
	if res.Data["count"].(int) != 5 || res.Data["value"].(float64) != 5 {
		t.Fatalf("unexpected data: %+v", res.Data)
	}

	under := processCountCheck{base: base{name: "p"}, preds: []levelPred{{"count", ">", 9}}, count: counter}
	if under.Run(context.Background()).OK {
		t.Fatal("5 processes should not breach > 9")
	}
}

func TestProcessCountPassesFilterAndScopeMessage(t *testing.T) {
	var gotUser, gotExe, gotDir string
	counter := func(user, exe, exeDir string) int {
		gotUser, gotExe, gotDir = user, exe, exeDir
		return 1
	}
	c := processCountCheck{
		base:   base{name: "p"},
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

func TestBuildProcessCountCheck(t *testing.T) {
	// 200 processes satisfies > 100; a threshold-less process_count check warns.
	assertBuildThresholdFires(t, "process_count",
		map[string]any{"user": "www-data", "count": map[string]any{"op": ">", "value": 100.0}},
		Deps{ProcessCount: func(string, string, string) int { return 200 }})
}
