package config

import "testing"

func TestValidateSupervisedProcessWatch(t *testing.T) {
	entry := map[string]any{"type": "supervised_process", "exe": "/usr/bin/worker", "user": "app", "cgroup": "/openrc.worker", "supervisor": map[string]any{"exe": "/usr/bin/supervise-daemon", "user": "root"}, "timeout": "10s"}
	assertNoWatchIssues(t, watchConfig("worker", entry))
	entry["cgroup"] = "/"
	assertWatchIssues(t, watchConfig("worker", entry), "cgroup must be an absolute clean non-root path")
}
