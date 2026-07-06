package config

import (
	"slices"
	"testing"
)

func TestNormalizeServiceUnits(t *testing.T) {
	got := normalizeServiceUnits([]string{
		" nginx.service ",
		"",
		"nginx.service",
		"\tphp-fpm.service\n",
		"php-fpm.service",
	})
	want := []string{"nginx.service", "php-fpm.service"}
	if !slices.Equal(got, want) {
		t.Fatalf("normalizeServiceUnits = %v, want %v", got, want)
	}
}
