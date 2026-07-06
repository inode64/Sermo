package cli

import (
	"context"
	"errors"
	"fmt"
	"os/user"
	"strings"

	"sermo/internal/notify"
	"sermo/internal/operation"
)

func (a App) notifyInteractiveBlockedAction(ctx context.Context, result operation.Result) {
	if !shouldNotifyInteractiveBlockedAction(result) {
		return
	}
	userName, ok := a.interactiveUser()
	if !ok {
		return
	}
	notifyBlocked := a.NotifyBlockedAction
	if notifyBlocked == nil {
		notifyBlocked = notifyBlockedActionTTY
	}
	_ = notifyBlocked(ctx, result, userName)
}

func shouldNotifyInteractiveBlockedAction(result operation.Result) bool {
	return result.Status == operation.ResultBlocked &&
		result.Action == actionRestart &&
		strings.Contains(strings.ToLower(result.Message), "backup")
}

func (a App) interactiveUser() (string, bool) {
	if a.InteractiveUser != nil {
		return a.InteractiveUser()
	}
	if !stdinIsTerminal(a.Stdin) {
		return "", false
	}
	name := loginUser(a.Env)
	return name, name != ""
}

func loginUser(env func(string) string) string {
	if env == nil {
		env = func(string) string { return "" }
	}
	for _, key := range []string{"SUDO_USER", "DOAS_USER", "LOGNAME", "USER"} {
		if value := strings.TrimSpace(env(key)); value != "" {
			return value
		}
	}
	current, err := user.Current()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(current.Username)
}

func notifyBlockedActionTTY(ctx context.Context, result operation.Result, userName string) error {
	registry, warnings := notify.Build(map[string]any{
		"operator-tty": map[string]any{
			"type":  "tty",
			"users": []any{userName},
		},
	}, notify.WithoutTemplates())
	if len(warnings) > 0 {
		return errors.New(strings.Join(warnings, "; "))
	}
	notifier := registry["operator-tty"]
	if notifier == nil {
		return errors.New("operator-tty notifier unavailable")
	}
	return notifier.Send(ctx, notify.Message{
		Subject: fmt.Sprintf("Sermo denied %s restart", result.Service),
		Body:    fmt.Sprintf("A restart request for %s was denied: %s.", result.Service, result.Message),
		Fields: map[string]string{
			"SERMO_SERVICE": result.Service,
			"SERMO_ACTION":  result.Action,
			"SERMO_STATUS":  string(result.Status),
		},
	})
}
