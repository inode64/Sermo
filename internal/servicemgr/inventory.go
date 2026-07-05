package servicemgr

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/execx"
)

// ListActiveUnits returns active service units for the selected init backend.
func ListActiveUnits(ctx context.Context, backend Backend, runner execx.Runner, timeout time.Duration) ([]string, error) {
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	switch backend {
	case BackendSystemd:
		res, err := execx.Run(ctx, runner, timeout, cmdSystemctl, "list-units", "--type=service", "--state=active", "--no-legend", "--no-pager")
		if err != nil && strings.TrimSpace(res.Stdout) == "" {
			return nil, err
		}
		return ParseSystemdActiveUnits(res.Stdout), nil
	case BackendOpenRC:
		res, err := execx.Run(ctx, runner, timeout, cmdRcStatus, "--all")
		if err != nil && strings.TrimSpace(res.Stdout) == "" {
			return nil, err
		}
		return ParseOpenRCActiveUnits(res.Stdout), nil
	default:
		return nil, fmt.Errorf("no active-unit listing for backend %q", backend)
	}
}

// ParseSystemdActiveUnits extracts active .service units from systemctl output.
func ParseSystemdActiveUnits(stdout string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 || fields[0] == "UNIT" {
			continue
		}
		if strings.HasSuffix(fields[0], ".service") {
			out = append(out, fields[0])
		}
	}
	return uniqueStrings(nil, out...)
}

// ParseOpenRCActiveUnits extracts started services from rc-status output.
func ParseOpenRCActiveUnits(stdout string) []string {
	var out []string
	inServiceRunlevel := false
	sc := bufio.NewScanner(strings.NewReader(stdout))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		lower := strings.ToLower(line)
		switch {
		case strings.HasPrefix(lower, "runlevel:"):
			name := strings.TrimSpace(strings.TrimPrefix(lower, "runlevel:"))
			inServiceRunlevel = openRCServiceRunlevel(name)
			continue
		case strings.HasPrefix(lower, "dynamic runlevel:"):
			name := strings.TrimSpace(strings.TrimPrefix(lower, "dynamic runlevel:"))
			inServiceRunlevel = openRCServiceRunlevel(name)
			continue
		}
		if !inServiceRunlevel || !strings.Contains(lower, "started") {
			continue
		}
		if strings.Contains(lower, "not started") || strings.Contains(lower, "stopped") || strings.Contains(lower, "crashed") {
			continue
		}
		beforeState := line
		if i := strings.Index(beforeState, "["); i >= 0 {
			beforeState = beforeState[:i]
		}
		fields := strings.Fields(beforeState)
		if len(fields) == 0 {
			continue
		}
		out = append(out, fields[0])
	}
	// A service can appear in more than one matched runlevel, and duplicates are
	// not guaranteed to be adjacent.
	return uniqueStrings(nil, out...)
}

func openRCServiceRunlevel(name string) bool {
	switch name {
	case "default", "needed/wanted", "manual", "hotplugged":
		return true
	default:
		return false
	}
}

func uniqueStrings(list []string, values ...string) []string {
	seen := make(map[string]struct{}, len(list)+len(values))
	for _, value := range list {
		if value == "" {
			continue
		}
		seen[value] = struct{}{}
	}
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		list = append(list, value)
	}
	return list
}
