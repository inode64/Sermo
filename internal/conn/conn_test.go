package conn

import (
	"context"
	"testing"
)

type fakeProto struct{ name string }

func (f fakeProto) Name() string                                { return f.name }
func (f fakeProto) DefaultPort() int                            { return 1234 }
func (fakeProto) RequiresUser() bool                            { return true }
func (fakeProto) Probe(context.Context, Config) (Result, error) { return Result{}, nil }

func TestRegistryLookupAndAlias(t *testing.T) {
	reg := newRegistry()
	reg.register(fakeProto{name: "demo"}, "demo-alias")

	if p, ok := reg.lookup("demo"); !ok || p.Name() != "demo" {
		t.Fatalf("lookup demo = %v/%v", p, ok)
	}
	if p, ok := reg.lookup("demo-alias"); !ok || p.Name() != "demo" {
		t.Fatalf("alias must resolve to the canonical protocol, got %v/%v", p, ok)
	}
	if _, ok := reg.lookup("nope"); ok {
		t.Fatal("unknown name must not resolve")
	}
}

func TestMySQLRegistered(t *testing.T) {
	// The package's init registers mysql with a mariadb alias.
	for _, name := range []string{"mysql", "mariadb"} {
		p, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s not registered", name)
		}
		if p.DefaultPort() != 3306 {
			t.Fatalf("%s default port = %d, want 3306", name, p.DefaultPort())
		}
	}
}
