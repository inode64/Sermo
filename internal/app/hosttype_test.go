package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDetectHostType(t *testing.T) {
	tests := []struct {
		name         string
		files        map[string]string
		wantKind     string
		wantPlatform string
		wantLabel    string
	}{
		{
			name: "kvm qemu from dmi",
			files: map[string]string{
				filepath.Join(dmiIDPath, "sys_vendor"):   "QEMU\n",
				filepath.Join(dmiIDPath, "product_name"): "Standard PC (Q35 + ICH9, 2009)\n",
			},
			wantKind:     "virtual_machine",
			wantPlatform: "kvm",
			wantLabel:    "KVM/QEMU VM",
		},
		{
			name: "hyper v from dmi",
			files: map[string]string{
				filepath.Join(dmiIDPath, "sys_vendor"):   "Microsoft Corporation\n",
				filepath.Join(dmiIDPath, "product_name"): "Virtual Machine\n",
			},
			wantKind:     "virtual_machine",
			wantPlatform: "hyperv",
			wantLabel:    "Hyper-V VM",
		},
		{
			name: "vmware from dmi",
			files: map[string]string{
				filepath.Join(dmiIDPath, "sys_vendor"):   "VMware, Inc.\n",
				filepath.Join(dmiIDPath, "product_name"): "VMware Virtual Platform\n",
			},
			wantKind:     "virtual_machine",
			wantPlatform: "vmware",
			wantLabel:    "VMware VM",
		},
		{
			name: "bare metal from physical dmi",
			files: map[string]string{
				filepath.Join(dmiIDPath, "sys_vendor"):   "Dell Inc.\n",
				filepath.Join(dmiIDPath, "product_name"): "PowerEdge R750\n",
			},
			wantKind:  "bare_metal",
			wantLabel: "bare metal",
		},
		{
			name: "kvm from cpu fallback",
			files: map[string]string{
				cpuInfoPath: "vendor_id\t: KVMKVMKVM\nflags\t: fpu hypervisor tsc\n",
			},
			wantKind:     "virtual_machine",
			wantPlatform: "kvm",
			wantLabel:    "KVM/QEMU VM",
		},
		{
			name: "generic virtualized from cpu flag",
			files: map[string]string{
				cpuInfoPath: "vendor_id\t: GenuineIntel\nflags\t: fpu hypervisor tsc\n",
			},
			wantKind:     "virtual_machine",
			wantPlatform: "virtualized",
			wantLabel:    "virtual machine",
		},
		{
			name:      "unknown without host signals",
			files:     map[string]string{},
			wantKind:  "unknown",
			wantLabel: "unknown",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detectHostType(fakeHostTypeReadFile(tt.files))
			if got.Kind != tt.wantKind || got.Platform != tt.wantPlatform || got.Label != tt.wantLabel {
				t.Fatalf("detectHostType = %+v, want kind=%q platform=%q label=%q", got, tt.wantKind, tt.wantPlatform, tt.wantLabel)
			}
		})
	}
}

func fakeHostTypeReadFile(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		value, ok := files[path]
		if !ok {
			return nil, os.ErrNotExist
		}
		return []byte(value), nil
	}
}
