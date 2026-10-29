package execx

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestCommandRunnerInputUsesStdinAndControlledEnvironment(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	t.Setenv("SERMO_TEST_AMBIENT", "must-not-leak")
	result, err := (CommandRunner{}).RunInputEnv(ctx, "literal $value\n", []string{"PATH=/usr/bin:/bin", "SERMO_TEST_EXPLICIT=provided"}, "sh", "-c", `IFS= read -r line; printf '%s|%s|%s' "$line" "$SERMO_TEST_EXPLICIT" "$SERMO_TEST_AMBIENT"`)
	if err != nil || result.Stdout != "literal $value|provided|" {
		t.Fatalf("result=%+v error=%v", result, err)
	}
	if strings.Contains(result.Stderr, "literal") {
		t.Fatal("stdin leaked into stderr")
	}
}
