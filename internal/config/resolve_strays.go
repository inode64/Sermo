package config

import (
	"sermo/internal/checks"
)

// straysCheckName is the injected check reporting control-group members that no
// selector claims.
const straysCheckName = "strays"

// expandStrays injects the strays check into every init-managed service that
// declares processes to discover. Like stale-binary the condition can hit any
// service — a probe that daemonizes, a child the daemon never reaps — so the
// sensor is not opt-in.
//
// Unlike stale-binary it injects **no rule**. A replaced binary is unambiguously a
// defect with one remedy; a stray is only unexplained, and on real hosts the raw
// condition is dominated by workloads a profile has legitimately marked
// `delegated: true` (container shims, Gluster bricks, libvirt's dnsmasq pairs).
// A fleet-wide alert would therefore mostly report incomplete catalog coverage,
// whose remedy is a selector rather than a reap. So the check reports state — it
// appears in the dashboard and in `sermoctl status` with the count and the
// executables, and never touches health or SLA — and an operator who wants to be
// paged writes a rule on it:
//
//	rules:
//	  alert-on-strays:
//	    if: { failed: { check: strays } }
//	    for: { cycles: 3 }
//	    then: { action: alert, message: "..." }
func expandStrays(tree map[string]any) []string {
	// An external control backend attributes a container's or domain's whole PID
	// set to the service, where a process no selector names is ordinary rather
	// than a leftover. Cgroup attribution by an init unit is what makes "outside
	// the principal's tree" mean reparented, so restrict the sensor to it.
	if _, external := tree[SectionControl]; external {
		return nil
	}
	if !serviceDeclaresProcesses(tree) {
		return nil
	}
	entry := map[string]any{
		checks.CheckKeyType: checks.CheckTypeStrays,
		// A stray is worth naming, not worth calling the service unhealthy: the
		// daemon itself is serving. Keep the raw result available for a rule's
		// `failed:` condition while excluding it from health and SLA accounting.
		checks.CheckKeyReports: checks.ReportsState,
	}
	if err := injectGenerated(tree, sectionChecks, straysCheckName, "check", checks.CheckTypeStrays, entry); err != "" {
		return []string{err}
	}
	return nil
}
