package checks

import "sermo/internal/conn"

// Result data-map keys shared by check implementations and consumers. These
// names are part of the event/web surface that Result.Data exposes.
const (
	DataKeyAllocated          = "allocated"
	DataKeyAge                = "age"
	DataKeyAddresses          = "addresses"
	DataKeyAttached           = "attached"
	DataKeyAvailablePct       = fieldAvailablePct
	DataKeyArrays             = fieldArrays
	DataKeyArray              = CheckKeyArray
	DataKeyAvail              = "avail"
	DataKeyAvailableBytes     = fieldAvailableBytes
	DataKeyBackend            = CheckKeyBackend
	DataKeyBaselineCount      = "baseline_count"
	DataKeyBaselineBytes      = "baseline_bytes"
	DataKeyChanged            = "changed"
	DataKeyChip               = CheckKeyChip
	DataKeyClockFailure       = "clock_failure"
	DataKeyCount              = CheckKeyCount
	DataKeyConnectedClients   = conn.ExtraKeyConnectedClients
	DataKeyCurrentBytes       = "current_bytes"
	DataKeyDaysLeft           = "days_left"
	DataKeyDBusAddress        = conn.ExtraKeyDBusAddress
	DataKeyDBusBusID          = conn.ExtraKeyDBusBusID
	DataKeyDBusBusName        = conn.ExtraKeyDBusBusName
	DataKeyDBusInterface      = conn.ExtraKeyDBusInterface
	DataKeyDBusObjectPath     = conn.ExtraKeyDBusObjectPath
	DataKeyDBusOwner          = conn.ExtraKeyDBusOwner
	DataKeyDBusProbe          = conn.ExtraKeyDBusProbe
	DataKeyDBusProperty       = conn.ExtraKeyDBusProperty
	DataKeyDBusPropertyValue  = conn.ExtraKeyDBusPropertyValue
	DataKeyDBusUniqueName     = conn.ExtraKeyDBusUniqueName
	DataKeyDegraded           = fieldDegraded
	DataKeyDetached           = "detached"
	DataKeyDegradedArrays     = "degraded_arrays"
	DataKeyDevice             = CheckKeyDevice
	DataKeyDNSNames           = "dns_names"
	DataKeyDatabase           = CheckKeyDatabase
	DataKeyEngine             = CheckKeyEngine
	DataKeyFamily             = "family"
	DataKeyFingerprint        = "fingerprint"
	DataKeyFingerprintOld     = "fingerprint_old"
	DataKeyFrequencyPPM       = "frequency_ppm"
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
	DataKeyLastOffsetSeconds  = "last_offset_seconds"
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
	DataKeyLVMTransition      = "lvm_transition"
	DataKeyLVMUsedBytes       = "vg_used_bytes"
	DataKeyLoad5              = fieldLoad5
	DataKeyLoad15             = fieldLoad15
	DataKeyMax                = "max"
	DataKeyMetric             = fieldMetric
	DataKeyMinRules           = CheckKeyMinRules
	DataKeyMode               = "mode"
	DataKeyMountSampleError   = "mount_sample_error"
	DataKeyModifiedAt         = "modified_at"
	DataKeyMounted            = CheckKeyMounted
	DataKeyMountPoint         = "mount_point"
	DataKeyMountpoints        = "mountpoints"
	DataKeyNew                = fieldNew
	DataKeyNumCPU             = "num_cpu"
	DataKeyNotAfter           = "not_after"
	DataKeyNotBefore          = "not_before"
	DataKeyOf                 = CheckKeyOf
	DataKeyOffsetAbsSeconds   = "offset_abs_seconds"
	DataKeyOffsetSeconds      = "offset_seconds"
	DataKeyOld                = fieldOld
	DataKeyOldestIdleSeconds  = "oldest_idle_seconds"
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
	DataKeyProtectedCount     = "protected_count"
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
	DataKeyReferenceAddress   = "reference_address"
	DataKeyReferenceAgeSecs   = "reference_age_seconds"
	DataKeyReferenceID        = "reference_id"
	DataKeyReferenceTime      = "reference_time"
	DataKeyResidualFreqPPM    = "residual_frequency_ppm"
	DataKeyResult             = CheckKeyResult
	DataKeyResource           = CheckKeyResource
	DataKeyRMSOffsetSeconds   = "rms_offset_seconds"
	DataKeyRootDelayMS        = "root_delay_ms"
	DataKeyRootDispersionMS   = "root_dispersion_ms"
	DataKeyRoutes             = "routes"
	DataKeyRules              = CheckKeyRules
	DataKeySampleError        = "sample_error"
	DataKeyServer             = "server"
	DataKeyTerminalSessions   = "terminal_sessions"
	DataKeySerialNumber       = "serial_number"
	DataKeySignatureAlgorithm = "signature_algorithm"
	DataKeySize               = CheckKeySize
	DataKeyScope              = CheckKeyScope
	DataKeySkewPPM            = "skew_ppm"
	DataKeySocket             = conn.ExtraKeySocket
	DataKeySource             = CheckKeySource
	DataKeySources            = "sources"
	DataKeySourcesOnline      = "sources_online"
	DataKeySourcesOffline     = "sources_offline"
	DataKeySourcesUnresolved  = "sources_unresolved"
	DataKeyStatus             = "status"
	DataKeyStratum            = "stratum"
	DataKeySubject            = "subject"
	DataKeySubprotocol        = CheckKeySubprotocol
	DataKeySynchronized       = "synchronized"
	DataKeyThreshold          = CheckKeyThreshold
	DataKeyTrigger            = "trigger"
	DataKeyTotal              = fieldTotal
	DataKeyTotalBytes         = fieldTotalBytes
	DataKeyType               = CheckKeyType
	DataKeyUnit               = "unit"
	DataKeyUpdateIntervalSecs = "update_interval_seconds"
	DataKeyUsedBytes          = fieldUsedBytes
	DataKeyUsedPct            = fieldUsedPct
	DataKeyValue              = fieldValue
	DataKeyVersion            = "version"
	DataKeyVersionOld         = "version_old"
	DataKeyVersionShort       = "version_short"
	DataKeyWindow             = "window"
	DataKeyNumberFiles        = "number_files"
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
