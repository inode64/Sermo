package config

import (
	"sermo/internal/checks"
	"sermo/internal/metrics"
	"sermo/internal/rules"
)

// validateDBQueriesWatch validates a db_queries watch. It alerts once per
// matching statement by itself, so it takes no rule action; its only native
// action is the opt-in then.kill_query, valid on a service watch alone because
// the kill runs through the service's operation engine and guards.
func validateDBQueriesWatch(name string, check, entry map[string]any, service bool, defaultNotify []string, add addFunc) {
	validateStatefulWatchEntry(name, checks.CheckTypeDBQueries, entry, add)
	checkPath := watchCheckPath(name)
	cfg, err := checks.ParseDBQueryConfig(check)
	if err != nil {
		add("%s: %s", checkPath, err)
	}
	// A misspelt filter (exclude_user, database for databases) would silently
	// widen what the watch alerts on and may stop.
	for key := range unknownBlockKeys(check, dbQueriesCheckKeys) {
		add(validationNotSupportedFormat, watchCheckFieldPath(name, key))
	}
	if _, present := check[checks.CheckKeyLevels]; present {
		add("%s is not supported on a db_queries watch: each statement is graded by the watch's severity", watchCheckFieldPath(name, checks.CheckKeyLevels))
	}
	then, hasThen := entry[rules.RuleFieldThen].(map[string]any)
	if !hasThen {
		if _, present := entry[rules.RuleFieldThen]; present {
			add(validationMappingFormat, watchPath(name)+"."+rules.RuleFieldThen)
		}
		validateDBQueriesPolicy(name, entry, false, add)
		return
	}
	prefix := watchPath(name)
	if _, present := then[rules.RuleFieldAction]; present {
		add("%s is not supported on a db_queries watch: it alerts per matching statement by itself", thenFieldPath(prefix, rules.RuleFieldAction))
		return
	}
	// The shared then-block validator owns where kill_query is allowed (a
	// service watch); its grammar is the one checks.ParseDBQueryKill applies.
	validateHookBlock(prefix, entry, watchNativeActions{killQuery: service}, defaultNotify, add)
	raw, hasKill := then[WatchThenKeyKillQuery]
	if hasKill && service {
		validateKillQueryAction(name, raw, cfg, add)
	}
	validateDBQueriesPolicy(name, entry, hasKill && service, add)
}

func validateKillQueryAction(name string, raw any, cfg checks.DBQueryConfig, add addFunc) {
	path := thenFieldPath(watchPath(name), WatchThenKeyKillQuery)
	if spec, ok := raw.(map[string]any); ok {
		for key := range unknownBlockKeys(spec, killQueryKeys) {
			add(validationNotSupportedFormat, path+"."+key)
		}
	}
	if _, err := checks.ParseDBQueryKill(raw, cfg); err != nil {
		add("%s %s", path, err)
	}
}

// validateDBQueriesPolicy requires a positive cooldown with kill_query (a zero
// policy would allow a kill every cycle) and rejects a policy without it.
func validateDBQueriesPolicy(name string, entry map[string]any, hasKill bool, add addFunc) {
	_, present := entry[sectionPolicy]
	switch {
	case hasKill:
		prefix := watchPath(name)
		requireWatchCooldown(prefix, thenFieldPath(prefix, WatchThenKeyKillQuery), entry, "an automatic query kill must be paced", add)
	case present:
		add("%s is only valid with then.%s on a db_queries watch", watchFieldPath(name, sectionPolicy), WatchThenKeyKillQuery)
	}
}

// dbQueriesCheckKeys are a db_queries check's keys.
var dbQueriesCheckKeys = set(checks.CheckKeyType, checks.CheckKeyEngine, checks.CheckKeyHost, checks.CheckKeyPort,
	checks.CheckKeySocket, checks.CheckKeyUser, checks.CheckKeyPassword, checks.CheckKeyDatabase, checks.CheckKeyTLS,
	checks.CheckKeyDefaultsFile, checks.CheckKeyMinDuration, checks.CheckKeyUsers, checks.CheckKeyExcludeUsers,
	checks.CheckKeyDatabases, checks.CheckKeyExcludeDatabases, checks.CheckKeyStates, checks.CheckKeyMaxQueryLength,
	checks.CheckKeyMaxRows, checks.CheckKeyTimeout, checks.CheckKeySeverity, checks.CheckKeySummary, checks.CheckKeyLevels,
	metrics.MetricCPU, metrics.MetricCPUThread, metrics.MetricMemory)

// killQueryKeys are then.kill_query's keys; checks.ParseDBQueryKill reads them.
var killQueryKeys = set(checks.CheckKeyAfter, checks.CheckKeyMode, checks.CheckKeyUsers, checks.CheckKeyDatabases)
