package checks

import (
	"errors"

	"sermo/internal/cfgval"
	"sermo/internal/conn"
)

// CheckKeyMemory requests Docker's optional resource sample.
const CheckKeyMemory = "memory"

// RPCBindParams preserves present-but-invalid selectors for shared validation.
func RPCBindParams(entry map[string]any) map[string]string {
	params := make(map[string]string)
	for _, key := range []string{conn.ParamKeyRPCProgram, conn.ParamKeyRPCVersion, CheckKeyTransport} {
		if value, present := entry[key]; present {
			params[key] = cfgval.String(value)
		}
	}
	return params
}

// DockerMemoryFields keeps the opt-in resource probe tied to a container.
func DockerMemoryFields(entry map[string]any) error {
	if raw, present := entry[CheckKeyMemory]; present {
		memory, ok := raw.(bool)
		if !ok {
			return errors.New("docker memory must be a boolean")
		}
		if memory && cfgval.AsString(entry[CheckKeyContainer]) == "" {
			return errors.New("docker memory requires a container")
		}
	}
	return nil
}
