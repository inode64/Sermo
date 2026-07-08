package checks

import "sermo/internal/netutil"

// Check-entry YAML keys shared by builders, validators and web readers. These
// names are part of Sermo's public configuration surface.
const (
	CheckKeyAnalyze               = "analyze"
	CheckKeyAuthSource            = "auth_source"
	CheckKeyBackend               = "backend"
	CheckKeyBinary                = "binary"
	CheckKeyBody                  = "body"
	CheckKeyCertExpiresInDays     = "cert_expires_in_days"
	CheckKeyCertOnAlgorithmChange = "cert_on_algorithm_change"
	CheckKeyCertOnChange          = "cert_on_change"
	CheckKeyCertOnIssuerChange    = "cert_on_issuer_change"
	CheckKeyCertVerify            = "cert_verify"
	CheckKeyChange                = "change"
	CheckKeyChangeLevel           = "change_level"
	CheckKeyChip                  = "chip"
	CheckKeyColumn                = "column"
	CheckKeyCollection            = "collection"
	CheckKeyCommand               = "command"
	CheckKeyConnectTimeout        = "connect_timeout"
	CheckKeyContainer             = "container"
	CheckKeyCount                 = "count"
	CheckKeyCounters              = "counters"
	CheckKeyDatabase              = "database"
	CheckKeyDefault               = "default"
	CheckKeyDelta                 = "delta"
	CheckKeyDevice                = "device"
	CheckKeyDomain                = "domain"
	CheckKeyEngine                = "engine"
	CheckKeyExpect                = "expect"
	CheckKeyExpectBody            = "expect_body"
	CheckKeyExpectExit            = "expect_exit"
	CheckKeyExpectJSON            = "expect_json"
	CheckKeyExpectLatency         = "expect_latency"
	CheckKeyExpectStatus          = "expect_status"
	CheckKeyExpectStderr          = "expect_stderr"
	CheckKeyExpectStdout          = "expect_stdout"
	CheckKeyExe                   = "exe"
	CheckKeyExeAny                = "exe_any"
	CheckKeyExeDir                = "exe_dir"
	CheckKeyExistence             = "existence"
	CheckKeyExpiresInDays         = "expires_in_days"
	CheckKeyExport                = "export"
	CheckKeyFamily                = "family"
	CheckKeyFilter                = "filter"
	CheckKeyFor                   = "for"
	CheckKeyFrom                  = "from"
	CheckKeyFSType                = "fstype"
	CheckKeyGone                  = "gone"
	CheckKeyGrowBy                = "grow_by"
	CheckKeyHeaders               = "headers"
	CheckKeyHost                  = "host"
	CheckKeyHTTP3                 = "http3"
	CheckKeyID                    = "id"
	CheckKeyInterface             = "interface"
	CheckKeyInterfaceMatch        = "interface_match"
	CheckKeyJSON                  = "json"
	CheckKeyLabel                 = "label"
	CheckKeyLanguage              = "language"
	CheckKeyLeaseFile             = "lease_file"
	CheckKeyMAC                   = "mac"
	CheckKeyMatch                 = "match"
	CheckKeyMethod                = "method"
	CheckKeyMetric                = "metric"
	CheckKeyMinRules              = "min_rules"
	CheckKeyMounted               = "mounted"
	CheckKeyName                  = "name"
	CheckKeyOf                    = "of"
	CheckKeyOn                    = "on"
	CheckKeyOnAlgorithmChange     = "on_algorithm_change"
	CheckKeyOnChange              = "on_change"
	CheckKeyOnIssuerChange        = "on_issuer_change"
	CheckKeyOnVersionChange       = "on_version_change"
	CheckKeyOp                    = "op"
	CheckKeyOptional              = "optional"
	CheckKeyOptions               = "options"
	CheckKeyOrg                   = "org"
	CheckKeyOrigin                = "origin"
	CheckKeyOwner                 = "owner"
	CheckKeyPath                  = "path"
	CheckKeyPassword              = "password"
	CheckKeyPerCPU                = "per_cpu"
	CheckKeyPermissions           = "permissions"
	CheckKeyPipeline              = "pipeline"
	CheckKeyPort                  = "port"
	CheckKeyPorts                 = "ports"
	CheckKeyProxy                 = "proxy"
	CheckKeyQuery                 = "query"
	CheckKeyQuick                 = "quick"
	CheckKeyRecursive             = "recursive"
	CheckKeyRegex                 = "regex"
	CheckKeyResolvconf            = "resolvconf"
	CheckKeyResource              = "resource"
	CheckKeyResult                = "result"
	CheckKeyRequires              = "requires"
	CheckKeyRules                 = "rules"
	CheckKeyScope                 = "scope"
	CheckKeyServerName            = "server_name"
	CheckKeySeverity              = "severity"
	CheckKeySkipWhenChanged       = "skip_when_changed"
	CheckKeySize                  = "size"
	CheckKeySocket                = "socket"
	CheckKeyState                 = "state"
	CheckKeyStatusPath            = "status_path"
	CheckKeyStream                = "stream"
	CheckKeySubprotocol           = "subprotocol"
	CheckKeyThreshold             = "threshold"
	CheckKeyTimeout               = "timeout"
	CheckKeyTLS                   = "tls"
	CheckKeyToken                 = "token"
	CheckKeyTransport             = "transport"
	CheckKeyTrim                  = "trim"
	CheckKeyType                  = "type"
	CheckKeyURL                   = "url"
	CheckKeyUser                  = "user"
	CheckKeyUPS                   = "ups"
	CheckKeyValue                 = "value"
	CheckKeyVerify                = "verify"
	CheckKeyVersionMatch          = "version_match"
	CheckKeyWithin                = "within"
)

// LevelField suffixes classify storage-style threshold fields by value form.
const (
	LevelFieldSuffixBytes = "_bytes"
	LevelFieldSuffixPct   = "_pct"
)

// CommandDefaultExpectedExit is the implicit successful command exit code.
const CommandDefaultExpectedExit = 0

// ports check expect/match values.
const (
	PortStateOpen   = "open"
	PortStateClosed = "closed"
	PortExpectAny   = "any"
	PortMatchAll    = "all"
	PortMatchAny    = "any"
	PortMatchNone   = "none"
	// PortExpectSummary is the user-facing list of ports expect values.
	PortExpectSummary = PortStateOpen + ", " + PortStateClosed + " or " + PortExpectAny
	// PortMatchSummary is the user-facing list of ports match values.
	PortMatchSummary = PortMatchAll + ", " + PortMatchAny + " or " + PortMatchNone
)

// count check entry-kind values.
const (
	CountKindAny     = "any"
	CountKindFile    = "file"
	CountKindDir     = "dir"
	CountKindSymlink = "symlink"
	// CountKindSummary is the user-facing list of count entry-kind values.
	CountKindSummary = CountKindAny + ", " + CountKindFile + ", " + CountKindDir + " or " + CountKindSymlink
)

// version_match mapping keys.
const (
	VersionMatchKeyContains = "contains"
	VersionMatchKeyExcludes = "excludes"
	VersionMatchKeyRegex    = "regex"
	// VersionMatchKeySummary is the user-facing list of version_match keys.
	VersionMatchKeySummary = VersionMatchKeyContains + ", " + VersionMatchKeyExcludes + " or " + VersionMatchKeyRegex
)

// interface_match values.
const (
	InterfaceMatchAny = "any"
	InterfaceMatchAll = "all"
	// InterfaceMatchSummary is the user-facing list of interface_match modes.
	InterfaceMatchSummary = InterfaceMatchAny + " or " + InterfaceMatchAll
)

// URL schemes accepted by checks.
const (
	URLSchemeHTTP    = netutil.URLSchemeHTTP
	URLSchemeHTTPS   = netutil.URLSchemeHTTPS
	URLSchemeWS      = "ws"
	URLSchemeWSS     = "wss"
	URLSchemeSOCKS5  = "socks5"
	URLSchemeSOCKS5H = "socks5h"
	// WebsocketURLSchemeSummary is the user-facing list of accepted websocket URL schemes.
	WebsocketURLSchemeSummary = URLSchemeWS + ", " + URLSchemeWSS + ", " + URLSchemeHTTP + " or " + URLSchemeHTTPS
)
