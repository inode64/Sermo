package config

import (
	"fmt"
	"strings"
	"testing"
)

// TestResolveRejectsCloneWithUses pins that a service declaring both clone and
// uses is rejected. The clone branch ignores uses entirely, so without this the
// daemon the author asked to inherit would be silently dropped.
func TestResolveRejectsCloneWithUses(t *testing.T) {
	cfg := loadServiceConfig(t, `
kind: service
name: svc
clone: other
uses: somedaemon
service: x
`)
	_, errs := cfg.Resolve("svc")
	if len(errs) == 0 {
		t.Fatal("expected a resolve error for clone+uses, got none")
	}
	if msg := fmt.Sprint(errs); !strings.Contains(msg, "both clone and uses") {
		t.Fatalf("errors = %v, want a clone/uses mutual-exclusion error", errs)
	}
}
