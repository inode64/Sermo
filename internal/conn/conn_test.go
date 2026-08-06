package conn

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type fakeProto struct{ name string }

func (f fakeProto) Name() string                                { return f.name }
func (f fakeProto) DefaultPort() int                            { return 1234 }
func (fakeProto) RequiresUser() bool                            { return true }
func (fakeProto) Probe(context.Context, Config) (Result, error) { return Result{}, nil }

type recordingProto struct {
	fakeProto
	config        Config
	targetAddress string
	commonContext bool
}

func (p *recordingProto) Probe(ctx context.Context, cfg Config) (Result, error) {
	p.config = cfg
	p.targetAddress = probeTargetFor(ctx, cfg, p.DefaultPort()).address()
	_, p.commonContext = ctx.Value(probeContextKey{}).(probeState)
	return Result{}, nil
}

func TestRegistryLookupAndAlias(t *testing.T) {
	reg, err := newRegistry([]protocolRegistration{{protocol: fakeProto{name: "demo"}, aliases: []string{"demo-alias"}}})
	if err != nil {
		t.Fatal(err)
	}

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

func TestRegisteredProtocolUsesCommonExecutor(t *testing.T) {
	implementation := &recordingProto{fakeProto: fakeProto{name: "demo"}}
	reg, err := newRegistry([]protocolRegistration{{
		protocol:      implementation,
		aliases:       []string{"demo-alias"},
		defaultSocket: "/run/demo.sock",
	}})
	if err != nil {
		t.Fatal(err)
	}

	protocol, ok := reg.lookup("demo-alias")
	if !ok {
		t.Fatal("lookup demo-alias failed")
	}
	if _, err := protocol.Probe(context.Background(), Config{}); err != nil {
		t.Fatal(err)
	}

	if !implementation.commonContext {
		t.Fatal("registered probe did not receive the common probe context")
	}
	wantConfig := Config{Host: DefaultHost, Port: implementation.DefaultPort(), Socket: "/run/demo.sock"}
	if implementation.config.Host != wantConfig.Host ||
		implementation.config.Port != wantConfig.Port ||
		implementation.config.Socket != wantConfig.Socket {
		t.Errorf("probe config = %+v, want %+v", implementation.config, wantConfig)
	}
	if implementation.targetAddress != "127.0.0.1:1234" {
		t.Errorf("prepared target address = %q, want %q", implementation.targetAddress, "127.0.0.1:1234")
	}
}

func TestRegistryRejectsInvalidRegistrations(t *testing.T) {
	tests := []struct {
		name          string
		registrations []protocolRegistration
	}{
		{name: "nil protocol", registrations: []protocolRegistration{{}}},
		{name: "empty canonical name", registrations: []protocolRegistration{{protocol: fakeProto{}}}},
		{name: "empty alias", registrations: []protocolRegistration{{protocol: fakeProto{name: "one"}, aliases: []string{""}}}},
		{name: "duplicate canonical name", registrations: []protocolRegistration{{protocol: fakeProto{name: "one"}}, {protocol: fakeProto{name: "one"}}}},
		{name: "alias collision", registrations: []protocolRegistration{{protocol: fakeProto{name: "one"}, aliases: []string{"shared"}}, {protocol: fakeProto{name: "two"}, aliases: []string{"shared"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := newRegistry(test.registrations); err == nil {
				t.Fatal("newRegistry() error = nil")
			}
		})
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

	lineRE := regexp.MustCompile("^- `([^`]+)`(?: \\((?:alias|aliases) ([^)]*)\\))?")
	aliasRE := regexp.MustCompile("`([^`]+)`")
	out := map[string][]string{}
	for line := range strings.SplitSeq(text, "\n") {
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

// TestRefIDLabelFallbacksAreEquivalent pins the regression the shared
// refIDLabel extraction introduced: ntpRefID used to return the raw trimmed
// bytes for a stratum-1 server, and briefly returned "" for a non-printable
// identifier, which drops reference_id from the result entirely.
func TestRefIDLabelFallbacksAreEquivalent(t *testing.T) {
	cases := []struct {
		name    string
		id      uint32
		stratum int
		wantNTP string
	}{
		{"printable refclock", 0x47505300, 1, "GPS"},
		{"non-printable stratum 1 falls back", 0x00010203, 1, "0.1.2.3"},
		{"all-zero stratum 1 falls back", 0x00000000, 1, "0.0.0.0"},
		{"stratum 2 is the upstream address", 0x0a000007, 2, "10.0.0.7"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ntpRefID(tc.id, tc.stratum); got != tc.wantNTP {
				t.Fatalf("ntpRefID(%#08x, %d) = %q, want %q", tc.id, tc.stratum, got, tc.wantNTP)
			}
			if ntpRefID(tc.id, tc.stratum) == "" {
				t.Fatal("reference_id must never be empty; an empty value is dropped from the result")
			}
			if chronyRefID(tc.id, tc.stratum) == "" {
				t.Fatal("chrony reference_id must never be empty either")
			}
		})
	}
}

// TestSecondsStringPreservesNanoseconds pins the precision widening: the ntp
// probe used to render offset_seconds at 6 decimals, truncating a
// nanosecond-resolution time.Duration to whole microseconds.
func TestSecondsStringPreservesNanoseconds(t *testing.T) {
	cases := []float64{0, 1e-9, -1e-9, 0.000000123, -0.000044694, 1.5, -1234.5}
	for _, want := range cases {
		got, err := strconv.ParseFloat(secondsString(want), 64)
		if err != nil {
			t.Fatalf("secondsString(%v) = %q, unparseable: %v", want, secondsString(want), err)
		}
		if got != want {
			t.Errorf("secondsString(%v) round-trips to %v", want, got)
		}
	}
}

// TestSynchronizedHandlesAbsentLeap covers the clock check's call shape: a
// sample whose probe published no leap field yields "" here.
func TestSynchronizedHandlesAbsentLeap(t *testing.T) {
	if !Synchronized(3, "") {
		t.Error("a synchronized stratum with no leap field must not be rejected")
	}
	if Synchronized(0, "") {
		t.Error("stratum 0 must be rejected whatever the leap field says")
	}
	if Synchronized(3, leapNameUnsynchronized) {
		t.Error("an unsynchronized leap must be rejected at any stratum")
	}
	// leapName never returns a value Synchronized would misread as healthy.
	for code := -1; code <= 4; code++ {
		name := leapName(code)
		if (code == 3) != (name == leapNameUnsynchronized) {
			t.Errorf("leapName(%d) = %q; only code 3 is the unsynchronized value", code, name)
		}
	}
	// The rule the chrony probe reports as `synchronized` and the clock check
	// rejects a sample by — one predicate so the two cannot diverge.
	codes := []struct {
		stratum int
		leap    int
		want    bool
	}{
		{3, 0, true},
		{1, 0, true},
		{3, 1, true},  // a pending leap second is still synchronized
		{0, 3, false}, // chronyd's unsynchronized state
		{0, 0, false},
		{3, 3, false},
	}
	for _, tc := range codes {
		if got := Synchronized(tc.stratum, leapName(tc.leap)); got != tc.want {
			t.Errorf("Synchronized(%d, %s) = %v, want %v", tc.stratum, leapName(tc.leap), got, tc.want)
		}
	}
}
