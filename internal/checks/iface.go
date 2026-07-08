package checks

import (
	"fmt"
	"sermo/internal/cfgval"
)

const interfaceResultOK = "ok"

// parseInterfaces reads the optional `interface` field: a single identifier
// (name/IP/MAC) or a list of them. An empty/absent value means default routing.
func parseInterfaces(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		out := make([]string, 0, len(t))
		for _, e := range t {
			if s := cfgval.AsString(e); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

// parseInterfaceMatch reads `interface_match` (any|all, default any → false).
// The bool reports whether ALL listed interfaces must succeed.
func parseInterfaceMatch(entry map[string]any) (all bool, warn string) {
	switch m := cfgval.AsString(entry[CheckKeyInterfaceMatch]); m {
	case "", InterfaceMatchAny:
		return false, ""
	case InterfaceMatchAll:
		return true, ""
	default:
		return false, fmt.Sprintf("interface_match %q must be %s", m, InterfaceMatchSummary)
	}
}

// tryInterfaces runs op once per interface, aggregating per matchAll: with
// matchAll=false (any) it succeeds on the first interface whose op returns nil;
// with matchAll=true (all) it fails on the first op error. With no interfaces it
// runs op once with "" (default routing). It returns the deciding interface, the
// resulting error (nil on success) and a per-interface outcome map for the
// result data.
func tryInterfaces(ifaces []string, matchAll bool, op func(iface string) error) (chosen string, perIface map[string]any, err error) {
	if len(ifaces) == 0 {
		return "", nil, op("")
	}
	perIface = make(map[string]any, len(ifaces))
	var lastErr error
	for _, ifc := range ifaces {
		e := op(ifc)
		if e == nil {
			perIface[ifc] = interfaceResultOK
			chosen = ifc
			if !matchAll {
				return ifc, perIface, nil // any: first success wins
			}
		} else {
			perIface[ifc] = e.Error()
			lastErr = e
			if matchAll {
				return ifc, perIface, e // all: first failure loses
			}
		}
	}
	if matchAll {
		return chosen, perIface, nil // all succeeded
	}
	return chosen, perIface, lastErr // any: none succeeded
}

// ifaceSuffix annotates a result message with the chosen interface (when one was
// selected from a configured set).
func ifaceSuffix(iface string) string {
	if iface == "" {
		return ""
	}
	return " via " + iface
}

// ifaceData returns result data carrying the per-interface outcomes, or nil when
// no interface set was configured.
func ifaceData(perIface map[string]any) map[string]any {
	if perIface == nil {
		return nil
	}
	return map[string]any{DataKeyInterfaces: perIface}
}
