package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

type smbTestBackend struct {
	fakeBackend
	service, check, identity string
}

func (b *smbTestBackend) CloseSMBSession(_ context.Context, service, check, identity string) ActionResult {
	b.service, b.check, b.identity = service, check, identity
	return ActionResult{OK: true}
}

func TestSMBSessionCloseEndpoint(t *testing.T) {
	b := &smbTestBackend{}
	h := newServer(b)
	path := testServicePath("smb", apiSegmentSMBSessions, "sessions", apiActionClose)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(path))
	if rec.Code != http.StatusBadRequest || b.identity != "" {
		t.Fatalf("missing identity status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, postReq(testPathQuery(path, testQueryParam(APIQueryIdentity, "generation:1234"))))
	if rec.Code != http.StatusOK || b.service != "smb" || b.check != "sessions" || b.identity != "generation:1234" {
		t.Fatalf("response=%d target=%+v", rec.Code, b)
	}
}
