package checks

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/utmp"
)

// UsersSamplerFunc reports the number of distinct logged-in users. Injected for
// tests; the default reads the system utmp database.
type UsersSamplerFunc func() (int, error)

// usersCheck compares the count of logged-in users (distinct user names in
// utmp) against a threshold. Like load/zombies it is a level check: OK==true
// means the `count` predicate holds, so a watch with `count: {op: '>', value: N}`
// fires when more than N users are logged in.
type usersCheck struct {
	base
	preds   []levelPred
	sampler UsersSamplerFunc
}

func (c usersCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultUsersSampler
	}
	n, err := sampler()
	if err != nil {
		return c.result(false, "users: "+err.Error(), start)
	}
	values := map[string]float64{"count": float64(n)}
	ok := levelPredsHold(c.preds, values)
	res := c.result(ok, fmt.Sprintf("%d user(s) logged in", n), start)
	res.Data = map[string]any{"count": n, fieldValue: float64(n)}
	return res
}

// defaultUsersSampler counts distinct users from the system utmp (returns an
// error off Linux, where there is no utmp).
func defaultUsersSampler() (int, error) {
	sessions, err := utmp.Sessions()
	if err != nil {
		return 0, err
	}
	return utmp.DistinctUsers(sessions), nil
}
