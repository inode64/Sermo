package app

import (
	"strconv"
	"time"

	"sermo/internal/checks"
)

func addSummaryAge(data map[string]any, env map[string]string) {
	ageSeconds, ok := env[sermoEnvAgeSeconds]
	if !ok {
		return
	}
	seconds, err := strconv.ParseInt(ageSeconds, envFormatBase, envFloatBits)
	if err != nil {
		return
	}
	data[checks.DataKeyAge] = time.Duration(seconds) * time.Second
	data[checks.DataKeyValue] = data[checks.DataKeyAge]
}
