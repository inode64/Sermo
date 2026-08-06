package config

import "testing"

func TestValidateTerminalSessionsCheck(t *testing.T) {
	issues := validateService(t, `
name: ssh
service: sshd
checks:
  tmux-sessions:
    type: terminal_sessions
    multiplexer: tmux
    binary: /usr/bin/tmux
    user: deploy
    socket: /run/tmux/deploy.sock
    count: { op: ">", value: 0 }
  screen-sessions:
    type: terminal_sessions
    multiplexer: screen
    binary: /usr/bin/screen
    user: backup
    detached: { op: ">", value: 0 }
`)
	mustNotHave(t, issues, "checks.tmux-sessions")
	mustNotHave(t, issues, "checks.screen-sessions")
}

func TestValidateTerminalSessionsCheckErrors(t *testing.T) {
	issues := validateService(t, `
name: ssh
service: sshd
checks:
  sessions:
    type: terminal_sessions
    multiplexer: terminal
    binary: tmux
    user: ""
    socket: relative.sock
    count: bad
`)
	mustHave(t, issues, "checks.sessions.multiplexer")
	mustHave(t, issues, "checks.sessions.binary path \"tmux\" must be absolute")
	mustHave(t, issues, "checks.sessions.user is required")
	mustHave(t, issues, "checks.sessions.socket is only supported for tmux")
	mustHave(t, issues, "checks.sessions.count must be a mapping")
}
