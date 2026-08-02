package rules

import (
	"testing"

	"github.com/goccy/go-yaml"
)

// FuzzParseRules ensures untrusted rules trees only yield rules/warnings or
// skip malformed entries — never a panic.
func FuzzParseRules(f *testing.F) {
	f.Add([]byte(`rules:
  restart-on-fail:
    if:
      state: failed
    then:
      restart: {}
    for: 2
`))
	f.Add([]byte("rules:\n  bad: not-a-map\n"))
	f.Add([]byte("rules:\n  incomplete:\n    if: {}\n"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, source []byte) {
		var tree map[string]any
		if err := yaml.Unmarshal(source, &tree); err != nil || tree == nil {
			return
		}
		_, _ = ParseRules(tree)
	})
}
