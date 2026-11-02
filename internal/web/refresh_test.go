package web

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func TestRefreshRoutesReachTheirBackendMethod(t *testing.T) {
	b := &fakeBackend{}
	h := newServer(b)
	for _, path := range []string{
		RefreshPath(APIPathApplications),
		RefreshPath(APIPathLibraries),
		RefreshPath(APIPathWatches),
		RefreshPath(APIPathWatches, "load", "svc:queue"),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postReq(path))
		if rec.Code != http.StatusOK {
			t.Fatalf("POST %s = %d body=%s", path, rec.Code, rec.Body.String())
		}
	}
	want := []string{"applications", "libraries", "watches:", "watches:load,svc:queue"}
	if !slices.Equal(b.refreshed, want) {
		t.Fatalf("refreshed = %q, want %q", b.refreshed, want)
	}
}

// A refreshed watch cycle can run that watch's actions, so the route shares
// the mutation gates: CSRF and the backend generation.
func TestRefreshRoutesAreMutations(t *testing.T) {
	path := RefreshPath(APIPathWatches)

	b := &pinnedMutationBackend{generation: 7}
	rec := httptest.NewRecorder()
	newServer(b).ServeHTTP(rec, postReq(path))
	if rec.Code != http.StatusPreconditionRequired || len(b.refreshed) != 0 {
		t.Fatalf("without generation: %d, refreshed %q", rec.Code, b.refreshed)
	}

	plain := &fakeBackend{}
	rec = httptest.NewRecorder()
	newServer(plain).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
	if rec.Code != http.StatusForbidden || len(plain.refreshed) != 0 {
		t.Fatalf("without CSRF: %d, refreshed %q", rec.Code, plain.refreshed)
	}
}
