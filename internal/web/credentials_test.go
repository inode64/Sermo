package web

import (
	"strings"
	"testing"

	"sermo/internal/webcred"
)

func testCredentials(tb testing.TB, secrets ...string) webcred.List {
	tb.Helper()
	lines := make([]string, 0, len(secrets))
	for _, secret := range secrets {
		line, err := webcred.HashBcrypt(secret, webcred.MinBcryptCost)
		if err != nil {
			tb.Fatalf("hash test credential: %v", err)
		}
		lines = append(lines, line)
	}
	list, err := webcred.Parse(strings.Join(lines, "\n"))
	if err != nil {
		tb.Fatalf("parse test credentials: %v", err)
	}
	return list
}
