package checks

// Result data-map keys shared by check implementations and consumers. These
// names are part of the event/web surface that Result.Data exposes.
const (
	DataKeyAllocated      = "allocated"
	DataKeyAddresses      = "addresses"
	DataKeyAvail          = "avail"
	DataKeyAvailableBytes = fieldAvailableBytes
	DataKeyBackend        = "backend"
	DataKeyBaselineBytes  = "baseline_bytes"
	DataKeyCount          = "count"
	DataKeyCurrentBytes   = "current_bytes"
	DataKeyDaysLeft       = "days_left"
	DataKeyDegradedArrays = "degraded_arrays"
	DataKeyDevice         = "device"
	DataKeyDNSNames       = "dns_names"
	DataKeyDatabase       = "database"
	DataKeyEngine         = "engine"
	DataKeyFamily         = "family"
	DataKeyFingerprint    = "fingerprint"
	DataKeyFingerprintOld = "fingerprint_old"
	DataKeyFreeBytes      = fieldFreeBytes
	DataKeyFSType         = "fstype"
	DataKeyGateway        = "gateway"
	DataKeyGrowthBytes    = "growth_bytes"
	DataKeyHealth         = "health"
	DataKeyHost           = fieldHost
	DataKeyInodesTotal    = "inodes_total"
	DataKeyInterface      = "interface"
	DataKeyInterfaces     = "interfaces"
	DataKeyIssuer         = "issuer"
	DataKeyKeyBits        = "key_bits"
	DataKeyKind           = "kind"
	DataKeyLanguage       = "language"
	DataKeyLatencyMS      = "latency_ms"
	DataKeyMax            = "max"
	DataKeyMinRules       = "min_rules"
	DataKeyMode           = "mode"
	DataKeyMounted        = "mounted"
	DataKeyMountpoints    = "mountpoints"
	DataKeyNotAfter       = "not_after"
	DataKeyNotBefore      = "not_before"
	DataKeyOf             = "of"
	DataKeyOp             = "op"
	DataKeyOptions        = "options"
	DataKeyOrg            = "org"
	DataKeyPages          = "pages"
	// DataKeyOutput carries bounded command/app stdout/stderr for event threading.
	DataKeyOutput             = "output"
	DataKeyPath               = "path"
	DataKeyPaths              = "paths"
	DataKeyPID                = "pid"
	DataKeyPIDs               = "pids"
	DataKeyPort               = fieldPort
	DataKeyProtocol           = "protocol"
	DataKeyPublicKeyAlgorithm = "public_key_algorithm"
	DataKeyQuery              = "query"
	DataKeyRecursive          = "recursive"
	DataKeyResult             = "result"
	DataKeyRoutes             = "routes"
	DataKeyRules              = "rules"
	DataKeySerialNumber       = "serial_number"
	DataKeySignatureAlgorithm = "signature_algorithm"
	DataKeySize               = "size"
	DataKeySocket             = "socket"
	DataKeySource             = "source"
	DataKeyStatus             = "status"
	DataKeySubject            = "subject"
	DataKeySubprotocol        = "subprotocol"
	DataKeyThreshold          = "threshold"
	DataKeyTotal              = fieldTotal
	DataKeyTotalBytes         = fieldTotalBytes
	DataKeyUsedBytes          = fieldUsedBytes
	DataKeyUsedPct            = fieldUsedPct
	DataKeyValue              = fieldValue
	DataKeyVersion            = "version"
	DataKeyVersionOld         = "version_old"
	DataKeyVersionShort       = "version_short"
	DataKeyWindow             = "window"
	DataKeyZombies            = "zombies"
)

// Result data-map source values.
const (
	DataSourceBackend = "backend"
)
