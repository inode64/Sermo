package app

import (
	"fmt"
	"time"

	"sermo/internal/checks"
)

// watchValuesEnv formats typed observations only at the hook boundary.
func watchValuesEnv(env map[string]string, values map[string]any) {
	for key, value := range values {
		switch v := value.(type) {
		case time.Duration:
			env[key] = envAgeSeconds(v)
		default:
			env[key] = fmt.Sprint(v)
		}
	}
}

func addSummaryAge(data, values map[string]any) {
	if age, ok := values[sermoEnvAgeSeconds].(time.Duration); ok {
		// Summaries and SERMO_AGE_SECONDS have always exposed whole seconds.
		data[checks.DataKeyAge] = age.Truncate(time.Second)
		data[checks.DataKeyValue] = data[checks.DataKeyAge]
	}
}
