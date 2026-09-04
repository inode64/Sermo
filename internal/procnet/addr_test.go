package procnet

import "testing"

func TestParseIPv4Host(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "0100007F", want: "127.0.0.1", ok: true},
		{in: "00000000", want: "0.0.0.0", ok: true},
		{in: "160200C0", want: "192.0.2.22", ok: true},
		{in: "100007F", ok: false},
		{in: "GG00007F", ok: false},
		{in: "", ok: false},
	}
	for _, tt := range tests {
		got, ok := ParseIPv4Host(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("ParseIPv4Host(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestParseIPv6Host(t *testing.T) {
	got, ok := ParseIPv6Host("00000000000000000000000001000000")
	if !ok || got != "::1" {
		t.Fatalf("ParseIPv6Host(loopback) = %q, %v; want ::1, true", got, ok)
	}
	if _, ok := ParseIPv6Host("0000000000000000000000000100000"); ok {
		t.Fatal("short IPv6 hex must not parse")
	}
}

func TestParseHost(t *testing.T) {
	got, ok := ParseHost("0100007F", false)
	if !ok || got != "127.0.0.1" {
		t.Fatalf("ParseHost IPv4 = %q, %v", got, ok)
	}
	got, ok = ParseHost("00000000000000000000000001000000", true)
	if !ok || got != "::1" {
		t.Fatalf("ParseHost IPv6 = %q, %v", got, ok)
	}
}

func TestParseIPv4Socket(t *testing.T) {
	tests := []struct {
		in       string
		wantHost string
		wantPort int
		wantErr  bool
	}{
		{in: "00000000:0044", wantHost: "0.0.0.0", wantPort: 68},
		{in: "0100007F:0035", wantHost: "127.0.0.1", wantPort: 53},
		{in: "0100007F", wantErr: true},
		{in: "GG00007F:0035", wantErr: true},
		{in: "0100007F:GGGG", wantErr: true},
	}
	for _, tt := range tests {
		host, port, err := ParseIPv4Socket(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseIPv4Socket(%q): expected error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseIPv4Socket(%q): %v", tt.in, err)
			continue
		}
		if host != tt.wantHost || port != tt.wantPort {
			t.Errorf("ParseIPv4Socket(%q) = %s:%d, want %s:%d", tt.in, host, port, tt.wantHost, tt.wantPort)
		}
	}
}
