package checks

import (
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/process"
	"sermo/internal/strutil"
)

// PrivateTmpFunc reports a service's systemd PrivateTmp mounts whose host
// directory was deleted.
type PrivateTmpFunc func() ([]process.LostPrivateTmp, error)

// PrivateTmpRestartHint is the only remedy: systemd builds a new namespace,
// and with it the private directories, when the unit starts again.
const PrivateTmpRestartHint = "systemd cannot run the unit's reload or stop commands until it restarts"

// privateTmpCheck reports the service's processes whose PrivateTmp directory
// was deleted on the host. It is a condition check like stale_binary: OK means
// nothing was lost, so a rule fires on it with `failed:`.
type privateTmpCheck struct {
	base
	lost PrivateTmpFunc
}

func (c privateTmpCheck) Run(_ context.Context) Result {
	start := time.Now()
	lost, err := c.lost()
	if err != nil {
		return c.unavailableResult("cannot inspect private /tmp: "+err.Error(), start)
	}
	message := "no private /tmp lost"
	if len(lost) > 0 {
		message = fmt.Sprintf("private %s of pid %s deleted on the host (%s); %s",
			strings.Join(lostPrivateTmpField(lost, func(l process.LostPrivateTmp) string { return l.MountPoint }), ", "),
			joinedPIDs(lost, func(l process.LostPrivateTmp) int { return l.PID }),
			strings.Join(lostPrivateTmpField(lost, func(l process.LostPrivateTmp) string { return l.Root }), ", "),
			PrivateTmpRestartHint)
	}
	res := c.result(len(lost) == 0, message, start)
	res.Data = map[string]any{DataKeyType: CheckTypePrivateTmp, DataKeyValue: float64(len(lost))}
	if len(lost) > 0 {
		res.Data[DataKeyPIDs] = joinedPIDs(lost, func(l process.LostPrivateTmp) int { return l.PID })
		res.Data[DataKeyPath] = strings.Join(lostPrivateTmpField(lost, func(l process.LostPrivateTmp) string { return l.Root }), ",")
	}
	return res
}

func lostPrivateTmpField(lost []process.LostPrivateTmp, field func(process.LostPrivateTmp) string) []string {
	values := make([]string, 0, len(lost))
	for _, l := range lost {
		values = append(values, field(l))
	}
	return strutil.SortedUnique(values)
}

// buildPrivateTmpCheck takes no entry fields: it inspects the service's own
// processes, like stale_binary.
func buildPrivateTmpCheck(b base, deps Deps) (Check, string) {
	if deps.PrivateTmp == nil {
		return nil, "private_tmp check needs process discovery, unavailable here"
	}
	return privateTmpCheck{base: b, lost: deps.PrivateTmp}, ""
}
