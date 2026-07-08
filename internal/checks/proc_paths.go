package checks

const (
	procDiskstatsPath    = "/proc/diskstats"
	procEntropyAvailPath = "/proc/sys/kernel/random/entropy_avail"
	procFileNRPath       = "/proc/sys/fs/file-nr"
	procLoadavgPath      = "/proc/loadavg"
	procMDStatPath       = "/proc/mdstat"
	procMeminfoPath      = "/proc/meminfo"
	procMountsPath       = "/proc/mounts"
	procPidMaxPath       = "/proc/sys/kernel/pid_max"
	procPressureRootPath = "/proc/pressure"
	procRootPath         = "/proc"
	procVMStatPath       = "/proc/vmstat"
	sysBlockPath         = "/sys/class/block"
	sysEDACPath          = "/sys/devices/system/edac"
	sysHwmonPath         = "/sys/class/hwmon"
)

// ProcPressureRootPath is the Linux PSI pressure root directory.
const ProcPressureRootPath = procPressureRootPath

// SysBlockPath is Linux's sysfs block-device root directory.
const SysBlockPath = sysBlockPath
