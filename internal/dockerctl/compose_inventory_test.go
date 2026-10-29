package dockerctl

import (
	"errors"
	"strings"
	"testing"

	"sermo/internal/execx"
	"sermo/internal/execx/execxtest"
)

func TestComposeRegistrationUsesDiscoveryInventory(t *testing.T) {
	for _, tc := range []struct {
		name       string
		containers []ContainerSummary
		wantErr    bool
		wantLists  int
	}{
		{name: "nil refreshes inventory", wantLists: 1},
		{name: "confirmed empty inventory", containers: []ContainerSummary{}},
		{name: "existing target", containers: []ContainerSummary{{Names: []string{"/web"}, Labels: map[string]string{composeProjectLabel: "project", composeServiceLabel: "app"}}}},
		{name: "other replica", containers: []ContainerSummary{{Names: []string{"/other"}, Labels: map[string]string{composeProjectLabel: "project", composeServiceLabel: "app"}}}, wantErr: true},
		{name: "one-off", containers: []ContainerSummary{{Names: []string{"/web"}, Labels: map[string]string{composeProjectLabel: "project", composeServiceLabel: "app", composeOneoffLabel: "True"}}}, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, c, _ := recoveryFixture(t)
			c.container = Container{ID: "existing", Image: c.image.ID, Config: ContainerConfig{Labels: map[string]string{
				composeProjectLabel: "project", composeServiceLabel: "app", composeConfigHashLabel: "matching",
			}}}
			c.runner.Queue = []execx.Result{{Stdout: "app matching\n"}, c.runner.Queue[0]}
			if tc.containers != nil {
				c.listErr = errors.New("inventory must be reused")
			}
			err := m.ValidateCompose(t.Context(), tc.containers)
			if (err != nil) != tc.wantErr || c.listCalls != tc.wantLists {
				t.Fatalf("validation=%v listings=%d; want error=%v listings=%d", err, c.listCalls, tc.wantErr, tc.wantLists)
			}
			if c.started != "" || len(c.runner.Calls()) != 2 {
				t.Fatal("registration must only resolve and hash the source")
			}
		})
	}
}

func TestComposeCreationRefreshesInventoryAfterDiscovery(t *testing.T) {
	m, c, model := recoveryFixture(t)
	labels := map[string]string{composeProjectLabel: "project", composeServiceLabel: "app", composeConfigHashLabel: "matching"}
	c.container = Container{ID: "existing", Image: c.image.ID, Config: ContainerConfig{Labels: labels}}
	c.runner.Queue = []execx.Result{{Stdout: "app matching\n"}, c.runner.Queue[0]}
	inventory := []ContainerSummary{{Names: []string{"/web"}, Labels: labels}}
	if err := m.ValidateCompose(t.Context(), inventory); err != nil {
		t.Fatal(err)
	}
	if c.listCalls != 0 {
		t.Fatal("discovery did not reuse its inventory")
	}
	// The registered container disappears, then another target claims the same
	// service. A prior successful discovery must not authorize stale ownership.
	c.container = Container{}
	c.containers = []ContainerSummary{{Names: []string{"/replacement"}, Labels: labels}}
	c.runner = &execxtest.Runner{RespectContext: true}
	m.Runner = c.runner
	setComposeOutput(t, c.runner, model)
	if _, err := m.Create(t.Context()); err == nil || !strings.Contains(err.Error(), "another container") {
		t.Fatalf("creation accepted stale inventory: %v", err)
	}
	if c.listCalls != 1 || c.started != "" || len(c.runner.Calls()) != 1 {
		t.Fatalf("creation must refresh ownership and stop before mutation: lists=%d started=%s calls=%v", c.listCalls, c.started, c.runner.Calls())
	}
}
