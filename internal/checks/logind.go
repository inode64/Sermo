package checks

import (
	"context"
	"fmt"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/logind"
)

// logindNamesCheck counts and names what one reader of logind's on-disk
// records reports: the seated sessions a logind stop could disrupt, or the
// users a logind restart could leave without their manager.
//
// Both read the records, not login1 over D-Bus, so they answer while logind
// has lost the system bus, which is when its restart is needed. Condition-style
// — the `count` predicate (default `> 0`) decides — and meant as guard
// sensors: they report a risk to an action, they do not judge the service.
type logindNamesCheck struct {
	base
	checkType string
	noun      string
	dataKey   string
	op        string
	value     float64
	sampler   func() ([]string, error)
}

func (c logindNamesCheck) Run(_ context.Context) Result {
	start := time.Now()
	names, err := c.sampler()
	if err != nil {
		return c.unavailableResult(c.checkType+": "+err.Error(), start)
	}
	return c.namedCountResult(names, c.op, c.value, c.noun, c.dataKey, start)
}

func buildLogindNamesCheck(b base, entry map[string]any, checkType, noun, dataKey string, sampler func() ([]string, error)) (Check, string) {
	op, value, errs := optionalCountPred(entry, []string{DataKeyCount}, checkType+" check")
	if errs != "" {
		return nil, errs
	}
	return logindNamesCheck{base: b, checkType: checkType, noun: noun, dataKey: dataKey, op: op, value: value, sampler: sampler}, ""
}

// buildSeatedSessionsCheck reads the optional `class` filter: the logind
// session classes that count, every class when absent.
func buildSeatedSessionsCheck(b base, entry map[string]any) (Check, string) {
	classes, err := cfgval.StrictStringList(entry[CheckKeyClass])
	if err != nil {
		return nil, CheckTypeSeatedSessions + " check: class must be a string or a list of strings"
	}
	return buildLogindNamesCheck(b, entry, CheckTypeSeatedSessions, "seated session", DataKeySessions, func() ([]string, error) {
		return seatedSessionsSampler(classes)
	})
}

func buildUsersWithoutLingerCheck(b base, entry map[string]any) (Check, string) {
	return buildLogindNamesCheck(b, entry, CheckTypeUsersWithoutLinger, "unlingered user", DataKeyUsers, usersWithoutLingerSampler)
}

func seatedSessionsSampler(classes []string) ([]string, error) {
	sessions, err := logind.SeatedSessions("", classes...)
	if err != nil {
		return nil, fmt.Errorf("read logind sessions: %w", err)
	}
	return sessions, nil
}

func usersWithoutLingerSampler() ([]string, error) {
	users, err := logind.UsersWithoutLinger("", "")
	if err != nil {
		return nil, fmt.Errorf("read logind users: %w", err)
	}
	return users, nil
}
