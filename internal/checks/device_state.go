package checks

// Device-reported states exposed by SMART, md RAID and LVM watches. All but
// DeviceStateMissing describe an in-progress device operation, not a health
// verdict. DeviceStateMissing is the exception: it reports that the device the
// check addresses no longer answers, so it always accompanies an unavailable
// observation and reads as a failure in the dashboard.
const (
	DeviceStateTesting    = "testing"
	DeviceStateRecovering = "recovering"
	DeviceStateRebuilding = "rebuilding"
	DeviceStateRepairing  = "repairing"
	DeviceStateMoving     = "moving"
	DeviceStateMerging    = "merging"
	DeviceStateMissing    = "missing"
)
