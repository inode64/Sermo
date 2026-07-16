package conn

import (
	"context"
	"testing"
)

func TestMongoRole(t *testing.T) {
	cases := []struct {
		primary, secondary, arbiter bool
		setName, want               string
	}{
		{false, false, false, "", "standalone"},
		{true, false, false, "", "standalone"}, // no set name -> standalone even if writable
		{true, false, false, "rs0", "primary"},
		{false, true, false, "rs0", "secondary"},
		{false, false, true, "rs0", "arbiter"},
		{false, false, false, "rs0", "unknown"},
	}
	for _, c := range cases {
		if got := mongoRole(c.primary, c.secondary, c.arbiter, c.setName); got != c.want {
			t.Errorf("mongoRole(%v,%v,%v,%q) = %q, want %q", c.primary, c.secondary, c.arbiter, c.setName, got, c.want)
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
	MongoDisconnect(context.Background(), client)
}

func TestMongoProbeUnreachable(t *testing.T) {
	assertProbeRefused(t, mongodbProtocol{}, deadPort(t))
}
