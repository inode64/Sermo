package conn

import (
	"context"
	"net"
	"path/filepath"
	"testing"
)

type unixSocketProbe func(context.Context, Config) (Result, error)

func assertUnixSocketProbe(t *testing.T, socketName string, probe unixSocketProbe) {
	t.Helper()
	socket := filepath.Join(t.TempDir(), socketName)
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	go acceptUnixSocketConnections(listener)

	result, err := probe(context.Background(), Config{Socket: socket})
	if err != nil {
		t.Fatalf("probe live socket: %v", err)
	}
	if result.Extra["socket"] != socket {
		t.Fatalf("extra = %v", result.Extra)
	}
	if _, err := probe(context.Background(), Config{Socket: filepath.Join(t.TempDir(), "absent.socket")}); err == nil {
		t.Fatal("a missing socket must fail the probe")
	}
}

func acceptUnixSocketConnections(listener net.Listener) {
	for {
		connection, err := listener.Accept()
		if err != nil {
			return
		}
		_ = connection.Close()
	}
}
