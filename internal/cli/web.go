package cli

import (
	"bufio"
	"errors"
	"fmt"
	"slices"
	"strings"

	"sermo/internal/webcred"
)

const (
	// webLabelPrefix starts the trailing comment --name adds to a credential
	// line, so an operator can tell whose credential it is.
	webLabelPrefix = "#"
	// webOutputSecretLabel introduces a generated secret on stderr.
	webOutputSecretLabel = "secret:"
	webPromptPassword    = "Password: "
	webPromptRepeat      = "Repeat:   "
)

// runWeb dispatches the `web` command, which owns the dashboard credential
// tooling: hashing a password so `web.password_file` never holds it in readable
// form.
func (a App) runWeb(opts options) int {
	if len(opts.args) == 0 {
		return a.commandUsageError(commandWeb, "web requires a subcommand ("+commandWebHashPassword+")")
	}
	sub := opts.args[0]
	rest := opts.args[1:]
	switch sub {
	case commandWebHashPassword:
		return a.runWebHashPassword(opts, rest)
	default:
		return a.commandUsageError(commandWeb, fmt.Sprintf("unknown web subcommand %q", sub))
	}
}

// runWebHashPassword prints one credential line for web.password_file. The
// password itself is never echoed back except with --generate, where the
// operator has no other way to learn the secret they must type in the browser.
func (a App) runWebHashPassword(opts options, rest []string) int {
	if len(rest) > 0 {
		return a.commandUsageError(commandWeb, commandWebHashPassword+" takes no argument; the password is read from the terminal, --stdin or --generate")
	}
	format, err := webHashFormat(opts)
	if err != nil {
		return a.commandUsageError(commandWeb, err.Error())
	}
	password, generated, err := a.webHashPasswordInput(opts)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	line, err := webcred.Hash(password, format, opts.cost)
	if err != nil {
		return a.fail(opts, fmt.Sprintf("hash password: %v", err))
	}
	label := strings.TrimSpace(opts.name)
	if strings.ContainsAny(label, "\r\n") {
		return a.commandUsageError(commandWeb, "--"+cliFlagName+" must be a single line; a newline would split the credential line in two")
	}
	if label != "" {
		line += "   " + webLabelPrefix + " " + label
	}
	if generated {
		// The secret goes to stderr, never stdout: stdout is meant to be appended
		// straight to the credential file, and a `secret: ...` line landing there
		// would become a cleartext credential of its own.
		fmt.Fprintf(a.Stderr, "%s %s\n", webOutputSecretLabel, password)
	}
	fmt.Fprintln(a.Stdout, line)
	if format == webcred.FormatSHA256 && !generated {
		fmt.Fprintf(a.Stderr, "warning: %s is only safe for a high-entropy secret; a password a person chose belongs in %s\n",
			webcred.FormatSHA256, webcred.FormatBcrypt)
	}
	return exitSuccess
}

// webHashFormat resolves --hash, defaulting to the fast form for a generated
// secret and to bcrypt for anything a person may have chosen.
func webHashFormat(opts options) (string, error) {
	switch {
	case opts.hash == "" && opts.generate:
		return webcred.FormatSHA256, nil
	case opts.hash == "":
		return webcred.FormatBcrypt, nil
	case slices.Contains(webcred.Formats(), opts.hash):
		return opts.hash, nil
	default:
		return "", fmt.Errorf("unknown --%s %q (expected %s)", cliFlagHash, opts.hash, strings.Join(webcred.Formats(), " or "))
	}
}

// webHashPasswordInput returns the password to hash and whether it was
// generated here (and therefore must be printed once).
func (a App) webHashPasswordInput(opts options) (string, bool, error) {
	switch {
	case opts.generate:
		if opts.stdin {
			return "", false, fmt.Errorf("--%s and --%s are mutually exclusive", cliFlagGenerate, cliFlagStdin)
		}
		secret, err := webcred.GenerateSecret()
		if err != nil {
			return "", false, fmt.Errorf("generate secret: %w", err)
		}
		return secret, true, nil
	case opts.stdin:
		password, err := readPasswordLine(bufio.NewReader(a.stdinReader()))
		if err != nil {
			return "", false, err
		}
		return password, false, nil
	default:
		password, err := a.promptPassword()
		return password, false, err
	}
}

// promptPassword reads a password twice from the terminal with echo disabled. A
// non-terminal stdin is a usage error rather than a silent read: piping a
// password into an interactive prompt would echo it into the transcript.
func (a App) promptPassword() (string, error) {
	if !stdinIsTerminal(a.stdinReader()) {
		return "", fmt.Errorf("standard input is not a terminal; use --%s or --%s", cliFlagStdin, cliFlagGenerate)
	}
	// One buffered reader for both prompts: a second one could not see input the
	// first had already buffered.
	reader := bufio.NewReader(a.stdinReader())
	first, err := a.readHiddenPassword(reader, webPromptPassword)
	if err != nil {
		return "", err
	}
	again, err := a.readHiddenPassword(reader, webPromptRepeat)
	if err != nil {
		return "", err
	}
	if first != again {
		return "", errors.New("the two passwords do not match")
	}
	return first, nil
}

func (a App) readHiddenPassword(reader *bufio.Reader, prompt string) (string, error) {
	fmt.Fprint(a.Stderr, prompt)
	defer fmt.Fprintln(a.Stderr)
	restore, err := disableEcho(a.stdinReader())
	if err != nil {
		return "", err
	}
	defer restore()
	return readPasswordLine(reader)
}

// readPasswordLine reads a single line and strips the trailing newline every
// editor and `echo` appends, exactly as the daemon does when reading a
// credential file.
func readPasswordLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	line = strings.TrimRight(line, "\r\n")
	if err != nil && line == "" {
		return "", fmt.Errorf("read password: %w", err)
	}
	if line == "" {
		return "", errors.New("the password is empty")
	}
	return line, nil
}
