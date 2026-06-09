package conn

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestMongoRegistered(t *testing.T) {
	for _, name := range []string{"mongodb", "mongo"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 27017 {
			t.Fatalf("%s default port = %d, want 27017", name, p.DefaultPort())
		}
		if p.RequiresUser() {
			t.Fatalf("%s must not require a user", name)
		}
	}
}

func TestMongoConnectBuilds(t *testing.T) {
	// Connection is lazy: building a client with credentials and TLS must not error.
	client, err := MongoConnect(Config{
		Host: "127.0.0.1", Port: 27017, User: "u", Password: "p",
		Database: "app", TLS: "skip-verify", Params: map[string]string{"auth_source": "admin"},
	})
	if err != nil {
		t.Fatalf("MongoConnect: %v", err)
	}
	_ = client.Disconnect(context.Background())
}

func TestMongoProbeUnreachable(t *testing.T) {
	// Grab a port then close it so the address is guaranteed refused.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, portStr, _ := net.SplitHostPort(ln.Addr().String())
	_ = ln.Close()
	port, _ := strconv.Atoi(portStr)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := (mongodbProtocol{}).Probe(ctx, Config{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("probing an unreachable MongoDB must error")
	}
}
