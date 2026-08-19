package conn

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"

	"github.com/OpenPrinting/goipp"
)

func TestBuildIPPRequest(t *testing.T) {
	payload, err := buildIPPRequest(goipp.OpCupsGetDefault, ippRequestIDDefault)
	if err != nil {
		t.Fatalf("buildIPPRequest(): %v", err)
	}
	var request goipp.Message
	if err := request.DecodeBytes(payload); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if request.Version != goipp.DefaultVersion {
		t.Fatalf("version = %s, want %s", request.Version, goipp.DefaultVersion)
	}
	if request.Code != goipp.Code(goipp.OpCupsGetDefault) {
		t.Fatalf("operation = %#x, want %#x", request.Code, goipp.OpCupsGetDefault)
	}
	if request.RequestID != ippRequestIDDefault {
		t.Fatalf("request ID = %d, want %d", request.RequestID, ippRequestIDDefault)
	}
	wantAttributes := goipp.Attributes{
		goipp.MakeAttr(ippAttrCharset, goipp.TagCharset, goipp.String(ippCharsetUTF8)),
		goipp.MakeAttr(ippAttrNaturalLanguage, goipp.TagLanguage, goipp.String(ippLanguageEN)),
	}
	if !request.Operation.Equal(wantAttributes) {
		t.Fatalf("operation attributes = %v, want %v", request.Operation, wantAttributes)
	}
}

func TestParseIPPResponse(t *testing.T) {
	tests := []struct {
		name        string
		response    []byte
		wantVersion string
		wantStatus  uint16
		wantErr     bool
	}{
		{
			name:        "valid empty response",
			response:    []byte{0x02, 0x00, 0x00, 0x00, 0, 0, 0, 1, 0x03},
			wantVersion: "2.0",
		},
		{
			name:        "valid error response",
			response:    []byte{0x01, 0x01, 0x04, 0x01, 0, 0, 0, 1, 0x03},
			wantVersion: "1.1",
			wantStatus:  ippStatusClientUnauthorized,
		},
		{name: "short header", response: []byte{0x02, 0x00}, wantErr: true},
		{name: "missing end delimiter", response: []byte{0x02, 0x00, 0x00, 0x00, 0, 0, 0, 1}, wantErr: true},
		{
			name:     "truncated attribute",
			response: []byte{0x02, 0x00, 0x00, 0x00, 0, 0, 0, 1, 0x01, 0x47, 0, 4, 'n'},
			wantErr:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			version, status, err := parseIPPResponse(test.response)
			if test.wantErr {
				if err == nil {
					t.Fatalf("parseIPPResponse() = version %q, status %#x; want error", version, status)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseIPPResponse(): %v", err)
			}
			if version != test.wantVersion || status != test.wantStatus {
				t.Fatalf("parseIPPResponse() = version %q, status %#x; want %q, %#x", version, status, test.wantVersion, test.wantStatus)
			}
		})
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
	if res.Version != "IPP/2.0" || res.Extra["ipp_version"] != "2.0" {
		t.Fatalf("version = %q, extra version = %q", res.Version, res.Extra["ipp_version"])
	}
}
