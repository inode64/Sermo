package checks

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHTTPHostHeaderSelectsVirtualHost(t *testing.T) {
	for _, header := range []string{"Host", "host", "HOST"} {
		t.Run(header, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Host != "app.example.com" {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				w.WriteHeader(http.StatusOK)
			}))
			defer srv.Close()
			result := runHTTP(t, srv, map[string]any{
				"url": srv.URL, "headers": map[string]any{header: "app.example.com"},
			})
			if !result.OK {
				t.Fatalf("virtual host check = %+v", result)
			}
		})
	}
}
