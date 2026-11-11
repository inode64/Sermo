package checks

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// sysfsEntry writes one sysfs directory entry with the given attribute files.
func sysfsEntry(t *testing.T, root, name string, attrs map[string]string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, value := range attrs {
		writeFile(t, filepath.Join(dir, file), value+"\n")
	}
}

func TestReadUSBDevices(t *testing.T) {
	root := t.TempDir()
	// A root hub and an external hub: plumbing, never candidates.
	sysfsEntry(t, root, "usb3", map[string]string{"idVendor": "1d6b", "idProduct": "0003", "bDeviceClass": "09"})
	sysfsEntry(t, root, "3-4", map[string]string{"idVendor": "05e3", "idProduct": "0610", "bDeviceClass": "09"})
	// A disk, with the upper-case hex some firmware publishes.
	sysfsEntry(t, root, "3-2", map[string]string{
		"idVendor": "0480", "idProduct": "A00C", "bDeviceClass": "00",
		"manufacturer": "TOSHIBA", "product": "External USB 3.0", "serial": "20231012004242F",
	})
	// Its interface, which names a function of the same device.
	sysfsEntry(t, root, "3-2:1.0", map[string]string{"bInterfaceClass": "08"})
	// A device that names nothing but its IDs.
	sysfsEntry(t, root, "3-4.1", map[string]string{"idVendor": "0557", "idProduct": "2221", "bDeviceClass": "00"})
	// An entry mid-removal: the directory is there, its attributes are not.
	sysfsEntry(t, root, "3-5", nil)

	got, err := readUSBDevices(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []usbDevice{
		{Port: "3-2", ID: "0480:a00c", Manufacturer: "TOSHIBA", Product: "External USB 3.0", Serial: "20231012004242F"},
		{Port: "3-4.1", ID: "0557:2221"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("devices = %+v\nwant      %+v", got, want)
	}
}

func TestReadUSBDevicesWithoutBus(t *testing.T) {
	_, err := readUSBDevices(filepath.Join(t.TempDir(), "absent"))
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want fs.ErrNotExist so the check can say there is no bus", err)
	}
}

func TestUSBCheckRun(t *testing.T) {
	disk := usbDevice{Port: "3-2", ID: "0480:a00c", Manufacturer: "TOSHIBA", Product: "External USB 3.0", Serial: "A1"}
	spare := usbDevice{Port: "3-3", ID: "0480:a00c", Manufacturer: "TOSHIBA", Product: "External USB 3.0", Serial: "B2"}
	kvm := usbDevice{Port: "2-14", ID: "0557:2221"}
	tests := []struct {
		name            string
		id, serial      string
		devices         []usbDevice
		err             error
		wantOK          bool
		wantUnavailable bool
		wantMsg         string
		wantData        map[string]any
	}{
		{
			name: "matched by id", id: "0480:a00c", devices: []usbDevice{kvm, disk},
			wantOK: true, wantMsg: "USB device 0480:a00c TOSHIBA External USB 3.0 connected at 3-2",
			wantData: map[string]any{
				DataKeyCount: 2, DataKeyValue: 1, DataKeyDevice: "0480:a00c", DataKeyBusPort: "3-2",
				DataKeyModel: "TOSHIBA External USB 3.0", DataKeySerialNumber: "A1",
			},
		},
		{
			name: "serial picks one unit", id: "0480:a00c", serial: "B2", devices: []usbDevice{disk, spare},
			wantOK: true, wantMsg: "connected at 3-3",
		},
		{
			name: "serial alone", serial: "A1", devices: []usbDevice{kvm, disk},
			wantOK: true, wantMsg: "connected at 3-2",
		},
		{
			name: "the wrong unit is plugged in", id: "0480:a00c", serial: "B2", devices: []usbDevice{kvm, disk},
			wantMsg:  "no USB device 0480:a00c with serial B2 connected (2 other device(s))",
			wantData: map[string]any{DataKeyCount: 2, DataKeyValue: 0, DataKeyConnected: "0557:2221, 0480:a00c TOSHIBA External USB 3.0"},
		},
		{
			name: "nothing plugged in", id: "0480:a00c",
			wantMsg:  "no USB device 0480:a00c connected (0 other device(s))",
			wantData: map[string]any{DataKeyCount: 0, DataKeyValue: 0},
		},
		{
			name: "host without a USB bus", id: "0480:a00c", err: fs.ErrNotExist,
			wantUnavailable: true, wantMsg: "exposes no USB bus",
		},
		{
			name: "unreadable sysfs", id: "0480:a00c", err: errors.New("permission denied"),
			wantUnavailable: true, wantMsg: "permission denied",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check := usbCheck{
				name: "u", timeout: time.Second, id: tc.id, serial: tc.serial,
				sampler: func() ([]usbDevice, error) { return tc.devices, tc.err },
			}
			res := check.Run(context.Background())
			if res.OK != tc.wantOK || res.Unavailable != tc.wantUnavailable {
				t.Fatalf("OK = %v, Unavailable = %v; want %v, %v (%s)", res.OK, res.Unavailable, tc.wantOK, tc.wantUnavailable, res.Message)
			}
			if !strings.Contains(res.Message, tc.wantMsg) {
				t.Fatalf("message = %q, want substring %q", res.Message, tc.wantMsg)
			}
			if tc.wantData != nil && !reflect.DeepEqual(res.Data, tc.wantData) {
				t.Fatalf("data = %#v\nwant   %#v", res.Data, tc.wantData)
			}
		})
	}
}

func TestUSBDeviceLabel(t *testing.T) {
	for want, d := range map[string]usbDevice{
		"0480:a00c TOSHIBA External USB 3.0": {ID: "0480:a00c", Manufacturer: "TOSHIBA", Product: "External USB 3.0"},
		// A product that already starts with its maker is not named twice.
		"46f4:0001 QEMU USB HARDDRIVE": {ID: "46f4:0001", Manufacturer: "QEMU", Product: "QEMU USB HARDDRIVE"},
		"0bda:0329 USB3.0-CRW":         {ID: "0bda:0329", Product: "USB3.0-CRW"},
		"05e3:0610 Genesys":            {ID: "05e3:0610", Manufacturer: "Genesys"},
		"0557:2221":                    {ID: "0557:2221"},
	} {
		if got := d.label(); got != want {
			t.Errorf("label = %q, want %q", got, want)
		}
	}
}

func TestBuildUSBCheck(t *testing.T) {
	tests := []struct {
		name       string
		entry      map[string]any
		wantID     string
		wantSerial string
		wantErr    string
	}{
		{name: "id is normalized to sysfs spelling", entry: map[string]any{CheckKeyDevice: " 0480:A00C "}, wantID: "0480:a00c"},
		{name: "serial alone", entry: map[string]any{CheckKeySerialNumber: " A1 "}, wantSerial: "A1"},
		{name: "both", entry: map[string]any{CheckKeyDevice: "0557:2221", CheckKeySerialNumber: "A1"}, wantID: "0557:2221", wantSerial: "A1"},
		{name: "no selection", entry: map[string]any{}, wantErr: "requires device"},
		{name: "vendor only", entry: map[string]any{CheckKeyDevice: "0480"}, wantErr: "vendor:product"},
		{name: "not hexadecimal", entry: map[string]any{CheckKeyDevice: "048g:0900"}, wantErr: "vendor:product"},
		{name: "wrong separator", entry: map[string]any{CheckKeyDevice: "0480-a00c"}, wantErr: "vendor:product"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			check, errs := buildUSBCheck(base{name: "w"}, tc.entry)
			if tc.wantErr != "" {
				if !strings.Contains(errs, tc.wantErr) {
					t.Fatalf("errs = %q, want substring %q", errs, tc.wantErr)
				}
				return
			}
			if errs != "" {
				t.Fatalf("unexpected errs %q", errs)
			}
			built, ok := check.(usbCheck)
			if !ok {
				t.Fatalf("check type = %T, want usbCheck", check)
			}
			if built.id != tc.wantID || built.serial != tc.wantSerial {
				t.Fatalf("selection = %q / %q, want %q / %q", built.id, built.serial, tc.wantID, tc.wantSerial)
			}
		})
	}
}
