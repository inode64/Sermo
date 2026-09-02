package web

import "testing"

func TestEventTarget(t *testing.T) {
	tests := []struct {
		name  string
		event Event
		want  string
	}{
		{name: "service", event: Event{Service: "web"}, want: "web"},
		{name: "watch", event: Event{Watch: "storage-root"}, want: "storage-root"},
		{name: "application", event: Event{App: "salt-minion"}, want: "salt-minion"},
		{name: "service takes precedence", event: Event{Service: "web", Watch: "storage-root", App: "salt-minion"}, want: "web"},
		{name: "no target", event: Event{}, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.event.Target(); got != tt.want {
				t.Errorf("Event.Target() = %q, want %q", got, tt.want)
			}
		})
	}
}
