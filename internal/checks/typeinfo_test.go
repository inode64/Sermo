package checks

import "testing"

func TestTypeInfoInventoryQueries(t *testing.T) {
	for _, typ := range []string{CheckTypeNet, CheckTypeICMP, CheckTypeSwap} {
		if !IsMultiMetricType(typ) {
			t.Errorf("%s must be multi-metric", typ)
		}
	}
	if IsMultiMetricType(CheckTypeStorage) || IsMultiMetricType("unknown") {
		t.Error("storage and unknown types are not multi-metric")
	}
	meters := map[string]string{CheckTypeFDS: DataKeyAllocated, CheckTypePIDs: DataKeyCount, CheckTypeConntrack: DataKeyCount}
	for typ, want := range meters {
		if key, ok := MeterCountKey(typ); !ok || key != want {
			t.Errorf("MeterCountKey(%s) = %q/%v, want %q", typ, key, ok, want)
		}
	}
	if _, ok := MeterCountKey(CheckTypeMemory); ok {
		t.Error("memory draws its gauge from bytes, not a count key")
	}
	for _, typ := range []string{CheckTypeBinary, CheckTypeFile, CheckTypeSocket, CheckTypePidfile, CheckTypeLockfile} {
		if !IsResourcePathType(typ) {
			t.Errorf("%s must be a resource-path preflight type", typ)
		}
	}
	if IsResourcePathType(CheckTypeCommand) {
		t.Error("command is not a resource-path type")
	}
}
