// Package assist provides an interactive, extensible assistant for generating
// Sermo watch configuration (the `sermoctl wizard` command). The Prompt type is
// the reusable question/answer layer; each Assistant turns answers into a
// set of watch, storage, mount or service document bodies.
package assist

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"sermo/internal/config"
)

// Prompt asks interactive questions over a reader/writer pair. All helpers
// re-prompt until the answer is valid, so callers never deal with parse errors.
// When the input ends (EOF) while an answer is still required, the re-prompting
// helpers abort the wizard (see ErrInputClosed) instead of spinning forever.
type Prompt struct {
	r   *bufio.Reader
	w   io.Writer
	eof bool
}

// NewPrompt returns a Prompt reading from in and writing prompts to out.
func NewPrompt(in io.Reader, out io.Writer) *Prompt {
	return &Prompt{r: bufio.NewReader(in), w: out}
}

// ErrInputClosed reports that the prompt's input ended (EOF) while an answer
// was still required — e.g. piped stdin with too few lines. The helpers that
// accept an empty answer (Ask, AskInt) still return their default on EOF; the
// ones that re-prompt until a valid answer (Confirm, Choose, AskNonEmpty, …)
// abort with this error.
var ErrInputClosed = errors.New("input ended before the prompt was answered")

const promptLineBreak = '\n'

// promptAbort is the panic sentinel Recover translates into ErrInputClosed.
type promptAbort struct{}

// Recover converts an input-closed abort into ErrInputClosed on *errp; any
// other panic is re-raised. Defer it around code that drives a Prompt:
//
//	func run() (err error) {
//		defer assist.Recover(&err)
//		idx := p.Choose(...)
//		...
//	}
func Recover(errp *error) {
	r := recover()
	if r == nil {
		return
	}
	if _, ok := r.(promptAbort); ok {
		*errp = ErrInputClosed
		return
	}
	panic(r)
}

// abortIfClosed stops a re-prompt loop once the input is exhausted — without
// it, EOF would make the loop spin forever at full CPU.
func (p *Prompt) abortIfClosed() {
	if p.eof {
		panic(promptAbort{})
	}
}

func (p *Prompt) printf(format string, a ...any) { fmt.Fprintf(p.w, format, a...) }

// readLine reads one trimmed line; io.EOF yields an empty string and marks the
// input as exhausted for abortIfClosed.
func (p *Prompt) readLine() string {
	line, err := p.r.ReadString(promptLineBreak)
	if err != nil {
		p.eof = true
	}
	return strings.TrimSpace(line)
}

// Ask reads a free-text answer, returning def when the line is empty.
func (p *Prompt) Ask(question, def string) string {
	if def != "" {
		p.printf("%s [%s]: ", question, def)
	} else {
		p.printf("%s: ", question)
	}
	if line := p.readLine(); line != "" {
		return line
	}
	return def
}

// AskNonEmpty reads a free-text answer, re-prompting until it is non-empty.
func (p *Prompt) AskNonEmpty(question string) string {
	for {
		p.printf("%s: ", question)
		if line := p.readLine(); line != "" {
			return line
		}
		p.printf("  a value is required\n")
		p.abortIfClosed()
	}
}

// Confirm reads a yes/no answer. It always forces an explicit y/n: an empty
// line re-prompts rather than accepting a default, so a wizard never takes a
// destructive or shaping decision the operator did not actually type. def only
// sets which letter the hint capitalizes (the suggested answer). On EOF the
// re-prompt aborts with ErrInputClosed like every other required prompt.
func (p *Prompt) Confirm(question string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		p.printf("%s [%s]: ", question, hint)
		switch strings.ToLower(p.readLine()) {
		case "y", "yes":
			return true
		case "n", "no":
			return false
		default:
			p.printf("  please answer y or n\n")
			p.abortIfClosed()
		}
	}
}

// Choose presents a numbered list and returns the chosen 0-based index,
// re-prompting until a valid number is entered.
func (p *Prompt) Choose(question string, options []string) int {
	for {
		p.printList(question, options)
		p.printf("choice [1-%d]: ", len(options))
		n, err := strconv.Atoi(p.readLine())
		if err == nil && n >= 1 && n <= len(options) {
			return n - 1
		}
		p.printf("  enter a number between 1 and %d\n", len(options))
		p.abortIfClosed()
	}
}

// MultiChoose presents a numbered list and returns the chosen 0-based indices.
// The answer is a comma/space separated list of numbers, the keyword "all", or a
// single option name. An empty or unrecognized answer re-prompts. It is
// MultiChooseKeyword with no reserved keywords, so the all/name vocabulary stays
// identical across every wizard selection.
func (p *Prompt) MultiChoose(question string, options []string) []int {
	idx, _ := p.MultiChooseKeyword(question, options)
	return idx
}

// MultiChooseKeyword is MultiChoose for menus with reserved answers: the
// numbered list shows only the real options, and each keyword is accepted as a
// typed answer instead of occupying a row (the notifier menu's 'none' and
// 'default'). It returns (nil, keyword) when a keyword is typed; otherwise
// (indices, ""). 'all' selects every option and needs at least one to exist;
// the keywords work even with an empty option list.
func (p *Prompt) MultiChooseKeyword(question string, options []string, keywords ...string) ([]int, string) {
	quoted := make([]string, 0, len(keywords)+1)
	if len(options) > 0 {
		quoted = append(quoted, "'all'")
	}
	for _, kw := range keywords {
		quoted = append(quoted, "'"+kw+"'")
	}
	hint := "choices (" + strings.Join(quoted, ", ") + "): "
	if len(options) > 0 {
		hint = "choices (numbers like 1,3, a name, " + strings.Join(quoted, ", ") + "): "
	}
	for {
		p.printList(question, options)
		p.printf("%s", hint)
		ans := strings.TrimSpace(p.readLine())
		for _, kw := range keywords {
			if strings.EqualFold(ans, kw) {
				return nil, kw
			}
		}
		if strings.EqualFold(ans, config.SelectionKeywordAll) && len(options) > 0 {
			idx := make([]int, len(options))
			for i := range options {
				idx[i] = i
			}
			return idx, ""
		}
		if i, ok := matchOptionWord(ans, options); ok {
			return []int{i}, ""
		}
		if idx, ok := parseIndices(ans, len(options)); ok && len(idx) > 0 {
			return idx, ""
		}
		if len(options) > 0 {
			p.printf("  enter numbers between 1 and %d (e.g. 1,3), a name, or %s\n", len(options), strings.Join(quoted, ", "))
		} else {
			p.printf("  enter %s\n", strings.Join(quoted, " or "))
		}
		p.abortIfClosed()
	}
}

// AskInt reads an integer, returning def on an empty line and re-prompting on a
// non-numeric answer.
func (p *Prompt) AskInt(question string, def int) int {
	for {
		p.printf("%s [%d]: ", question, def)
		line := p.readLine()
		if line == "" {
			return def
		}
		if n, err := strconv.Atoi(line); err == nil {
			return n
		}
		p.printf("  enter a whole number\n")
	}
}

func (p *Prompt) printList(question string, options []string) {
	p.printf("%s\n", question)
	for i, o := range options {
		p.printf("  %d) %s\n", i+1, o)
	}
}

// parseIndices parses a comma/space separated list of 1-based numbers into
// distinct 0-based indices within [0,n). It reports ok=false on any invalid or
// out-of-range token.
func parseIndices(s string, n int) ([]int, bool) {
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' })
	seen := map[int]bool{}
	var out []int
	for _, f := range fields {
		v, err := strconv.Atoi(f)
		if err != nil || v < 1 || v > n {
			return nil, false
		}
		if !seen[v-1] {
			seen[v-1] = true
			out = append(out, v-1)
		}
	}
	return out, true
}

// matchOptionWord matches a single whole-word answer (case-insensitive) to the
// option whose label begins with that word, so "none" / "default" select the
// "none (…)" / "default (…)" entries the wizard always offers, and a bare
// notifier/interface/volume name selects that item. It reports ok=false unless
// exactly one option matches, so an empty, multi-token, or ambiguous answer
// falls through to numeric parsing or a re-prompt.
func matchOptionWord(ans string, options []string) (int, bool) {
	if ans == "" || strings.ContainsAny(ans, ", ") {
		return 0, false
	}
	match := -1
	for i, o := range options {
		first := o
		if cut := strings.IndexAny(o, " ("); cut >= 0 {
			first = o[:cut]
		}
		if strings.EqualFold(strings.TrimSpace(first), ans) {
			if match >= 0 {
				return 0, false // ambiguous
			}
			match = i
		}
	}
	if match < 0 {
		return 0, false
	}
	return match, true
}
