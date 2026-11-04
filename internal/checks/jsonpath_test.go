package checks

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestParseJSONPath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		want    []string
		wantErr string
	}{
		{name: "single key", path: "status", want: []string{"status"}},
		{name: "dotted keys", path: "data.replicas", want: []string{"data", "replicas"}},
		{name: "quoted key with dots", path: `counters["MAIN.backend_fail"].value`, want: []string{"counters", "MAIN.backend_fail", "value"}},
		{name: "leading quoted key", path: `["MGT.child_panic"].value`, want: []string{"MGT.child_panic", "value"}},
		{name: "consecutive quoted keys", path: `a["b.c"]["d"]`, want: []string{"a", "b.c", "d"}},
		{name: "escaped quote in key", path: `a["say \"hi\""]`, want: []string{"a", `say "hi"`}},
		{name: "empty quoted key", path: `a[""]`, want: []string{"a", ""}},
		{name: "empty path", path: "", wantErr: "empty"},
		{name: "double dot", path: "a..b", wantErr: "empty key"},
		{name: "leading dot", path: ".a", wantErr: "empty key"},
		{name: "trailing dot", path: "a.", wantErr: "empty key"},
		{name: "dot before bracket", path: `a.["b"]`, wantErr: "empty key"},
		{name: "unterminated bracket", path: "a[", wantErr: "double-quoted key"},
		{name: "unterminated quoted key", path: `a["x`, wantErr: "unterminated"},
		{name: "unclosed bracket", path: `a["x"`, wantErr: "closed by ]"},
		{name: "unquoted bracket key", path: "a[x]", wantErr: "double-quoted key"},
		{name: "array index", path: "a[0]", wantErr: "double-quoted key"},
		{name: "key glued after bracket", path: `a["x"]b`, wantErr: "followed by . or ["},
		{name: "stray closing bracket", path: "a]b", wantErr: "without a matching ["},
		{name: "invalid escape", path: `a["\q"]`, wantErr: "invalid quoted key"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseJSONPath(tc.path)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ParseJSONPath(%q) = %q, %v; want error containing %q", tc.path, got, err, tc.wantErr)
				}
				return
			}
			if err != nil || !slices.Equal(got, tc.want) {
				t.Fatalf("ParseJSONPath(%q) = %q, %v; want %q", tc.path, got, err, tc.want)
			}
		})
	}
}

func TestJSONLookup(t *testing.T) {
	var doc any
	if err := json.Unmarshal([]byte(`{"status":"ok","data":{"n":3},"counters":{"MAIN.backend_fail":{"value":7}},"list":[1]}`), &doc); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		path    string
		want    string
		missing bool
	}{
		{path: "status", want: "ok"},
		{path: "data.n", want: "3"},
		{path: `counters["MAIN.backend_fail"].value`, want: "7"},
		{path: "counters.MAIN.backend_fail.value", missing: true},
		{path: "data.n.deeper", missing: true},
		{path: "list.x", missing: true},
		{path: "absent", missing: true},
	}
	for _, tc := range tests {
		keys, err := ParseJSONPath(tc.path)
		if err != nil {
			t.Fatalf("ParseJSONPath(%q): %v", tc.path, err)
		}
		got, ok := jsonLookup(doc, keys)
		if ok == tc.missing || (ok && jsonValueString(got) != tc.want) {
			t.Errorf("jsonLookup(%q) = %v, %v; want %q (missing %v)", tc.path, got, ok, tc.want, tc.missing)
		}
	}
	// Summary templates keep the plain dotted form.
	if got, ok := jsonPath(doc, "data.n"); !ok || jsonValueString(got) != "3" {
		t.Errorf(`jsonPath("data.n") = %v, %v`, got, ok)
	}
}

func TestParseJSONAssertionsRejectsABadPath(t *testing.T) {
	if _, warn := parseJSONAssertions(map[string]any{"a..b": 1}); !strings.Contains(warn, `expect_json: JSON path "a..b" has an empty key`) {
		t.Fatalf("warn = %q", warn)
	}
	assertions, warn := parseJSONAssertions(map[string]any{`c["x.y"]`: map[string]any{"op": ">", "value": 1}})
	if warn != "" || len(assertions) != 1 || !slices.Equal(assertions[0].keys, []string{"c", "x.y"}) || assertions[0].op != ">" {
		t.Fatalf("assertions = %+v, warn %q", assertions, warn)
	}
}
