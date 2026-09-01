package metrics

import "testing"

func TestMetricDescriptors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		scope      Scope
		name       string
		absolute   bool
		percentage bool
	}{
		{ScopeService, MetricMemory, true, true},
		{ScopeService, MetricCPU, false, true},
		{ScopeService, MetricIORead, true, false},
		{ScopeSystem, MetricTotalCPU, false, true},
		{ScopeSystem, MetricLoad1, true, false},
	}
	for _, test := range tests {
		t.Run(string(test.scope)+"/"+test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := LookupDescriptor(string(test.scope), test.name)
			if !ok {
				t.Fatal("descriptor not found")
			}
			if got.Absolute != test.absolute || got.Percentage != test.percentage {
				t.Fatalf("descriptor = %+v", got)
			}
		})
	}
	if _, ok := LookupDescriptor(string(ScopeSystem), MetricMemory); ok {
		t.Fatal("service metric found in system scope")
	}
	if ValidScope("host") {
		t.Fatal("unknown scope accepted")
	}
}
