package checks

// Device-reported activity states exposed by SMART, md RAID and LVM watches.
// They describe an in-progress device operation, not a health verdict.
const (
	DeviceStateTesting    = "testing"
	DeviceStateRecovering = "recovering"
	DeviceStateRebuilding = "rebuilding"
	DeviceStateRepairing  = "repairing"
	DeviceStateMoving     = "moving"
	DeviceStateMerging    = "merging"
)
