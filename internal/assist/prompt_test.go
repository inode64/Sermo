package assist

import (
	"errors"
	"strings"
	"testing"

	"sermo/internal/config"
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
	// Forced y/n: y and n answer directly; an empty line re-prompts (it does NOT
	// take the default) until an explicit answer is typed.
	p, out := newTestPrompt("y\nn\n\nyes\n")
	if !p.Confirm("ok?", false) {
		t.Fatal("y should be true")
	}
	if p.Confirm("ok?", true) {
		t.Fatal("n should be false")
	}
	if !p.Confirm("ok?", true) {
		t.Fatal("after the empty line re-prompts, 'yes' should be true")
	}
	if !strings.Contains(out.String(), strings.TrimSpace(promptConfirmAnswerRequired)) {
		t.Fatalf("an empty answer must re-prompt, got %q", out.String())
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

	p2, _ := newTestPrompt(config.SelectionKeywordAll + "\n")
	if got := p2.MultiChoose("pick", []string{"a", "b", "c"}); len(got) != 3 {
		t.Fatalf("'all' should select every option, got %v", got)
	}
}

func TestPromptMultiChooseByName(t *testing.T) {
	opts := []string{config.NotifyNone + " (do not notify)", "ops-email", config.NotifyKeywordDefault + " (inherit global notify)"}

	// "none" / "default" select the matching always-present entries by name.
	if got := mustMultiChoose(t, config.NotifyNone+"\n", opts); len(got) != 1 || got[0] != 0 {
		t.Fatalf("'none' = %v, want [0]", got)
	}
	if got := mustMultiChoose(t, strings.ToUpper(config.NotifyKeywordDefault)+"\n", opts); len(got) != 1 || got[0] != 2 {
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

func TestPromptMultiChooseKeyword(t *testing.T) {
	opts := []string{"ops-email", "team-slack"}

	t.Run("keywords return without indices", func(t *testing.T) {
		p, _ := newTestPrompt(config.NotifyNone + "\n")
		if idx, kw := p.MultiChooseKeyword("pick", opts, config.NotifyNone, config.NotifyKeywordDefault); kw != config.NotifyNone || idx != nil {
			t.Fatalf("= (%v, %q), want (nil, none)", idx, kw)
		}
		p, _ = newTestPrompt(strings.ToUpper(config.NotifyKeywordDefault) + "\n")
		if _, kw := p.MultiChooseKeyword("pick", opts, config.NotifyNone, config.NotifyKeywordDefault); kw != config.NotifyKeywordDefault {
			t.Fatalf("keyword should match case-insensitively, got %q", kw)
		}
	})

	t.Run("numbers, names and all select options", func(t *testing.T) {
		p, _ := newTestPrompt("2\n")
		if idx, kw := p.MultiChooseKeyword("pick", opts, config.NotifyNone, config.NotifyKeywordDefault); kw != "" || len(idx) != 1 || idx[0] != 1 {
			t.Fatalf("= (%v, %q), want ([1], \"\")", idx, kw)
		}
		p, _ = newTestPrompt("team-slack\n")
		if idx, _ := p.MultiChooseKeyword("pick", opts, config.NotifyNone, config.NotifyKeywordDefault); len(idx) != 1 || idx[0] != 1 {
			t.Fatalf("name = %v, want [1]", idx)
		}
		p, _ = newTestPrompt(config.SelectionKeywordAll + "\n")
		if idx, _ := p.MultiChooseKeyword("pick", opts, config.NotifyNone, config.NotifyKeywordDefault); len(idx) != 2 {
			t.Fatalf("'all' = %v, want both options", idx)
		}
	})

	t.Run("keywords do not occupy menu rows", func(t *testing.T) {
		p, out := newTestPrompt("1\n")
		if idx, _ := p.MultiChooseKeyword("pick", opts, config.NotifyNone, config.NotifyKeywordDefault); idx[0] != 0 {
			t.Fatalf("1 = %v, want the first defined option", idx)
		}
		if s := out.String(); strings.Contains(s, "1) none") || strings.Contains(s, "3) default") {
			t.Fatalf("reserved answers must not be listed as rows: %q", s)
		}
		if s := out.String(); !strings.Contains(s, "'none'") || !strings.Contains(s, "'default'") || !strings.Contains(s, "'all'") {
			t.Fatalf("keywords must be offered in the question hint: %q", s)
		}
	})

	t.Run("empty option list still accepts keywords", func(t *testing.T) {
		p, out := newTestPrompt(config.SelectionKeywordAll + "\n" + config.NotifyNone + "\n")
		idx, kw := p.MultiChooseKeyword("pick", nil, config.NotifyNone, config.NotifyKeywordDefault)
		if kw != config.NotifyNone || idx != nil {
			t.Fatalf("= (%v, %q), want (nil, none): 'all' is meaningless without options", idx, kw)
		}
		if !strings.Contains(out.String(), "enter 'none' or 'default'") {
			t.Fatalf("expected the no-options hint, got %q", out.String())
		}
	})
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

// driveWithRecover runs fn under the Recover contract and returns the error.
func driveWithRecover(fn func()) (err error) {
	defer Recover(&err)
	fn()
	return nil
}

func TestPromptAbortsOnExhaustedInput(t *testing.T) {
	cases := map[string]struct {
		input string // one unusable answer, then EOF
		drive func(p *Prompt)
	}{
		"Choose invalid then EOF":      {"zzz\n", func(p *Prompt) { p.Choose("pick", []string{"a", "b"}) }},
		"MultiChoose invalid then EOF": {"zzz\n", func(p *Prompt) { p.MultiChoose("pick", []string{"a", "b"}) }},
		"AskNonEmpty empty then EOF":   {"\n", func(p *Prompt) { p.AskNonEmpty("value") }},
		"Confirm empty then EOF":       {"\n", func(p *Prompt) { p.Confirm("ok?", true) }},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p, _ := newTestPrompt(tc.input)
			if err := driveWithRecover(func() { tc.drive(p) }); !errors.Is(err, ErrInputClosed) {
				t.Fatalf("err = %v, want ErrInputClosed", err)
			}
		})
	}
}

func TestPromptDefaultsSurviveEOF(t *testing.T) {
	// The empty-accepting helpers keep their "empty -> default" contract on a
	// fully exhausted reader instead of aborting. Confirm is NOT one of them
	// anymore (it forces an explicit answer; see TestPromptAbortsOnExhaustedInput).
	p, _ := newTestPrompt("")
	err := driveWithRecover(func() {
		if got := p.Ask("q", "def"); got != "def" {
			t.Errorf("Ask = %q", got)
		}
		if got := p.AskInt("n", 7); got != 7 {
			t.Errorf("AskInt = %d", got)
		}
	})
	if err != nil {
		t.Fatalf("defaults must not abort: %v", err)
	}
}

func TestPromptAnswerWithoutTrailingNewline(t *testing.T) {
	// A last line without \n is a valid answer even though it sets EOF.
	p, _ := newTestPrompt("2")
	if idx := p.Choose("pick", []string{"a", "b"}); idx != 1 {
		t.Fatalf("Choose = %d, want 1", idx)
	}
}

func TestRecoverRepanicsOtherPanics(t *testing.T) {
	defer func() {
		if r := recover(); r != "boom" {
			t.Fatalf("recovered %v, want boom", r)
		}
	}()
	var err error
	defer Recover(&err)
	panic("boom")
}
