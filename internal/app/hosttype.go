package app

import (
	"os"
	"path/filepath"
	"strings"

	"sermo/internal/web"
)

const (
	appLineSeparator = "\n"

	dmiIDPath      = "/sys/class/dmi/id"
	hypervisorPath = "/sys/hypervisor/type"
	cpuInfoPath    = "/proc/cpuinfo"
)

const (
	dmiFieldSysVendor      = "sys_vendor"
	dmiFieldProductName    = "product_name"
	dmiFieldProductVersion = "product_version"
	dmiFieldBoardVendor    = "board_vendor"
	dmiFieldBIOSVendor     = "bios_vendor"
	dmiFieldChassisVendor  = "chassis_vendor"
)

const (
	hostTypeKindBareMetal      = "bare_metal"
	hostTypeKindUnknown        = "unknown"
	hostTypeKindVirtualMachine = "virtual_machine"
	hostTypeLabelBareMetal     = "bare metal"
	hostTypeLabelAlibabaCloud  = "Alibaba Cloud VM"
	hostTypeLabelAmazonEC2     = "Amazon EC2 VM"
	hostTypeLabelAppleVirtual  = "Apple Virtualization VM"
	hostTypeLabelBhyve         = "bhyve VM"
	hostTypeLabelDigitalOcean  = "DigitalOcean VM"
	hostTypeLabelGCE           = "Google Compute Engine VM"
	hostTypeLabelHyperV        = "Hyper-V VM"
	hostTypeLabelKVM           = "KVM/QEMU VM"
	hostTypeLabelOpenStack     = "OpenStack VM"
	hostTypeLabelOracleCloud   = "Oracle Cloud VM"
	hostTypeLabelParallels     = "Parallels VM"
	hostTypeLabelQEMU          = "QEMU VM"
	hostTypeLabelTencentCloud  = "Tencent Cloud VM"
	hostTypeLabelUnknown       = hostTypeKindUnknown
	hostTypeLabelVirtual       = "virtual machine"
	hostTypeLabelVirtualBox    = "VirtualBox VM"
	hostTypeLabelVMware        = "VMware VM"
	hostTypeLabelXen           = "Xen VM"
	hostTypePlatformAlibaba    = "alibaba_cloud"
	hostTypePlatformAmazonEC2  = "amazon_ec2"
	hostTypePlatformApple      = "apple_virtualization"
	hostTypePlatformBhyve      = "bhyve"
	hostTypePlatformDigital    = "digitalocean"
	hostTypePlatformGCE        = "gce"
	hostTypePlatformHyperV     = "hyperv"
	hostTypePlatformKVM        = "kvm"
	hostTypePlatformOpenStack  = "openstack"
	hostTypePlatformOracle     = "oracle_cloud"
	hostTypePlatformParallels  = "parallels"
	hostTypePlatformQEMU       = "qemu"
	hostTypePlatformTencent    = "tencent_cloud"
	hostTypePlatformVirtual    = "virtualized"
	hostTypePlatformVirtualBox = "virtualbox"
	hostTypePlatformVMware     = "vmware"
	hostTypePlatformXen        = "xen"
)

type hostTypeFact struct {
	key   string
	value string
}

func hostTypeInfo() *web.HostTypeInfo {
	info := detectHostType(os.ReadFile)
	return &info
}

func detectHostType(readFile func(string) ([]byte, error)) web.HostTypeInfo {
	if readFile == nil {
		readFile = os.ReadFile
	}

	facts := readDMIHostTypeFacts(readFile)
	if platform, label := virtualPlatformFromText(joinHostTypeFacts(facts)); platform != "" {
		return web.HostTypeInfo{
			Kind:     hostTypeKindVirtualMachine,
			Platform: platform,
			Label:    label,
			Detail:   hostTypeFactDetail(facts),
		}
	}

	if typ, ok := readHostTypeFile(readFile, hypervisorPath); ok && strings.EqualFold(typ, hostTypePlatformXen) {
		return web.HostTypeInfo{
			Kind:     hostTypeKindVirtualMachine,
			Platform: hostTypePlatformXen,
			Label:    hostTypeLabelXen,
			Detail:   hypervisorPath + "=" + hostTypePlatformXen,
		}
	}

	if cpuinfo, ok := readHostTypeFile(readFile, cpuInfoPath); ok {
		if platform, label := virtualPlatformFromCPU(cpuinfo); platform != "" {
			return web.HostTypeInfo{
				Kind:     hostTypeKindVirtualMachine,
				Platform: platform,
				Label:    label,
				Detail:   "CPU hypervisor vendor",
			}
		}
		if cpuHasHypervisorFlag(cpuinfo) {
			return web.HostTypeInfo{
				Kind:     hostTypeKindVirtualMachine,
				Platform: hostTypePlatformVirtual,
				Label:    hostTypeLabelVirtual,
				Detail:   "CPU hypervisor flag",
			}
		}
	}

	if len(facts) > 0 {
		return web.HostTypeInfo{
			Kind:   hostTypeKindBareMetal,
			Label:  hostTypeLabelBareMetal,
			Detail: hostTypeFactDetail(facts),
		}
	}

	return web.HostTypeInfo{Kind: hostTypeKindUnknown, Label: hostTypeLabelUnknown}
}

func readDMIHostTypeFacts(readFile func(string) ([]byte, error)) []hostTypeFact {
	files := []string{
		dmiFieldSysVendor,
		dmiFieldProductName,
		dmiFieldProductVersion,
		dmiFieldBoardVendor,
		dmiFieldBIOSVendor,
		dmiFieldChassisVendor,
	}
	facts := make([]hostTypeFact, 0, len(files))
	for _, name := range files {
		value, ok := readHostTypeFile(readFile, filepath.Join(dmiIDPath, name))
		if !ok || ignoredDMIValue(value) {
			continue
		}
		facts = append(facts, hostTypeFact{key: name, value: value})
	}
	return facts
}

func readHostTypeFile(readFile func(string) ([]byte, error), path string) (string, bool) {
	b, err := readFile(path)
	if err != nil {
		return "", false
	}
	value := strings.TrimSpace(string(b))
	return value, value != ""
}

func ignoredDMIValue(value string) bool {
	switch normalizeHostTypeText(value) {
	case "", "0", "none", "not specified", "default string", "to be filled by o e m",
		"system manufacturer", "system product name", "system version":
		return true
	default:
		return false
	}
}

func joinHostTypeFacts(facts []hostTypeFact) string {
	parts := make([]string, 0, len(facts))
	for _, fact := range facts {
		parts = append(parts, fact.value)
	}
	return strings.Join(parts, " ")
}

func hostTypeFactDetail(facts []hostTypeFact) string {
	if len(facts) == 0 {
		return ""
	}
	preferred := []string{dmiFieldSysVendor, dmiFieldProductName, dmiFieldProductVersion}
	seen := map[string]bool{}
	parts := make([]string, 0, len(preferred))
	for _, key := range preferred {
		for _, fact := range facts {
			if fact.key != key || seen[fact.value] {
				continue
			}
			seen[fact.value] = true
			parts = append(parts, fact.value)
		}
	}
	if len(parts) == 0 {
		for _, fact := range facts {
			if seen[fact.value] {
				continue
			}
			seen[fact.value] = true
			parts = append(parts, fact.value)
		}
	}
	return strings.Join(parts, " ")
}

func virtualPlatformFromText(text string) (string, string) {
	normalized := normalizeHostTypeText(text)
	for _, match := range []struct {
		needles  []string
		platform string
		label    string
	}{
		{[]string{"vmware"}, hostTypePlatformVMware, hostTypeLabelVMware},
		{[]string{"microsoft corporation virtual machine", "hyper v", "microsoft hv"}, hostTypePlatformHyperV, hostTypeLabelHyperV},
		{[]string{"virtualbox", "innotek"}, hostTypePlatformVirtualBox, hostTypeLabelVirtualBox},
		{[]string{"kvm", "qemu", "rhev", "ovirt", "bochs"}, hostTypePlatformKVM, hostTypeLabelKVM},
		{[]string{"xen"}, hostTypePlatformXen, hostTypeLabelXen},
		{[]string{"parallels"}, hostTypePlatformParallels, hostTypeLabelParallels},
		{[]string{"bhyve"}, hostTypePlatformBhyve, hostTypeLabelBhyve},
		{[]string{"amazon ec2"}, hostTypePlatformAmazonEC2, hostTypeLabelAmazonEC2},
		{[]string{"google compute engine"}, hostTypePlatformGCE, hostTypeLabelGCE},
		{[]string{"digitalocean"}, hostTypePlatformDigital, hostTypeLabelDigitalOcean},
		{[]string{"openstack"}, hostTypePlatformOpenStack, hostTypeLabelOpenStack},
		{[]string{"oracle cloud"}, hostTypePlatformOracle, hostTypeLabelOracleCloud},
		{[]string{"alibaba cloud"}, hostTypePlatformAlibaba, hostTypeLabelAlibabaCloud},
		{[]string{"tencent cloud"}, hostTypePlatformTencent, hostTypeLabelTencentCloud},
		{[]string{"apple virtualization", "virtualmac"}, hostTypePlatformApple, hostTypeLabelAppleVirtual},
		{[]string{hostTypeLabelVirtual}, hostTypePlatformVirtual, hostTypeLabelVirtual},
	} {
		for _, needle := range match.needles {
			if strings.Contains(normalized, needle) {
				return match.platform, match.label
			}
		}
	}
	return "", ""
}

func virtualPlatformFromCPU(cpuinfo string) (string, string) {
	normalized := normalizeHostTypeText(cpuinfo)
	for _, match := range []struct {
		needle   string
		platform string
		label    string
	}{
		{"kvmkvmkvm", hostTypePlatformKVM, hostTypeLabelKVM},
		{"microsoft hv", hostTypePlatformHyperV, hostTypeLabelHyperV},
		{"vmwarevmware", hostTypePlatformVMware, hostTypeLabelVMware},
		{"vboxvboxvbox", hostTypePlatformVirtualBox, hostTypeLabelVirtualBox},
		{"xenvmmxenvmm", hostTypePlatformXen, hostTypeLabelXen},
		{"bhyve bhyve", hostTypePlatformBhyve, hostTypeLabelBhyve},
		{"tcgtcgtcgtcg", hostTypePlatformQEMU, hostTypeLabelQEMU},
	} {
		if strings.Contains(normalized, match.needle) {
			return match.platform, match.label
		}
	}
	return "", ""
}

func cpuHasHypervisorFlag(cpuinfo string) bool {
	for _, line := range strings.Split(cpuinfo, appLineSeparator) {
		name, flags, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		switch strings.TrimSpace(strings.ToLower(name)) {
		case "flags", "features":
			for _, flag := range strings.Fields(flags) {
				if flag == "hypervisor" {
					return true
				}
			}
		}
	}
	return false
}

func normalizeHostTypeText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	replacer := strings.NewReplacer(
		"_", " ",
		"-", " ",
		".", " ",
		",", " ",
		"(", " ",
		")", " ",
		"/", " ",
	)
	s = replacer.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}
