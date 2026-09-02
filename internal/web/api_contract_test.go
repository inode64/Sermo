package web

import "testing"

func TestAPIContractPaths(t *testing.T) {
	paths := map[string]string{
		"applications": APIPathApplications,
		"events":       APIPathEvents,
		"events clear": APIPathEventsClear,
		"services":     APIPathServices,
		"watches":      APIPathWatches,
	}
	for name, path := range paths {
		if len(path) <= len(APIPathRoot) || path[:len(APIPathRoot)] != APIPathRoot {
			t.Errorf("%s path %q is not below API root %q", name, path, APIPathRoot)
		}
	}
	if got, want := APIPathEventsClear, APIPathEvents+"/clear"; got != want {
		t.Errorf("events clear path = %q, want %q", got, want)
	}
}
