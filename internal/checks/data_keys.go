package checks

import "sermo/internal/conn"

// Result data-map keys shared by check implementations and consumers. These
// names are part of the event/web surface that Result.Data exposes.
const (
	DataKeyAllocated          = "allocated"
	DataKeyAge                = "age"
	DataKeyAddresses          = "addresses"
	DataKeyAvailablePct       = fieldAvailablePct
	DataKeyArrays             = fieldArrays
	DataKeyArray              = CheckKeyArray
	DataKeyAvail              = "avail"
	DataKeyAvailableBytes     = fieldAvailableBytes
	DataKeyBackend            = CheckKeyBackend
	DataKeyBaselineCount      = "baseline_count"
	DataKeyBaselineBytes      = "baseline_bytes"
	DataKeyCE                 = fieldCE
	DataKeyChanged            = "changed"
	DataKeyChip               = CheckKeyChip
	DataKeyCount              = CheckKeyCount
	DataKeyCurrentBytes       = "current_bytes"
	DataKeyDaysLeft           = "days_left"
	DataKeyDegraded           = fieldDegraded
	DataKeyDegradedArrays     = "degraded_arrays"
	DataKeyDevice             = CheckKeyDevice
	DataKeyDNSNames           = "dns_names"
	DataKeyDatabase           = CheckKeyDatabase
	DataKeyEngine             = CheckKeyEngine
	DataKeyEgress             = "egress"
	DataKeyFamily             = "family"
	DataKeyFan                = sensorFan
	DataKeyFingerprint        = "fingerprint"
	DataKeyFingerprintOld     = "fingerprint_old"
	DataKeyFreeBytes          = fieldFreeBytes
	DataKeyFreePct            = fieldFreePct
	DataKeyFSType             = CheckKeyFSType
	DataKeyGateway            = "gateway"
	DataKeyGrowthBytes        = "growth_bytes"
	DataKeyGrowthCount        = "growth_count"
	DataKeyHealth             = "health"
	DataKeyHost               = fieldHost
	DataKeyInodesTotal        = "inodes_total"
	DataKeyInodesFree         = fieldInodesFree
	DataKeyInodesFreePct      = fieldInodesFreePct
	DataKeyInodesUsedPct      = fieldInodesUsedPct
	DataKeyInputs             = "inputs"
	DataKeyInterface          = CheckKeyInterface
	DataKeyInterfaces         = "interfaces"
	DataKeyIssuer             = "issuer"
	DataKeyKeyBits            = "key_bits"
	DataKeyKind               = "kind"
	DataKeyLabel              = CheckKeyLabel
	DataKeyLanguage           = CheckKeyLanguage
	DataKeyLatencyMS          = "latency_ms"
	DataKeyLeap               = "leap"
	DataKeyLoad1              = fieldLoad1
	DataKeyLVMReasons         = "lvm_reasons"
	DataKeyLogicalVolume      = CheckKeyLogicalVolume
	DataKeyVolumeGroup        = CheckKeyVolumeGroup
	DataKeyLVMFreeBytes       = "vg_free_bytes"
	DataKeyLVMFreePct         = "free_pct"
	DataKeyLVMSizeBytes       = "vg_size_bytes"
	DataKeyLVMThinDataPct     = "thin_data_pct"
	DataKeyLVMThinMetadataPct = "thin_metadata_pct"
	DataKeyLVMUsedBytes       = "vg_used_bytes"
	DataKeyLoad5              = fieldLoad5
	DataKeyLoad15             = fieldLoad15
	DataKeyMax                = "max"
	DataKeyMetric             = fieldMetric
	DataKeyMinRules           = CheckKeyMinRules
	DataKeyMode               = "mode"
	DataKeyModifiedAt         = "modified_at"
	DataKeyMounted            = CheckKeyMounted
	DataKeyMountpoints        = "mountpoints"
	DataKeyNew                = fieldNew
	DataKeyNumCPU             = "num_cpu"
	DataKeyNotAfter           = "not_after"
	DataKeyNotBefore          = "not_before"
	DataKeyOf                 = CheckKeyOf
	DataKeyOffsetAbsSeconds   = "offset_abs_seconds"
	DataKeyOffsetSeconds      = "offset_seconds"
	DataKeyOld                = fieldOld
	DataKeyOp                 = CheckKeyOp
	DataKeyOptions            = CheckKeyOptions
	DataKeyOrg                = CheckKeyOrg
	DataKeyPages              = "pages"
	DataKeyPerCPU             = CheckKeyPerCPU
	// DataKeyOutput carries bounded command/app stdout/stderr for event threading.
	DataKeyOutput             = "output"
	DataKeyPath               = CheckKeyPath
	DataKeyPaths              = "paths"
	DataKeyPID                = "pid"
	DataKeyPIDs               = "pids"
	DataKeyPort               = fieldPort
	DataKeyPresent            = "present"
	DataKeyProgressPct        = "progress_pct"
	DataKeyPrecisionSeconds   = "precision_seconds"
	DataKeyProtocol           = "protocol"
	DataKeyPublicKeyAlgorithm = "public_key_algorithm"
	DataKeyQuery              = CheckKeyQuery
	DataKeyRecursive          = CheckKeyRecursive
	DataKeyRecovering         = fieldRecovering
	DataKeyRaidMembers        = "raid_members"
	DataKeyRaidMismatchCount  = "raid_mismatch_cnt"
	DataKeyRaidOperation      = "raid_operation"
	DataKeyRaidProgressPct    = "raid_progress_pct"
	DataKeyRaidTransitions    = "raid_transitions"
	DataKeyReferenceID        = "reference_id"
	DataKeyResult             = CheckKeyResult
	DataKeyResource           = CheckKeyResource
	DataKeyRootDelayMS        = "root_delay_ms"
	DataKeyRootDispersionMS   = "root_dispersion_ms"
	DataKeyRoutes             = "routes"
	DataKeyRules              = CheckKeyRules
	DataKeyServer             = "server"
	DataKeySerialNumber       = "serial_number"
	DataKeySignatureAlgorithm = "signature_algorithm"
	DataKeySize               = CheckKeySize
	DataKeyScope              = CheckKeyScope
	DataKeySocket             = conn.ExtraKeySocket
	DataKeySource             = "source"
	DataKeyStatus             = "status"
	DataKeyStratum            = "stratum"
	DataKeySubject            = "subject"
	DataKeySubprotocol        = CheckKeySubprotocol
	DataKeyTemp               = sensorTemp
	DataKeyThreshold          = CheckKeyThreshold
	DataKeyTotal              = fieldTotal
	DataKeyTotalBytes         = fieldTotalBytes
	DataKeyType               = CheckKeyType
	DataKeyUE                 = fieldUE
	DataKeyUnit               = "unit"
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

// DataKeyDeviceState is an active, device-reported operation such as a SMART
// self-test or RAID/LVM recovery. It is distinct from check health.
const DataKeyDeviceState = "device_state"

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
