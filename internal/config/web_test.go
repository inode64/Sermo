package config

import (
	"errors"
	"testing"
)

func TestWebBind(t *testing.T) {
	tests := []struct {
		name     string
		web      any
		want     WebBind
		wantAddr string
		wantErr  string
		wantIs   error
	}{
		{name: "no web section", wantErr: "no [web] section in config", wantIs: ErrWebNotConfigured},
		{name: "port missing", web: map[string]any{}, wantErr: "web.port is not set", wantIs: ErrWebPortUnset},
		{name: "default address", web: map[string]any{WebKeyPort: 9797}, want: WebBind{Host: "127.0.0.1", Port: 9797}, wantAddr: "127.0.0.1:9797"},
		{name: "empty address uses default", web: map[string]any{WebKeyAddress: "", WebKeyPort: 9797}, want: WebBind{Host: "127.0.0.1", Port: 9797}, wantAddr: "127.0.0.1:9797"},
		{name: "IPv4 wildcard", web: map[string]any{WebKeyAddress: "0.0.0.0", WebKeyPort: 9797}, want: WebBind{Host: "0.0.0.0", Port: 9797}, wantAddr: "0.0.0.0:9797"},
		{name: "IPv6 loopback", web: map[string]any{WebKeyAddress: "::1", WebKeyPort: 9797}, want: WebBind{Host: "::1", Port: 9797}, wantAddr: "[::1]:9797"},
		{name: "quoted port accepted", web: map[string]any{WebKeyPort: "8080"}, want: WebBind{Host: "127.0.0.1", Port: 8080}, wantAddr: "127.0.0.1:8080"},
		{name: "port zero", web: map[string]any{WebKeyPort: 0}, wantErr: "web.port must be in 1..65535 (got 0)"},
		{name: "port above range", web: map[string]any{WebKeyPort: 65536}, wantErr: "web.port must be in 1..65535 (got 65536)"},
		{name: "port not a number", web: map[string]any{WebKeyPort: "abc"}, wantErr: "web.port is not a number (string)"},
		{name: "address not a string", web: map[string]any{WebKeyAddress: 7, WebKeyPort: 9797}, wantErr: "web.address must be a string (got int)"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw := map[string]any{}
			if tc.web != nil {
				raw[SectionWeb] = tc.web
			}
			got, err := (Global{Raw: raw}).WebBind()
			if tc.wantErr != "" {
				if err == nil || err.Error() != tc.wantErr {
					t.Fatalf("WebBind() error = %v, want %q", err, tc.wantErr)
				}
				if tc.wantIs != nil && !errors.Is(err, tc.wantIs) {
					t.Errorf("errors.Is(%v, %v) = false", err, tc.wantIs)
				}
				return
			}
			if err != nil {
				t.Fatalf("WebBind() error = %v", err)
			}
			if got != tc.want {
				t.Errorf("WebBind() = %#v, want %#v", got, tc.want)
			}
			if addr := got.HostPort(); addr != tc.wantAddr {
				t.Errorf("HostPort() = %q, want %q", addr, tc.wantAddr)
			}
		})
	}
}
