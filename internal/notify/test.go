package notify

const (
	// TestSubject identifies an operator-initiated test message.
	TestSubject = "[sermo] notifier test"
	// TestField marks an operator-initiated test in structured notifier payloads.
	TestField = "SERMO_TEST"
)

// TestMessage returns the fixed message used to verify one notifier's delivery
// configuration. It intentionally has no monitored target.
func TestMessage() Message {
	return Message{
		Subject: TestSubject,
		Body:    "This is a test notification sent by Sermo at an operator's request.",
		Fields: map[string]string{
			TestField: "true",
		},
	}
}
