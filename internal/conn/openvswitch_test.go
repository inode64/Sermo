package conn

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// serveOVSDB answers OVSDB JSON-RPC requests on c: list_dbs returns dbs, and
// transact returns a single row carrying ovs_version (when non-empty).
func serveOVSDB(c net.Conn, dbs []string, version string) {
	var encodedVersion any = []any{"set", []any{}}
	if version != "" {
		encodedVersion = version
	}
	rows := []any{map[string]any{"ovs_version": encodedVersion}}
	serveOVSDBResult(c, dbs, []any{map[string]any{"rows": rows}})
}

func serveOVSDBResult(c net.Conn, dbs []string, transactResult any) {
	defer func() { _ = c.Close() }()
	dec := json.NewDecoder(c)
	enc := json.NewEncoder(c)
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := dec.Decode(&req); err != nil {
			return
		}
		switch req.Method {
		case "list_dbs":
			if err := enc.Encode(map[string]any{"id": req.ID, "result": dbs, "error": nil}); err != nil {
				return
			}
		case "transact":
			if err := enc.Encode(map[string]any{"id": req.ID, "result": transactResult, "error": nil}); err != nil {
				return
			}
		default:
			return
		}
	}
}

func TestOpenvswitchProbeTCP(t *testing.T) {
	port := serveOnce(t, func(c net.Conn) {
		serveOVSDB(c, []string{"Open_vSwitch", "_Server"}, "3.1.0")
	})
	res, err := openvswitchProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "3.1.0" {
		t.Fatalf("version = %q, want 3.1.0", res.Version)
	}
	if res.Extra["databases"] != "Open_vSwitch,_Server" {
		t.Fatalf("databases = %q", res.Extra["databases"])
	}
}

func TestOpenvswitchProbeSocket(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "db.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		serveOVSDB(c, []string{"Open_vSwitch", "_Server"}, "")
	}()

	res, err := openvswitchProtocol{}.Probe(context.Background(), Config{Socket: sock})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if res.Version != "" {
		t.Fatalf("version = %q, want empty optional value", res.Version)
	}
	if res.Extra["databases"] != "Open_vSwitch,_Server" {
		t.Fatalf("databases = %q", res.Extra["databases"])
	}
}

func TestOpenvswitchProbeRejectsInvalidDatabase(t *testing.T) {
	tests := []struct {
		name    string
		dbs     []string
		version string
		result  any
		wantErr string
	}{
		{
			name:    "Open_vSwitch database absent",
			dbs:     []string{"_Server"},
			wantErr: "does not serve Open_vSwitch",
		},
		{
			name:    "Open_vSwitch row unreadable",
			dbs:     []string{"Open_vSwitch", "_Server"},
			result:  []any{map[string]any{"rows": []any{}}},
			wantErr: "root row",
		},
		{
			name: "transaction operation rejected",
			dbs:  []string{"Open_vSwitch", "_Server"},
			result: []any{map[string]any{
				"error":   "unknown column",
				"details": "ovs_version is unavailable",
			}},
			wantErr: "transact operation failed: unknown column",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := serveOnce(t, func(c net.Conn) {
				if tt.result != nil {
					serveOVSDBResult(c, tt.dbs, tt.result)
					return
				}
				serveOVSDB(c, tt.dbs, tt.version)
			})
			_, err := openvswitchProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
			if err == nil {
				t.Fatal("probe must reject an invalid Open_vSwitch database")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("probe error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestOpenvswitchProbeNotOVSDB(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_, _ = c.Write([]byte("garbage not json\n"))
	}()

	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	port, _ := strconv.Atoi(portStr)
	_, err = openvswitchProtocol{}.Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	if err == nil {
		t.Fatal("a non-OVSDB server must error")
	}
}
