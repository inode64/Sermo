package checks

import (
	"context"
	"errors"
	"testing"
)

func fakeUsers(n int, err error) UsersSamplerFunc {
	return func() (int, error) { return n, err }
}

func TestUsersThreshold(t *testing.T) {
	over := usersCheck{base: base{name: "u"}, preds: []levelPred{{"count", ">", 2}}, sampler: fakeUsers(5, nil)}
	res := over.Run(context.Background())
	if !res.OK {
		t.Fatalf("5 users should breach > 2, got %q", res.Message)
	}
	if res.Data["count"].(int) != 5 || res.Data["value"].(float64) != 5 {
		t.Fatalf("unexpected data: %+v", res.Data)
	}

	under := usersCheck{base: base{name: "u"}, preds: []levelPred{{"count", ">", 2}}, sampler: fakeUsers(1, nil)}
	if under.Run(context.Background()).OK {
		t.Fatal("1 user should not breach > 2")
	}
}

func TestUsersSamplerErrorFails(t *testing.T) {
	c := usersCheck{base: base{name: "u"}, preds: []levelPred{{"count", ">", 0}}, sampler: fakeUsers(0, errors.New("no utmp"))}
	res := c.Run(context.Background())
	if res.OK {
		t.Fatal("a sampler error must not fire OK")
	}
	if res.Message == "" {
		t.Fatal("error result should carry a message")
	}
}

func TestBuildUsersCheck(t *testing.T) {
	// 3 users satisfies >= 3; a threshold-less users check is meaningless and warns.
	assertBuildThresholdFires(t, "users",
		map[string]any{"count": map[string]any{"op": ">=", "value": 3.0}},
		Deps{UsersSampler: fakeUsers(3, nil)})
}
