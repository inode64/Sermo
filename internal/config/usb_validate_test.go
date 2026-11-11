package config

import "testing"

func TestValidateUSBCheck(t *testing.T) {
	assertServiceValidation(t, `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  # Unquoted on purpose: the colon keeps a vendor:product pair a string.
  dongle: { type: usb, device: 0480:a00c }
  unit:   { type: usb, device: "0557:2221", serial_number: "0001234" }
  serial: { type: usb, serial_number: "20231012004242F" }
`, "dongle", `
name: svc
service: x
policy: { cooldown: 5m }
checks:
  none:    { type: usb }
  vendor:  { type: usb, device: "0480" }
  dash:    { type: usb, device: 0480-a00c }
  # An unquoted serial of digits is a number: its leading zeros are gone.
  digits:  { type: usb, serial_number: 0001234 }
`,
		"checks.none requires device (vendor:product) and/or serial_number for a usb check",
		"checks.vendor.device must be a vendor:product ID as lsusb prints it (e.g. 0480:a00c)",
		"checks.dash.device must be a vendor:product ID",
		"checks.digits.serial_number must be a non-empty quoted string")
}
