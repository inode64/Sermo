package checks

import "testing"

func TestEvaluateStateTransition(t *testing.T) {
	tests := []struct {
		name          string
		current       string
		expected      string
		expectedLabel string
		primed        bool
		previous      string
		wantOK        bool
		wantMessage   string
		wantPrevious  string
		wantData      map[string]any
	}{
		{
			name: "expected network state", expected: NetStateDown, expectedLabel: NetMetricState,
			wantOK: true, wantMessage: "host state down (want down)", wantData: map[string]any{DataKeyValue: NetStateDown},
		},
		{
			name: "expected ICMP state", current: NetStateUp, expected: NetStateUp,
			wantOK: true, wantMessage: "host up (want up)", wantData: map[string]any{DataKeyValue: NetStateUp},
		},
		{
			name: "baseline", current: NetStateUp, wantMessage: "host state baseline up", wantPrevious: NetStateUp,
		},
		{
			name: "changed", primed: true, previous: NetStateUp,
			wantOK: true, wantMessage: "host state up->down", wantPrevious: NetStateDown,
			wantData: map[string]any{DataKeyOld: NetStateUp, DataKeyNew: NetStateDown, DataKeyValue: NetStateDown},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := map[string]any{}
			primed, previous := test.primed, test.previous
			current := test.current
			if current == "" {
				current = NetStateDown
			}
			ok, message := evaluateStateTransition(stateTransitionSpec{
				target: "host", current: current, expected: test.expected, expectedLabel: test.expectedLabel,
				data: data, primed: &primed, previous: &previous,
			})
			if ok != test.wantOK || message != test.wantMessage || previous != test.wantPrevious {
				t.Fatalf("result = ok:%v message:%q previous:%q", ok, message, previous)
			}
			if len(data) != len(test.wantData) {
				t.Fatalf("data = %v, want %v", data, test.wantData)
			}
			for key, want := range test.wantData {
				if data[key] != want {
					t.Fatalf("data[%q] = %v, want %v", key, data[key], want)
				}
			}
		})
	}
}
