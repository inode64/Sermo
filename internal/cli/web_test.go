package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sermo/internal/config"
	"sermo/internal/webcred"
)

// runWebCommand drives `sermoctl web ...` with a scripted stdin and returns the
// exit code plus both streams.
func runWebCommand(t *testing.T, stdin string, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	app := App{
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader(stdin),
		Env:    func(string) string { return "" },
	}
	code := app.Run(t.Context(), args)
	return code, stdout.String(), stderr.String()
}

func TestWebHashPassword(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		stdin     string
		wantCode  int
		wantLine  string // prefix the credential line must have
		wantErr   string
		wantLabel string
	}{
		{
			name:     "stdin defaults to bcrypt",
			args:     []string{commandWeb, commandWebHashPassword, "--" + cliFlagStdin},
			stdin:    "s3cret\n",
			wantCode: exitSuccess,
			wantLine: "$2",
		},
		{
			name:     "stdin with an explicit sha256 format",
			args:     []string{commandWeb, commandWebHashPassword, "--" + cliFlagStdin, "--" + cliFlagHash, webcred.FormatSHA256},
			stdin:    "s3cret\n",
			wantCode: exitSuccess,
			wantLine: webcred.PrefixSHA256,
			wantErr:  "only safe for a high-entropy secret",
		},
		{
			name:      "the label becomes a trailing comment",
			args:      []string{commandWeb, commandWebHashPassword, "--" + cliFlagStdin, "--" + cliFlagName, "ana"},
			stdin:     "s3cret\n",
			wantCode:  exitSuccess,
			wantLine:  "$2",
			wantLabel: "# ana",
		},
		{
			name:     "an empty password is refused",
			args:     []string{commandWeb, commandWebHashPassword, "--" + cliFlagStdin},
			stdin:    "\n",
			wantCode: exitRuntimeError,
			wantErr:  "the password is empty",
		},
		{
			name:     "a non-terminal stdin needs --stdin",
			args:     []string{commandWeb, commandWebHashPassword},
			stdin:    "s3cret\n",
			wantCode: exitRuntimeError,
			wantErr:  "standard input is not a terminal",
		},
		{
			name:     "unknown hash format",
			args:     []string{commandWeb, commandWebHashPassword, "--" + cliFlagStdin, "--" + cliFlagHash, "md5"},
			stdin:    "s3cret\n",
			wantCode: exitUsage,
			wantErr:  `unknown --hash "md5"`,
		},
		{
			name:     "a bcrypt cost out of range is refused",
			args:     []string{commandWeb, commandWebHashPassword, "--" + cliFlagStdin, "--" + cliFlagCost, "99"},
			stdin:    "s3cret\n",
			wantCode: exitRuntimeError,
			wantErr:  "out of range",
		},
		{
			name:     "hash-password takes no argument",
			args:     []string{commandWeb, commandWebHashPassword, "s3cret"},
			wantCode: exitUsage,
			wantErr:  "takes no argument",
		},
		{
			name:     "unknown subcommand",
			args:     []string{commandWeb, "whatever"},
			wantCode: exitUsage,
			wantErr:  `unknown web subcommand "whatever"`,
		},
		{
			name:     "web needs a subcommand",
			args:     []string{commandWeb},
			wantCode: exitUsage,
			wantErr:  "web requires a subcommand",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, stdout, stderr := runWebCommand(t, tc.stdin, tc.args...)
			if code != tc.wantCode {
				t.Fatalf("exit = %d, want %d (stderr %q)", code, tc.wantCode, stderr)
			}
			if tc.wantErr != "" && !strings.Contains(stderr, tc.wantErr) {
				t.Errorf("stderr = %q, want it to contain %q", stderr, tc.wantErr)
			}
			if tc.wantLine == "" {
				return
			}
			line := strings.TrimSpace(stdout)
			if !strings.HasPrefix(line, tc.wantLine) {
				t.Fatalf("credential line = %q, want the %s prefix", line, tc.wantLine)
			}
			if tc.wantLabel != "" && !strings.HasSuffix(line, tc.wantLabel) {
				t.Errorf("credential line = %q, want it to end in %q", line, tc.wantLabel)
			}
			// The printed line must be a credential the daemon accepts for the
			// password it was made from, and for nothing else.
			list, err := webcred.Parse(line)
			if err != nil {
				t.Fatalf("Parse(%q) error = %v", line, err)
			}
			if !list.Verify(t.Context(), strings.TrimSpace(tc.stdin)) {
				t.Errorf("the printed credential does not accept the password it was made from")
			}
			if list.Verify(t.Context(), "wrong") {
				t.Error("the printed credential accepts a wrong password")
			}
		})
	}
}

// --generate prints the secret once, because the operator has no other way to
// learn what to type in the browser, plus the credential line for the file.
func TestWebHashPasswordGenerate(t *testing.T) {
	code, stdout, stderr := runWebCommand(t, "", commandWeb, commandWebHashPassword, "--"+cliFlagGenerate)
	if code != exitSuccess {
		t.Fatalf("exit = %d, want %d (stderr %q)", code, exitSuccess, stderr)
	}
	// stdout carries the credential line and nothing else: it is meant to be
	// appended straight to the credential file, where a stray `secret: ...` line
	// would become a cleartext credential of its own.
	line := strings.TrimSpace(stdout)
	if strings.Contains(line, "\n") || !strings.HasPrefix(line, webcred.PrefixSHA256) {
		t.Fatalf("stdout = %q, want only the %s credential line", stdout, webcred.PrefixSHA256)
	}
	secret, ok := strings.CutPrefix(strings.TrimSpace(stderr), webOutputSecretLabel+" ")
	if !ok || secret == "" {
		t.Fatalf("stderr = %q, want %q plus the secret", stderr, webOutputSecretLabel)
	}
	list, err := webcred.Parse(line)
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	if !list.Verify(t.Context(), secret) {
		t.Error("the printed credential does not accept the generated secret")
	}
	if strings.Contains(stderr, "warning") {
		t.Errorf("stderr = %q, want no warning for a generated secret", stderr)
	}
	// Appending stdout to a file must leave exactly one usable credential.
	if list.Len() != 1 {
		t.Errorf("the printed output parses to %d credentials, want 1", list.Len())
	}
}

// A label is a comment on the credential line, so it must not be able to add a
// line of its own.
func TestWebHashPasswordRejectsMultilineLabel(t *testing.T) {
	code, stdout, stderr := runWebCommand(t, "s3cret\n", commandWeb, commandWebHashPassword,
		"--"+cliFlagStdin, "--"+cliFlagName, "ana\nsmuggled-password")
	if code != exitUsage {
		t.Fatalf("exit = %d, want %d", code, exitUsage)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want nothing written", stdout)
	}
	if !strings.Contains(stderr, "single line") {
		t.Errorf("stderr = %q, want it to explain the single-line rule", stderr)
	}
}

func TestWebHashPasswordGenerateRejectsStdin(t *testing.T) {
	code, _, stderr := runWebCommand(t, "s3cret\n", commandWeb, commandWebHashPassword, "--"+cliFlagGenerate, "--"+cliFlagStdin)
	if code != exitRuntimeError {
		t.Fatalf("exit = %d, want %d", code, exitRuntimeError)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("stderr = %q, want a mutual-exclusion error", stderr)
	}
}

// The credential sermoctl sends follows a fixed precedence, because a hashed
// password file leaves nothing for it to send.
func TestDaemonWebPasswordPrecedence(t *testing.T) {
	root := t.TempDir()
	runtimeDir := filepath.Join(root, "run")
	if err := os.MkdirAll(runtimeDir, 0o700); err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(runtimeDir, config.DaemonWebTokenFilename)

	localCfg := &config.Config{Global: config.Global{
		Runtime: runtimeDir,
		Raw:     map[string]any{config.SectionWeb: map[string]any{}},
	}}
	// A daemon on another host never knows this host's runtime token.
	remoteCfg := &config.Config{Global: config.Global{
		Runtime: runtimeDir,
		Raw:     map[string]any{config.SectionWeb: map[string]any{config.WebKeyAddress: "10.0.0.9"}},
	}}

	tests := []struct {
		name  string
		cfg   *config.Config
		env   string
		token string
		want  string
	}{
		{name: "environment wins", cfg: localCfg, env: "from-env", token: "from-token", want: "from-env"},
		{name: "local token", cfg: localCfg, token: "from-token", want: "from-token"},
		{name: "no credential available", cfg: localCfg, want: ""},
		{name: "a remote daemon never gets the local token", cfg: remoteCfg, token: "from-token", want: ""},
		{name: "the environment still reaches a remote daemon", cfg: remoteCfg, env: "from-env", token: "from-token", want: "from-env"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.token != "" {
				mustWrite(t, tokenPath, tc.token+"\n")
			} else if err := os.RemoveAll(tokenPath); err != nil {
				t.Fatal(err)
			}
			app := App{Env: func(name string) string {
				if name == config.EnvWebPassword {
					return tc.env
				}
				return ""
			}}
			if got := app.daemonWebPassword(tc.cfg); got != tc.want {
				t.Errorf("daemonWebPassword() = %q, want %q", got, tc.want)
			}
		})
	}
}
