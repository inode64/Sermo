package checks

// Result data-map keys shared by check implementations and consumers. These
// names are part of the event/web surface that Result.Data exposes.
const (
	DataKeyAllocated      = "allocated"
	DataKeyAvail          = "avail"
	DataKeyAvailableBytes = fieldAvailableBytes
	DataKeyBackend        = "backend"
	DataKeyBaselineBytes  = "baseline_bytes"
	DataKeyCount          = "count"
	DataKeyCurrentBytes   = "current_bytes"
	DataKeyDaysLeft       = "days_left"
	DataKeyDevice         = "device"
	DataKeyDNSNames       = "dns_names"
	DataKeyFingerprint    = "fingerprint"
	DataKeyFreeBytes      = fieldFreeBytes
	DataKeyFSType         = "fstype"
	DataKeyGrowthBytes    = "growth_bytes"
	DataKeyHealth         = "health"
	DataKeyHost           = fieldHost
	DataKeyInodesTotal    = "inodes_total"
	DataKeyInterfaces     = "interfaces"
	DataKeyIssuer         = "issuer"
	DataKeyKeyBits        = "key_bits"
	DataKeyKind           = "kind"
	DataKeyLatencyMS      = "latency_ms"
	DataKeyMax            = "max"
	DataKeyMinRules       = "min_rules"
	DataKeyMounted        = "mounted"
	DataKeyMountpoints    = "mountpoints"
	DataKeyNotAfter       = "not_after"
	DataKeyNotBefore      = "not_before"
	DataKeyOf             = "of"
	DataKeyOptions        = "options"
	// DataKeyOutput carries bounded command/app stdout/stderr for event threading.
	DataKeyOutput             = "output"
	DataKeyPath               = "path"
	DataKeyPaths              = "paths"
	DataKeyPID                = "pid"
	DataKeyPIDs               = "pids"
	DataKeyPort               = fieldPort
	DataKeyProtocol           = "protocol"
	DataKeyPublicKeyAlgorithm = "public_key_algorithm"
	DataKeyRecursive          = "recursive"
	DataKeyRules              = "rules"
	DataKeySerialNumber       = "serial_number"
	DataKeySignatureAlgorithm = "signature_algorithm"
	DataKeySize               = "size"
	DataKeySocket             = "socket"
	DataKeySource             = "source"
	DataKeyStatus             = "status"
	DataKeySubject            = "subject"
	DataKeySubprotocol        = "subprotocol"
	DataKeyTotal              = fieldTotal
	DataKeyTotalBytes         = fieldTotalBytes
	DataKeyUsedBytes          = fieldUsedBytes
	DataKeyUsedPct            = fieldUsedPct
	DataKeyValue              = fieldValue
	DataKeyVersion            = "version"
	DataKeyWindow             = "window"
	DataKeyZombies            = "zombies"
)

// Result data-map source values.
const (
	DataSourceBackend = "backend"
)
