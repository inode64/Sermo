package servicemgr

import (
	"bufio"
	"context"
	"fmt"
	"strings"
	"time"

	"sermo/internal/execx"
)

const (
	openRCRunlevelPrefix        = "runlevel:"
	openRCDynamicRunlevelPrefix = "dynamic runlevel:"
	openRCRunlevelDefault       = "default"
	openRCRunlevelNeededWanted  = "needed/wanted"
	openRCRunlevelManual        = "manual"
	openRCRunlevelHotplugged    = "hotplugged"
	openRCStateStarted          = "started"
	openRCStateNotStarted       = "not started"
	openRCStateStopped          = "stopped"
	openRCStateCrashed          = "crashed"
	systemdListUnitNameIndex    = 0
	openRCServiceNameIndex      = 0
)

// ListActiveUnits returns active service units for the selected init backend.
func ListActiveUnits(ctx context.Context, backend Backend, runner execx.Runner, timeout time.Duration) ([]string, error) {
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	switch backend {
	case BackendSystemd:
		res, err := execx.Run(ctx, runner, timeout, cmdSystemctl, systemctlCmdListUnits, systemctlFlagTypeService, systemctlFlagStateActive, systemctlFlagNoLegend, systemctlFlagNoPager)
		if err != nil && strings.TrimSpace(res.Stdout) == "" {
			return nil, err
		}
		return ParseSystemdActiveUnits(res.Stdout), nil
	case BackendOpenRC:
		res, err := execx.Run(ctx, runner, timeout, cmdRcStatus, openRCFlagAll)
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
		if len(fields) <= systemdListUnitNameIndex || fields[systemdListUnitNameIndex] == systemdUnitHeader {
			continue
		}
		if strings.HasSuffix(fields[systemdListUnitNameIndex], systemdServiceSuffix) {
			out = append(out, fields[systemdListUnitNameIndex])
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
		if name, ok := openRCRunlevel(line); ok {
			inServiceRunlevel = openRCServiceRunlevel(name)
			continue
		}
		if !inServiceRunlevel {
			continue
		}
		if service := openRCStartedService(line); service != "" {
			out = append(out, service)
		}
	}
	// A service can appear in more than one matched runlevel, and duplicates are
	// not guaranteed to be adjacent.
	return uniqueStrings(nil, out...)
}

func openRCRunlevel(line string) (string, bool) {
	lower := strings.ToLower(line)
	if name, ok := strings.CutPrefix(lower, openRCRunlevelPrefix); ok {
		return strings.TrimSpace(name), true
	}
	if name, ok := strings.CutPrefix(lower, openRCDynamicRunlevelPrefix); ok {
		return strings.TrimSpace(name), true
	}
	return "", false
}

func openRCServiceRunlevel(name string) bool {
	switch name {
	case openRCRunlevelDefault, openRCRunlevelNeededWanted, openRCRunlevelManual, openRCRunlevelHotplugged:
		return true
	default:
		return false
	}
}

func openRCStartedService(line string) string {
	lower := strings.ToLower(line)
	if !strings.Contains(lower, openRCStateStarted) {
		return ""
	}
	if strings.Contains(lower, openRCStateNotStarted) ||
		strings.Contains(lower, openRCStateStopped) ||
		strings.Contains(lower, openRCStateCrashed) {
		return ""
	}
	beforeState, _, _ := strings.Cut(line, "[")
	fields := strings.Fields(beforeState)
	if len(fields) <= openRCServiceNameIndex {
		return ""
	}
	return fields[openRCServiceNameIndex]
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
