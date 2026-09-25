package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// CLI mutations use the watch route's HEAD response to bind their generation.
func TestWatchHeadCarriesBackendGeneration(t *testing.T) {
	rec := httptest.NewRecorder()
	newServer(&generationBackend{generation: 7}).ServeHTTP(rec, httptest.NewRequest(http.MethodHead, APIPathWatches, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get(HeaderGeneration); got != "7" {
		t.Fatalf("HEAD generation = %q, want 7", got)
	}
}
