package assist

import (
	"strings"
	"testing"
)

func newTestPrompt(input string) (*Prompt, *strings.Builder) {
	out := &strings.Builder{}
	return NewPrompt(strings.NewReader(input), out), out
}

func TestPromptAskDefault(t *testing.T) {
	p, _ := newTestPrompt("\nhello\n")
	if got := p.Ask("q", "def"); got != "def" {
		t.Fatalf("empty input should use default, got %q", got)
	}
	if got := p.Ask("q", "def"); got != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestPromptConfirm(t *testing.T) {
	p, _ := newTestPrompt("y\nn\n\n")
	if !p.Confirm("ok?", false) {
		t.Fatal("y should be true")
	}
	if p.Confirm("ok?", true) {
		t.Fatal("n should be false")
	}
	if !p.Confirm("ok?", true) {
		t.Fatal("empty should use default true")
	}
}

func TestPromptChooseRepromptsOnBad(t *testing.T) {
	// "9" out of range, "x" not a number, then "2".
	p, out := newTestPrompt("9\nx\n2\n")
	idx := p.Choose("pick", []string{"a", "b", "c"})
	if idx != 1 {
		t.Fatalf("Choose = %d, want 1 (b)", idx)
	}
	if strings.Count(out.String(), "pick") < 2 {
		t.Fatalf("expected re-prompt on bad input, output: %q", out.String())
	}
}

func TestPromptMultiChoose(t *testing.T) {
	p, _ := newTestPrompt("1,3\n")
	got := p.MultiChoose("pick", []string{"a", "b", "c"})
	if len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("MultiChoose = %v, want [0 2]", got)
	}

	p2, _ := newTestPrompt("all\n")
	if got := p2.MultiChoose("pick", []string{"a", "b", "c"}); len(got) != 3 {
		t.Fatalf("'all' should select every option, got %v", got)
	}
}

func TestPromptMultiChooseByName(t *testing.T) {
	opts := []string{"none (do not notify)", "ops-email", "default (inherit global notify)"}

	// "none" / "default" select the matching always-present entries by name.
	if got := mustMultiChoose(t, "none\n", opts); len(got) != 1 || got[0] != 0 {
		t.Fatalf("'none' = %v, want [0]", got)
	}
	if got := mustMultiChoose(t, "DEFAULT\n", opts); len(got) != 1 || got[0] != 2 {
		t.Fatalf("'default' (case-insensitive) = %v, want [2]", got)
	}
	if got := mustMultiChoose(t, "ops-email\n", opts); len(got) != 1 || got[0] != 1 {
		t.Fatalf("notifier name = %v, want [1]", got)
	}

	// An unknown name re-prompts, then a number is accepted.
	p, out := newTestPrompt("nope\n2\n")
	if got := p.MultiChoose("pick", opts); len(got) != 1 || got[0] != 1 {
		t.Fatalf("after bad name = %v, want [1]", got)
	}
	if strings.Count(out.String(), "pick") < 2 {
		t.Fatalf("expected re-prompt on unknown name, output: %q", out.String())
	}
}

func mustMultiChoose(t *testing.T, input string, opts []string) []int {
	t.Helper()
	p, _ := newTestPrompt(input)
	return p.MultiChoose("pick", opts)
}

func TestPromptAskInt(t *testing.T) {
	p, _ := newTestPrompt("\nnope\n42\n")
	if got := p.AskInt("n", 7); got != 7 {
		t.Fatalf("empty -> default, got %d", got)
	}
	if got := p.AskInt("n", 7); got != 42 {
		t.Fatalf("got %d, want 42 (after re-prompt)", got)
	}
}
