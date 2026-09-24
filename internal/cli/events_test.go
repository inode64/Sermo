package cli

import (
	"strings"
	"testing"
)

func TestEventTablePreservesUnicode(t *testing.T) {
	if got := eventTableValue("á界🙂x", 3); got != "á界🙂" {
		t.Fatalf("truncated value=%q", got)
	}
	message := strings.Repeat("界", eventsTableMessageWidth+1)
	want := strings.Repeat("界", eventsTableMessageWidth-eventsTableEllipsisWidth) + eventsTableEllipsis
	if got := eventTableMessage(message); got != want {
		t.Fatalf("truncated message=%q", got)
	}
}
