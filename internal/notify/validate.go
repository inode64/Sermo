package notify

import (
	"fmt"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/telegramapi"
)

// ValidationIssue describes one invalid field in a notifier entry. Suffix is
// ready to append to the caller's path for Field, allowing config validation
// and runtime construction to preserve their own entry-name context.
type ValidationIssue struct {
	Field  string
	Suffix string
}

// ValidateEntry validates the transport-owned fields of one enabled notifier
// entry using the same registry Build uses to select its constructor.
func ValidateEntry(entry map[string]any) []ValidationIssue {
	typ := cfgval.String(entry[KeyType])
	if typ == "" {
		return []ValidationIssue{{Field: KeyType, Suffix: " is required"}}
	}
	registered, ok := transports[typ]
	if !ok {
		return []ValidationIssue{{
			Field:  KeyType,
			Suffix: fmt.Sprintf(" %q is not supported (%s)", typ, strings.Join(SupportedTypes(), ", ")),
		}}
	}
	return registered.validate(entry)
}

func validateEmailConfig(entry map[string]any) []ValidationIssue {
	var issues []ValidationIssue
	dsn := cfgval.String(entry[KeyDSN])
	switch {
	case dsn == "":
		issues = append(issues, ValidationIssue{Field: KeyDSN, Suffix: " is required for an email notifier"})
	case !strings.HasPrefix(dsn, EmailDSNPrefixSMTP) && !strings.HasPrefix(dsn, EmailDSNPrefixSMTPS):
		issues = append(issues, ValidationIssue{Field: KeyDSN, Suffix: " must be an smtp:// or smtps:// URL"})
	}
	if cfgval.String(entry[KeyFrom]) == "" {
		issues = append(issues, ValidationIssue{Field: KeyFrom, Suffix: " is required for an email notifier"})
	}
	if !cfgval.IsNonEmptyStringList(entry[KeyTo]) {
		issues = append(issues, ValidationIssue{Field: KeyTo, Suffix: " must list at least one address"})
	}
	return issues
}

func validateGotifyConfig(entry map[string]any) []ValidationIssue {
	issues := validateWebhookConfig(TypeGotify)(entry)
	if cfgval.String(entry[KeyToken]) == "" {
		issues = append(issues, ValidationIssue{Field: KeyToken, Suffix: " is required for a gotify notifier"})
	}
	return issues
}

func validateNtfyConfig(entry map[string]any) []ValidationIssue {
	issues := validateWebhookConfig(TypeNtfy)(entry)
	if webhook := cfgval.String(entry[KeyWebhook]); webhook != "" {
		if _, _, err := ParseNtfyWebhook(webhook); err != nil {
			issues = append(issues, ValidationIssue{Field: KeyWebhook, Suffix: ": " + err.Error()})
		}
	}
	return issues
}

func validateTelegramConfig(entry map[string]any) []ValidationIssue {
	// An empty token (usually an unset secret) intentionally leaves the named
	// notifier inactive. Build emits its existing runtime warning.
	if cfgval.String(entry[KeyToken]) == "" {
		return nil
	}
	var issues []ValidationIssue
	if cfgval.String(entry[KeyChatID]) == "" {
		issues = append(issues, ValidationIssue{Field: KeyChatID, Suffix: " is required for a telegram notifier"})
	}
	if value, present := entry[KeyParseMode]; present {
		mode, ok := value.(string)
		if !ok || !telegramapi.ValidParseMode(mode) {
			issues = append(issues, ValidationIssue{
				Field:  KeyParseMode,
				Suffix: " must be one of " + strings.Join(telegramapi.ParseModes(), ", "),
			})
		}
	}
	if value, present := entry[KeySilent]; present {
		if _, ok := value.(bool); !ok {
			issues = append(issues, ValidationIssue{Field: KeySilent, Suffix: " must be a boolean"})
		}
	}
	if value, present := entry[KeyMessageThreadID]; present {
		if _, ok := cfgval.Int(value); !ok {
			issues = append(issues, ValidationIssue{Field: KeyMessageThreadID, Suffix: " must be an integer"})
		}
	}
	return issues
}

func validateWebhookConfig(typ string) func(map[string]any) []ValidationIssue {
	return func(entry map[string]any) []ValidationIssue {
		webhook := cfgval.String(entry[KeyWebhook])
		switch {
		case webhook == "":
			return []ValidationIssue{{Field: KeyWebhook, Suffix: " is required for a " + typ + " notifier"}}
		case !strings.HasPrefix(webhook, WebhookURLPrefixHTTP) && !strings.HasPrefix(webhook, WebhookURLPrefixHTTPS):
			return []ValidationIssue{{Field: KeyWebhook, Suffix: " must be an http(s) URL"}}
		default:
			return nil
		}
	}
}

func validateTTYConfig(entry map[string]any) []ValidationIssue {
	if users, present := entry[KeyUsers]; present && !cfgval.IsStringOrStringList(users) {
		return []ValidationIssue{{Field: KeyUsers, Suffix: " must be a string or list of strings"}}
	}
	return nil
}

func validateWallConfig(entry map[string]any) []ValidationIssue {
	if _, present := entry[KeyUsers]; present {
		return []ValidationIssue{{
			Field:  KeyUsers,
			Suffix: " is not supported for a wall notifier; use type tty to target specific users",
		}}
	}
	return nil
}
