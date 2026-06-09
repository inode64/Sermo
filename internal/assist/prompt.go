// Package assist provides an interactive, extensible assistant for generating
// Sermo watch configuration (the `sermoctl wizard` command). The Prompt type is
// the reusable question/answer layer; each Assistant turns answers into a
// `watches:` config fragment.
package assist

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Prompt asks interactive questions over a reader/writer pair. All helpers
// re-prompt until the answer is valid, so callers never deal with parse errors.
type Prompt struct {
	r *bufio.Reader
	w io.Writer
}

// NewPrompt returns a Prompt reading from in and writing prompts to out.
func NewPrompt(in io.Reader, out io.Writer) *Prompt {
	return &Prompt{r: bufio.NewReader(in), w: out}
}

func (p *Prompt) printf(format string, a ...any) { fmt.Fprintf(p.w, format, a...) }

// readLine reads one trimmed line; io.EOF yields an empty string.
func (p *Prompt) readLine() string {
	line, _ := p.r.ReadString('\n')
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
	}
}

// Confirm reads a yes/no answer, returning def on an empty line.
func (p *Prompt) Confirm(question string, def bool) bool {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		p.printf("%s [%s]: ", question, hint)
		switch strings.ToLower(p.readLine()) {
		case "":
			return def
		case "y", "yes":
			return true
		case "n", "no":
			return false
		default:
			p.printf("  please answer y or n\n")
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
	}
}

// MultiChoose presents a numbered list and returns the chosen 0-based indices.
// The answer is a comma/space separated list of numbers, or "all". An empty
// answer re-prompts.
func (p *Prompt) MultiChoose(question string, options []string) []int {
	for {
		p.printList(question, options)
		p.printf("choices (e.g. 1,3 or all): ")
		ans := strings.TrimSpace(p.readLine())
		if strings.EqualFold(ans, "all") {
			idx := make([]int, len(options))
			for i := range options {
				idx[i] = i
			}
			return idx
		}
		idx, ok := parseIndices(ans, len(options))
		if ok && len(idx) > 0 {
			return idx
		}
		p.printf("  enter numbers between 1 and %d (e.g. 1,3) or 'all'\n", len(options))
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
