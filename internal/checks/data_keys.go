package checks

import "sermo/internal/conn"

// Result data-map keys shared by check implementations and consumers. These
// names are part of the event/web surface that Result.Data exposes.
const (
	DataKeyAllocated      = "allocated"
	DataKeyAddresses      = "addresses"
	DataKeyAvailablePct   = fieldAvailablePct
	DataKeyArrays         = fieldArrays
	DataKeyAvail          = "avail"
	DataKeyAvailableBytes = fieldAvailableBytes
	DataKeyBackend        = CheckKeyBackend
	DataKeyBaselineBytes  = "baseline_bytes"
	DataKeyCE             = fieldCE
	DataKeyChanged        = "changed"
	DataKeyChip           = CheckKeyChip
	DataKeyCount          = CheckKeyCount
	DataKeyCurrentBytes   = "current_bytes"
	DataKeyDaysLeft       = "days_left"
	DataKeyDegraded       = fieldDegraded
	DataKeyDegradedArrays = "degraded_arrays"
	DataKeyDevice         = CheckKeyDevice
	DataKeyDNSNames       = "dns_names"
	DataKeyDatabase       = CheckKeyDatabase
	DataKeyEngine         = CheckKeyEngine
	DataKeyEgress         = "egress"
	DataKeyFamily         = "family"
	DataKeyFan            = sensorFan
	DataKeyFingerprint    = "fingerprint"
	DataKeyFingerprintOld = "fingerprint_old"
	DataKeyFreeBytes      = fieldFreeBytes
	DataKeyFreePct        = fieldFreePct
	DataKeyFSType         = CheckKeyFSType
	DataKeyGateway        = "gateway"
	DataKeyGrowthBytes    = "growth_bytes"
	DataKeyHealth         = "health"
	DataKeyHost           = fieldHost
	DataKeyInodesTotal    = "inodes_total"
	DataKeyInodesFree     = fieldInodesFree
	DataKeyInodesFreePct  = fieldInodesFreePct
	DataKeyInodesUsedPct  = fieldInodesUsedPct
	DataKeyInputs         = "inputs"
	DataKeyInterface      = CheckKeyInterface
	DataKeyInterfaces     = "interfaces"
	DataKeyIssuer         = "issuer"
	DataKeyKeyBits        = "key_bits"
	DataKeyKind           = "kind"
	DataKeyLabel          = CheckKeyLabel
	DataKeyLanguage       = CheckKeyLanguage
	DataKeyLatencyMS      = "latency_ms"
	DataKeyLoad1          = fieldLoad1
	DataKeyLoad5          = fieldLoad5
	DataKeyLoad15         = fieldLoad15
	DataKeyMax            = "max"
	DataKeyMetric         = fieldMetric
	DataKeyMinRules       = CheckKeyMinRules
	DataKeyMode           = "mode"
	DataKeyMounted        = CheckKeyMounted
	DataKeyMountpoints    = "mountpoints"
	DataKeyNew            = fieldNew
	DataKeyNumCPU         = "num_cpu"
	DataKeyNotAfter       = "not_after"
	DataKeyNotBefore      = "not_before"
	DataKeyOf             = CheckKeyOf
	DataKeyOld            = fieldOld
	DataKeyOp             = CheckKeyOp
	DataKeyOptions        = CheckKeyOptions
	DataKeyOrg            = CheckKeyOrg
	DataKeyPages          = "pages"
	DataKeyPerCPU         = CheckKeyPerCPU
	// DataKeyOutput carries bounded command/app stdout/stderr for event threading.
	DataKeyOutput             = "output"
	DataKeyPath               = CheckKeyPath
	DataKeyPaths              = "paths"
	DataKeyPID                = "pid"
	DataKeyPIDs               = "pids"
	DataKeyPort               = fieldPort
	DataKeyPresent            = "present"
	DataKeyProtocol           = "protocol"
	DataKeyPublicKeyAlgorithm = "public_key_algorithm"
	DataKeyQuery              = CheckKeyQuery
	DataKeyRecursive          = CheckKeyRecursive
	DataKeyRecovering         = fieldRecovering
	DataKeyResult             = CheckKeyResult
	DataKeyResource           = CheckKeyResource
	DataKeyRoutes             = "routes"
	DataKeyRules              = CheckKeyRules
	DataKeySerialNumber       = "serial_number"
	DataKeySignatureAlgorithm = "signature_algorithm"
	DataKeySize               = CheckKeySize
	DataKeySocket             = conn.ExtraKeySocket
	DataKeySource             = "source"
	DataKeyStatus             = "status"
	DataKeySubject            = "subject"
	DataKeySubprotocol        = CheckKeySubprotocol
	DataKeyTemp               = sensorTemp
	DataKeyThreshold          = CheckKeyThreshold
	DataKeyTotal              = fieldTotal
	DataKeyTotalBytes         = fieldTotalBytes
	DataKeyUE                 = fieldUE
	DataKeyUsedBytes          = fieldUsedBytes
	DataKeyUsedPct            = fieldUsedPct
	DataKeyValue              = fieldValue
	DataKeyVersion            = "version"
	DataKeyVersionOld         = "version_old"
	DataKeyVersionShort       = "version_short"
	DataKeyVoltage            = sensorVoltage
	DataKeyWindow             = "window"
	DataKeyZombies            = "zombies"
)

// Pattern analyzer result data keys.
const (
	DataKeyPatternID       = "pattern_id"
	DataKeyPatternLine     = "pattern_line"
	DataKeyPatternSeverity = "pattern_severity"
)

// Result data-map source values.
const (
	DataSourceBackend = DataKeyBackend
)
