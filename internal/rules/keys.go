package rules

// Rule and condition YAML keys. The rules package owns the runtime grammar; the
// config package reuses these constants when validating and desugaring rules so
// accepted YAML and runtime parsing cannot drift.
const (
	SectionRules      = "rules"
	SectionPolicy     = "policy"
	SectionRuleWindow = "rule_window"
)

// RuleField constants are keys inside one rule entry or its then/action block.
const (
	RuleFieldType    = "type"
	RuleFieldIf      = "if"
	RuleFieldThen    = "then"
	RuleFieldFor     = "for"
	RuleFieldWithin  = "within"
	RuleFieldBlocks  = "blocks"
	RuleFieldNotify  = "notify"
	RuleFieldActions = "actions"
	RuleFieldAction  = "action"
	RuleFieldMessage = "message"
)

// Condition constants are the recognized condition-tree operators.
const (
	ConditionAnd     = "and"
	ConditionOr      = "or"
	ConditionNot     = "not"
	ConditionFailed  = "failed"
	ConditionActive  = "active"
	ConditionFile    = "file"
	ConditionCommand = "command"
	ConditionService = "service"
	ConditionProcess = "process"
	ConditionMetric  = "metric"
	ConditionChanged = "changed"
)

// Field constants are reusable operand keys inside condition leaves and inline
// check entries produced from rules.
const (
	FieldApp     = "app"
	FieldCheck   = "check"
	FieldExe     = "exe"
	FieldExists  = "exists"
	FieldExpect  = "expect"
	FieldLevel   = "level"
	FieldLibrary = "library"
	FieldMetric  = "metric"
	FieldMode    = "mode"
	FieldName    = "name"
	FieldOp      = "op"
	FieldPath    = "path"
	FieldScope   = "scope"
	FieldState   = "state"
	FieldType    = "type"
	FieldUser    = "user"
	FieldValue   = "value"
)

// Window keys parsed from rule `for`, rule `within`, and `rule_window` blocks.
const (
	WindowKeyCycles       = "cycles"
	WindowKeyDuration     = "duration"
	WindowKeyMinMatches   = "min_matches"
	WindowModeConsecutive = "consecutive"
	WindowModeWithin      = "within"
)

// PolicyKey constants are keys inside a service or watch `policy` block.
const (
	PolicyKeyCooldown         = "cooldown"
	PolicyKeyMaxActions       = "max_actions"
	PolicyKeyMaxActionsWindow = "max_actions_window"
	PolicyKeyBackoff          = "backoff"
)

// BackoffKey constants are keys inside `policy.backoff`.
const (
	BackoffKeyInitial = "initial"
	BackoffKeyFactor  = "factor"
	BackoffKeyMax     = "max"
)

// ProcessStateRunning is the default process condition state.
const ProcessStateRunning = "running"
