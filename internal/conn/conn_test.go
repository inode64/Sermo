package conn

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
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

func TestDocsRulesProtocolListMatchesRegistry(t *testing.T) {
	documented := documentedProtocolsFromRules(t)
	registered := registeredProtocolsForDocs()

	for _, name := range slices.Sorted(maps.Keys(registered)) {
		wantAliases := registered[name]
		gotAliases, ok := documented[name]
		if !ok {
			t.Errorf("docs/rules.md missing protocol %q with aliases %v", name, wantAliases)
			continue
		}
		if !slices.Equal(gotAliases, wantAliases) {
			t.Errorf("docs/rules.md protocol %q aliases = %v, want %v", name, gotAliases, wantAliases)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(documented)) {
		if _, ok := registered[name]; !ok {
			t.Errorf("docs/rules.md documents unknown protocol %q with aliases %v", name, documented[name])
		}
	}
}

func registeredProtocolsForDocs() map[string][]string {
	defaultRegistry.mu.RLock()
	defer defaultRegistry.mu.RUnlock()

	out := map[string][]string{}
	for name, proto := range defaultRegistry.byName {
		canonical := proto.Name()
		if _, ok := out[canonical]; !ok {
			out[canonical] = nil
		}
		if name != canonical {
			out[canonical] = append(out[canonical], name)
		}
	}
	for name := range out {
		slices.Sort(out[name])
	}
	return out
}

func documentedProtocolsFromRules(t *testing.T) map[string][]string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "rules.md"))
	if err != nil {
		t.Fatalf("read docs/rules.md: %v", err)
	}
	text := string(raw)

	const startMarker = "Protocols, in the order of the table above:"
	start := strings.Index(text, startMarker)
	if start < 0 {
		t.Fatalf("docs/rules.md missing protocol-list marker %q", startMarker)
	}
	text = text[start+len(startMarker):]

	const endMarker = "\n### SQLite integrity"
	end := strings.Index(text, endMarker)
	if end < 0 {
		t.Fatalf("docs/rules.md missing protocol-list end marker %q", strings.TrimSpace(endMarker))
	}
	text = text[:end]

	lineRE := regexp.MustCompile("^[-] `([^`]+)`(?: \\((?:alias|aliases) ([^)]*)\\))?")
	aliasRE := regexp.MustCompile("`([^`]+)`")
	out := map[string][]string{}
	for _, line := range strings.Split(text, "\n") {
		match := lineRE.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		name := match[1]
		if _, exists := out[name]; exists {
			t.Fatalf("docs/rules.md documents protocol %q more than once", name)
		}
		out[name] = nil
		for _, alias := range aliasRE.FindAllStringSubmatch(match[2], -1) {
			out[name] = append(out[name], alias[1])
		}
		slices.Sort(out[name])
	}
	if len(out) == 0 {
		t.Fatal("docs/rules.md protocol list parsed no protocols")
	}
	return out
}
