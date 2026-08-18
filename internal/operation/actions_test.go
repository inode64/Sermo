package operation

import "testing"

func TestServiceActionSemantics(t *testing.T) {
	tests := []struct {
		action          string
		serviceAction   bool
		cascade         bool
		settlesAfter    bool
		canRemainActive bool
	}{
		{action: actionStart, serviceAction: true, cascade: true, settlesAfter: true, canRemainActive: true},
		{action: actionStop, serviceAction: true, cascade: true},
		{action: actionRestart, serviceAction: true, cascade: true, settlesAfter: true, canRemainActive: true},
		{action: actionReload, serviceAction: true, settlesAfter: true},
		{action: actionResume, serviceAction: true, settlesAfter: true, canRemainActive: true},
		{action: ActionRepair, serviceAction: true, settlesAfter: true, canRemainActive: true},
		{action: "alert"},
		{action: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.action, func(t *testing.T) {
			if got := IsServiceAction(test.action); got != test.serviceAction {
				t.Errorf("IsServiceAction(%q) = %t, want %t", test.action, got, test.serviceAction)
			}
			if got := CascadesAlsoApply(test.action); got != test.cascade {
				t.Errorf("CascadesAlsoApply(%q) = %t, want %t", test.action, got, test.cascade)
			}
			if got := SettlesAfter(test.action); got != test.settlesAfter {
				t.Errorf("SettlesAfter(%q) = %t, want %t", test.action, got, test.settlesAfter)
			}
			if got := CanRemainActiveAfterPostflightFailure(test.action); got != test.canRemainActive {
				t.Errorf("CanRemainActiveAfterPostflightFailure(%q) = %t, want %t", test.action, got, test.canRemainActive)
			}
		})
	}
}
