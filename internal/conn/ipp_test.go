package conn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestIPPRegistered(t *testing.T) {
	for _, name := range []string{"ipp", "cups"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 631 {
			t.Fatalf("%s default port = %d, want 631", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func TestBuildIPPRequest(t *testing.T) {
	req := buildIPPRequest(0x4001, 1)
	// version 2.0, operation-id 0x4001, request-id 1, operation-attributes-tag 0x01.
	if req[0] != 0x02 || req[1] != 0x00 {
		t.Fatalf("version bytes = % x", req[:2])
	}
	if req[2] != 0x40 || req[3] != 0x01 {
		t.Fatalf("operation-id = % x, want 4001", req[2:4])
	}
	if req[8] != 0x01 {
		t.Fatalf("operation-attributes-tag = %#x, want 0x01", req[8])
	}
	if req[len(req)-1] != 0x03 {
		t.Fatalf("must end with end-of-attributes-tag 0x03, got %#x", req[len(req)-1])
	}
	if !strings.Contains(string(req), "attributes-charset") || !strings.Contains(string(req), "utf-8") {
		t.Fatalf("required attributes missing: %q", req)
	}
}

func TestParseIPPResponse(t *testing.T) {
	// version 2.0, status 0x0000 (successful-ok), request-id 1.
	ver, status, err := parseIPPResponse([]byte{0x02, 0x00, 0x00, 0x00, 0, 0, 0, 1, 0x03})
	if err != nil || ver != "2.0" || status != 0x0000 {
		t.Fatalf("ver=%q status=%#x err=%v", ver, status, err)
	}
	if _, _, err := parseIPPResponse([]byte{0x02, 0x00}); err == nil {
		t.Fatal("a short IPP response must error")
	}
}

func TestIPPProbeAgainstFakeServer(t *testing.T) {
	var gotIPP bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") == "application/ipp" {
			gotIPP = true
		}
		w.Header().Set("Content-Type", "application/ipp")
		// version 2.0, status successful-ok, request-id 1, end-of-attributes.
		_, _ = w.Write([]byte{0x02, 0x00, 0x00, 0x00, 0, 0, 0, 1, 0x03})
	}))
	defer srv.Close()

	u, _ := url.Parse(srv.URL)
	port, _ := strconv.Atoi(u.Port())
	res, err := ippProtocol{}.Probe(context.Background(), Config{Host: u.Hostname(), Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !gotIPP {
		t.Fatal("the probe did not POST an application/ipp request")
	}
	if res.Extra["ipp_status"] != "successful-ok" {
		t.Fatalf("status = %q", res.Extra["ipp_status"])
	}
}
