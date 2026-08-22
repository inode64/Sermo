package app

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/execx"
	"sermo/internal/output"
	"sermo/internal/web"
)

// serviceButton is one configured operator button: a named command the
// dashboard offers as an explicit admin action for one service. Buttons are
// manual by definition, so dry_run does not gate them — like the built-in
// operate verbs, pressing one is the operator acting, not the daemon deciding.
type serviceButton struct {
	name    string
	label   string
	command []string
	timeout time.Duration
}

// serviceButtons reads the resolved buttons: section of one service tree.
// Validation has already rejected malformed entries; anything that still fails
// to parse here is silently skipped rather than half-built.
func serviceButtons(tree map[string]any) []serviceButton {
	section, ok := tree[config.SectionButtons].(map[string]any)
	if !ok || len(section) == 0 {
		return nil
	}
	out := make([]serviceButton, 0, len(section))
	for name, raw := range section {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		command := cfgval.StringArray(entry[config.ButtonKeyCommand])
		if len(command) == 0 {
			continue
		}
		label := cfgval.String(entry[config.ButtonKeyLabel])
		if label == "" {
			label = name
		}
		out = append(out, serviceButton{
			name:    name,
			label:   label,
			command: command,
			timeout: cfgval.Duration(entry[config.ButtonKeyTimeout]),
		})
	}
	sortButtons(out)
	return out
}

func sortButtons(buttons []serviceButton) {
	for i := 1; i < len(buttons); i++ {
		for j := i; j > 0 && buttons[j].name < buttons[j-1].name; j-- {
			buttons[j], buttons[j-1] = buttons[j-1], buttons[j]
		}
	}
}

// serviceButtonViews projects the configured buttons into the payload: name
// and label only — the command stays server-side.
func serviceButtonViews(buttons []serviceButton) []web.ServiceButton {
	if len(buttons) == 0 {
		return nil
	}
	out := make([]web.ServiceButton, 0, len(buttons))
	for _, b := range buttons {
		out = append(out, web.ServiceButton{Name: b.name, Label: b.label})
	}
	return out
}

// ServiceButton runs one configured operator button and reports the outcome.
// The command runs exactly as configured (argv, never a shell), bounded by the
// button's timeout or the backend's operation timeout, and leaves an event
// either way.
func (b *WebBackend) ServiceButton(ctx context.Context, service, button string) web.ActionResult {
	e := b.entries[service]
	if e == nil {
		return web.ActionResult{Message: fmt.Sprintf(unknownServiceMessageFmt, service)}
	}
	var found *serviceButton
	for i := range e.buttons {
		if e.buttons[i].name == button {
			found = &e.buttons[i]
			break
		}
	}
	if found == nil || e.disabled {
		return web.ActionResult{Message: fmt.Sprintf("service %q has no button %q configured", service, button)}
	}
	timeout := found.timeout
	if timeout <= 0 {
		timeout = b.operationTimeout
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	runner := execx.RunnerOrDefault(b.execRunner)
	res, err := runner.Run(runCtx, found.command[0], found.command[1:]...)
	action := "button:" + button
	if err != nil || res.ExitCode != 0 {
		msg := fmt.Sprintf("%s: %s", found.label, execx.OperatorFailureOr(err, res, timeout, fmt.Sprintf("exit %d", res.ExitCode)))
		if line := output.FirstNonEmptyLine(res.Stderr); line != "" {
			msg += ": " + line
		}
		b.emitMonitorEvent(service, action, eventKindError, eventStatusFailed, msg)
		return web.ActionResult{Message: msg}
	}
	msg := found.label + ": ok"
	if line := output.FirstNonEmptyLine(res.Stdout); line != "" {
		msg += ": " + line
	}
	b.emitMonitorEvent(service, action, eventKindAction, eventStatusOK, msg)
	return web.ActionResult{OK: true, Message: msg}
}
