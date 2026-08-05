package config

import "testing"

func TestValidateSSHIdleCheck(t *testing.T) {
	issues := validateService(t, `
name: ssh-guard
service: sshd
checks:
  idle:
    type: ssh_idle
    idle_for: 30m
    sshd_exe: /usr/sbin/sshd
    count: { op: ">", value: 0 }
    protected_processes:
      deploy: { user: deploy }
      dba: { group: database }
      backup: { exe: /usr/bin/mysqldump, user: backup }
`)
	mustNotHave(t, issues, "checks.idle")
}

func TestValidateSSHIdleCheckErrors(t *testing.T) {
	issues := validateService(t, `
name: ssh-guard
service: sshd
checks:
  idle:
    type: ssh_idle
    idle_for: never
    sshd_exe: sshd
    count: bad
    protected_processes:
      empty: {}
      invalid: { exe: relative/command, cmd: codex }
      scalar: deploy
`)
	mustHave(t, issues, "checks.idle.idle_for")
	mustHave(t, issues, "checks.idle.sshd_exe path \"sshd\" must be absolute")
	mustHave(t, issues, "checks.idle.count must be a mapping")
	mustHave(t, issues, "checks.idle.protected_processes.empty requires exe, user or group")
	mustHave(t, issues, "checks.idle.protected_processes.invalid.cmd is not supported")
	mustHave(t, issues, "checks.idle.protected_processes.invalid.exe path \"relative/command\" must be absolute")
	mustHave(t, issues, "checks.idle.protected_processes.scalar must be a mapping")
}
