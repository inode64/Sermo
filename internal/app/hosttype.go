package app

import (
	"os"
	"path/filepath"
	"strings"

	"sermo/internal/web"
)

const (
	dmiIDPath      = "/sys/class/dmi/id"
	hypervisorPath = "/sys/hypervisor/type"
	cpuInfoPath    = "/proc/cpuinfo"
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
			Kind:     "virtual_machine",
			Platform: platform,
			Label:    label,
			Detail:   hostTypeFactDetail(facts),
		}
	}

	if typ, ok := readHostTypeFile(readFile, hypervisorPath); ok && strings.EqualFold(typ, "xen") {
		return web.HostTypeInfo{
			Kind:     "virtual_machine",
			Platform: "xen",
			Label:    "Xen VM",
			Detail:   "/sys/hypervisor/type=xen",
		}
	}

	if cpuinfo, ok := readHostTypeFile(readFile, cpuInfoPath); ok {
		if platform, label := virtualPlatformFromCPU(cpuinfo); platform != "" {
			return web.HostTypeInfo{
				Kind:     "virtual_machine",
				Platform: platform,
				Label:    label,
				Detail:   "CPU hypervisor vendor",
			}
		}
		if cpuHasHypervisorFlag(cpuinfo) {
			return web.HostTypeInfo{
				Kind:     "virtual_machine",
				Platform: "virtualized",
				Label:    "virtual machine",
				Detail:   "CPU hypervisor flag",
			}
		}
	}

	if len(facts) > 0 {
		return web.HostTypeInfo{
			Kind:   "bare_metal",
			Label:  "bare metal",
			Detail: hostTypeFactDetail(facts),
		}
	}

	return web.HostTypeInfo{Kind: "unknown", Label: "unknown"}
}

func readDMIHostTypeFacts(readFile func(string) ([]byte, error)) []hostTypeFact {
	files := []string{
		"sys_vendor",
		"product_name",
		"product_version",
		"board_vendor",
		"bios_vendor",
		"chassis_vendor",
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
	preferred := []string{"sys_vendor", "product_name", "product_version"}
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
		{[]string{"vmware"}, "vmware", "VMware VM"},
		{[]string{"microsoft corporation virtual machine", "hyper v", "microsoft hv"}, "hyperv", "Hyper-V VM"},
		{[]string{"virtualbox", "innotek"}, "virtualbox", "VirtualBox VM"},
		{[]string{"kvm", "qemu", "rhev", "ovirt", "bochs"}, "kvm", "KVM/QEMU VM"},
		{[]string{"xen"}, "xen", "Xen VM"},
		{[]string{"parallels"}, "parallels", "Parallels VM"},
		{[]string{"bhyve"}, "bhyve", "bhyve VM"},
		{[]string{"amazon ec2"}, "amazon_ec2", "Amazon EC2 VM"},
		{[]string{"google compute engine"}, "gce", "Google Compute Engine VM"},
		{[]string{"digitalocean"}, "digitalocean", "DigitalOcean VM"},
		{[]string{"openstack"}, "openstack", "OpenStack VM"},
		{[]string{"oracle cloud"}, "oracle_cloud", "Oracle Cloud VM"},
		{[]string{"alibaba cloud"}, "alibaba_cloud", "Alibaba Cloud VM"},
		{[]string{"tencent cloud"}, "tencent_cloud", "Tencent Cloud VM"},
		{[]string{"apple virtualization", "virtualmac"}, "apple_virtualization", "Apple Virtualization VM"},
		{[]string{"virtual machine"}, "virtualized", "virtual machine"},
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
		{"kvmkvmkvm", "kvm", "KVM/QEMU VM"},
		{"microsoft hv", "hyperv", "Hyper-V VM"},
		{"vmwarevmware", "vmware", "VMware VM"},
		{"vboxvboxvbox", "virtualbox", "VirtualBox VM"},
		{"xenvmmxenvmm", "xen", "Xen VM"},
		{"bhyve bhyve", "bhyve", "bhyve VM"},
		{"tcgtcgtcgtcg", "qemu", "QEMU VM"},
	} {
		if strings.Contains(normalized, match.needle) {
			return match.platform, match.label
		}
	}
	return "", ""
}

func cpuHasHypervisorFlag(cpuinfo string) bool {
	for _, line := range strings.Split(cpuinfo, "\n") {
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
