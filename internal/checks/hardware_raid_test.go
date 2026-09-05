package checks

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"sermo/internal/execx"
)

const healthyStorCLIController = `{
  "Controllers": [{
    "Command Status": {"Controller": 0, "Status": "Success", "Description": "None"},
    "Response Data": {
      "Status": {
        "Controller Status": "Optimal",
        "Memory Correctable Errors": 0,
        "Memory Uncorrectable Errors": 0,
        "Any Offline VD Cache Preserved": "No",
        "Controller has booted into safe mode": "No",
        "Controller shutdown required": "No"
      },
	  "Basics": {"Model": "MegaRAID 3108", "Serial Number": "CTRL-1", "PCI Address": "00:65:00:00"},
	  "Version": {"Firmware Version": "4.680", "Driver Name": "megaraid_sas"},
	  "HwCfg": {
		"ROC temperature(Degree Celsius)": 62,
		"On Board Memory Size": "2048MB",
		"Current Size of FW Cache (MB)": 1698
	  },
      "Enclosure LIST": [{"EID": 252, "State": "OK"}],
      "Cachevault_Info": [{"Model": "CVPM02", "State": "Optimal", "Temp": "29C"}]
    }
  }]
}`

const healthyStorCLIDrives = `{
  "Controllers": [{
    "Command Status": {"Controller": 0, "Status": "Success"},
    "Response Data": {
      "Drive /c0/e252/s0": [{"EID:Slt": "252:0", "State": "Onln", "Med": "SSD", "Size": "893.750 GB"}],
      "Drive /c0/e252/s0 - Detailed Information": {
        "Drive /c0/e252/s0 State": {
          "Media Error Count": 0,
          "Other Error Count": 0,
          "Drive Temperature": "44C (111.2 F)",
          "Predictive Failure Count": 0,
          "S.M.A.R.T alert flagged by drive": "No"
		},
		"Drive /c0/e252/s0 Device attributes": {
		  "SN": "DISK-1",
		  "Model Number": "MODEL-1",
		  "Firmware Revision": "FW-1",
		  "Raw size": "894.252 GB [0x6fc81ab0 Sectors]",
		  "Device Speed": "12.0Gb/s"
        }
      }
    }
  }]
}`

const healthyStorCLIVolumes = `{
  "Controllers": [{
    "Command Status": {"Controller": 0, "Status": "Success"},
    "Response Data": {
      "/c0/v0": [{"DG/VD": "0/0", "State": "Optl", "Access": "RW", "Consist": "Yes", "Cache": "RWBD"}],
	  "VD0 Properties": {
		"Exposed to OS": "Yes",
		"OS Drive Name": "/dev/sda",
		"Active Operations": "None",
		"Write Cache(initial setting)": "WriteBack"
	  }
    }
  }]
}`

func storCLIResults(controller, drives, volumes string) map[string]execx.Result {
	return map[string]execx.Result{
		"/usr/bin/storcli64 /call show all J":           {Stdout: controller},
		"/usr/bin/storcli64 /call/eall/sall show all J": {Stdout: drives},
		"/usr/bin/storcli64 /call/eall/sall show rebuild J": {
			Stdout: `{"Controllers":[{"Command Status":{"Controller":0,"Status":"Success"},"Response Data":[{"Drive-ID":"/c0/e252/s0","Progress%":"-","Status":"Not in progress"}]}]}`,
		},
		"/usr/bin/storcli64 /call/vall show all J": {Stdout: volumes},
	}
}

func TestStorCLICheckHealthy(t *testing.T) {
	runner := cliRunner(storCLIResults(healthyStorCLIController, healthyStorCLIDrives, healthyStorCLIVolumes), nil)
	check := hardwareRAIDCheck{
		name: CheckTypeStorCLI, timeout: time.Second, runner: runner,
		binary: "/usr/bin/storcli64", tool: CheckTypeStorCLI,
		preds: []levelPred{{field: SmartFieldTemperature, op: ">", value: 70}},
	}
	result := check.Run(context.Background())

	if !result.OK || result.Unavailable {
		t.Fatalf("healthy result = %+v", result)
	}
	if !runner.SawDeadline() {
		t.Fatal("StorCLI commands did not inherit the check deadline")
	}
	if got, want := len(runner.Lines()), 4; got != want {
		t.Fatalf("StorCLI calls = %d, want %d: %v", got, want, runner.Lines())
	}
	for key, want := range map[string]int{
		DataKeyHardwareRAIDControllers: 1,
		DataKeyHardwareRAIDVolumes:     1,
		DataKeyHardwareRAIDDrives:      1,
		DataKeyHardwareRAIDEnclosures:  1,
		DataKeyHardwareRAIDCaches:      1,
	} {
		if got := result.Data[key]; got != want {
			t.Errorf("data[%s] = %v, want %d", key, got, want)
		}
	}
	if got := result.Data[SmartFieldTemperature]; got != float64(62) {
		t.Errorf("max temperature = %v, want 62", got)
	}
	controllers := result.Data[DataKeyHardwareRAIDControllerDetails].([]HardwareRAIDControllerStatus)
	if got, want := controllers[0].MemoryBytes, uint64(2*1024*1024*1024); got != want {
		t.Errorf("controller memory = %d, want %d", got, want)
	}
	if got, want := controllers[0].CacheBytes, uint64(1698*1024*1024); got != want {
		t.Errorf("controller cache = %d, want %d", got, want)
	}
	drives := result.Data[DataKeyHardwareRAIDDriveDetails].([]HardwareRAIDDriveStatus)
	if got, want := drives[0].SerialNumber, "DISK-1"; got != want {
		t.Errorf("drive serial = %q, want %q", got, want)
	}
	if got, want := drives[0].MediaType, "SSD"; got != want {
		t.Errorf("drive media type = %q, want %q", got, want)
	}
	sizeGiB := 894.252
	if got, want := drives[0].SizeBytes, uint64(sizeGiB*float64(1024*1024*1024)); got != want {
		t.Errorf("drive size = %d, want %d", got, want)
	}
	volumes := result.Data[DataKeyHardwareRAIDVolumeDetails].([]HardwareRAIDVolumeStatus)
	if got, want := volumes[0].OSDevice, "/dev/sda"; got != want {
		t.Errorf("volume device = %q, want %q", got, want)
	}
}

func TestStorCLIRebuildProgressIsAttachedToItsDrive(t *testing.T) {
	results := storCLIResults(healthyStorCLIController, healthyStorCLIDrives, healthyStorCLIVolumes)
	results["/usr/bin/storcli64 /call/eall/sall show rebuild J"] = execx.Result{Stdout: `{
	  "Controllers": [{
		"Command Status": {"Controller": 0, "Status": "Success"},
		"Response Data": [{"Drive-ID": "/c0/e252/s0", "Progress%": 37, "Status": "In progress"}]
	  }]
	}`}
	result := (hardwareRAIDCheck{
		name: CheckTypeStorCLI, timeout: time.Second, runner: cliRunner(results, nil),
		binary: "/usr/bin/storcli64", tool: CheckTypeStorCLI,
	}).Run(context.Background())
	if result.OK || result.Unavailable {
		t.Fatalf("rebuilding result = %+v", result)
	}
	drive := result.Data[DataKeyHardwareRAIDDriveDetails].([]HardwareRAIDDriveStatus)[0]
	if drive.Operation != "rebuild" || !drive.HasProgress || drive.ProgressPct != 37 {
		t.Fatalf("drive rebuild = %+v", drive)
	}
	if got := result.Data[DataKeyRaidProgressPct]; got != float64(37) {
		t.Fatalf("aggregate rebuild progress = %v, want 37", got)
	}
}

func TestStorCLICheckReportsControllerVolumeDriveCacheSMARTAndTemperatureFailures(t *testing.T) {
	controller := strings.NewReplacer(
		`"Controller Status": "Optimal"`, `"Controller Status": "Degraded"`,
		`"Memory Uncorrectable Errors": 0`, `"Memory Uncorrectable Errors": 2`,
		`"State": "Optimal"`, `"State": "Degraded"`,
	).Replace(healthyStorCLIController)
	drives := strings.NewReplacer(
		`"State": "Onln"`, `"State": "Offln"`,
		`"Media Error Count": 0`, `"Media Error Count": 12`,
		`"Other Error Count": 0`, `"Other Error Count": 3`,
		`"Drive Temperature": "44C (111.2 F)"`, `"Drive Temperature": "74C (165.2 F)"`,
		`"Predictive Failure Count": 0`, `"Predictive Failure Count": 1`,
		`"S.M.A.R.T alert flagged by drive": "No"`, `"S.M.A.R.T alert flagged by drive": "Yes"`,
	).Replace(healthyStorCLIDrives)
	volumes := strings.NewReplacer(
		`"State": "Optl"`, `"State": "Dgrd"`,
		`"Access": "RW"`, `"Access": "Blocked"`,
		`"Consist": "Yes"`, `"Consist": "No"`,
	).Replace(healthyStorCLIVolumes)
	runner := cliRunner(storCLIResults(controller, drives, volumes), nil)
	check := hardwareRAIDCheck{
		name: CheckTypeStorCLI, timeout: time.Second, runner: runner,
		binary: "/usr/bin/storcli64", tool: CheckTypeStorCLI,
		preds: []levelPred{{field: SmartFieldTemperature, op: ">", value: 70}},
	}
	result := check.Run(context.Background())

	if result.OK || result.Unavailable {
		t.Fatalf("failed hardware result = %+v", result)
	}
	for _, want := range []string{
		"controller c0 state Degraded", "uncorrectable memory errors 2",
		"cache c0/CVPM02 state Degraded", "volume c0/v0 state Dgrd",
		"drive c0/e252/s0 state Offln", "media errors 12", "other errors 3",
		"predictive failures 1", "SMART alert", "temperature 74C",
	} {
		if !strings.Contains(result.Message, want) && !strings.Contains(strings.Join(result.Data[DataKeyHardwareRAIDIssues].([]string), "\n"), want) {
			t.Errorf("result does not report %q: %+v", want, result)
		}
	}
	if got := result.Data[DataKeyHardwareRAIDMediaErrors]; got != 12 {
		t.Errorf("media errors = %v, want 12", got)
	}
	if got := result.Data[DataKeyHardwareRAIDSMARTAlerts]; got != 1 {
		t.Errorf("SMART alerts = %v, want 1", got)
	}
}

const healthySSACLI = `
HPE Smart Array P816i-a SR Gen10 in Slot 0 (Embedded)
	Bus Interface: PCI
	Serial Number: CTRL-SSA
	Firmware Version: 4.11
	Controller Status: OK
	Cache Status: OK
	Total Cache Size: 2.0
	Total Cache Memory Available: 1.3
	Battery/Capacitor Status: OK
   Controller Temperature (C): 50
   Cache Module Temperature (C): 42
   Capacitor Temperature  (C): 39

   Array: A
      Status: OK
	  Logical Drive: 1
		 Size: 2.18 TB
		 Fault Tolerance: 1
		 Disk Name: /dev/sda
		 Logical Drive Label: system
		 Status: OK
		 Unrecoverable Media Errors: None
	  physicaldrive 1I:3:1
		 Status: OK
		 Drive Type: Data Drive
		 Interface Type: SAS
		 Rotational Speed: 10500
		 Size: 2.4 TB
		 Firmware Revision: HPD5
		 Serial Number: DISK-SSA
		 Model: HPE DRIVE
		 Current Temperature (C): 43
         SSD Smart Trip Wearout: False
         Drive Authentication Status: OK
`

func TestSSACLICheckHealthy(t *testing.T) {
	runner := cliRunner(map[string]execx.Result{
		"/usr/bin/ssacli ctrl all show config detail": {Stdout: healthySSACLI},
	}, nil)
	check := hardwareRAIDCheck{
		name: CheckTypeSSACLI, timeout: time.Second, runner: runner,
		binary: "/usr/bin/ssacli", tool: CheckTypeSSACLI,
		preds: []levelPred{{field: SmartFieldTemperature, op: ">", value: 70}},
	}
	result := check.Run(context.Background())

	if !result.OK || result.Unavailable {
		t.Fatalf("healthy result = %+v", result)
	}
	for key, want := range map[string]int{
		DataKeyHardwareRAIDControllers: 1,
		DataKeyHardwareRAIDVolumes:     1,
		DataKeyHardwareRAIDDrives:      1,
		DataKeyHardwareRAIDCaches:      1,
		DataKeyHardwareRAIDBatteries:   1,
	} {
		if got := result.Data[key]; got != want {
			t.Errorf("data[%s] = %v, want %d", key, got, want)
		}
	}
	if got := result.Data[SmartFieldTemperature]; got != float64(50) {
		t.Errorf("max temperature = %v, want 50", got)
	}
	controller := result.Data[DataKeyHardwareRAIDControllerDetails].([]HardwareRAIDControllerStatus)[0]
	if got, want := controller.MemoryBytes, uint64(2*1024*1024*1024); got != want {
		t.Errorf("controller memory = %d, want %d", got, want)
	}
	volume := result.Data[DataKeyHardwareRAIDVolumeDetails].([]HardwareRAIDVolumeStatus)[0]
	if volume.RAIDLevel != "RAID1" || volume.OSDevice != "/dev/sda" {
		t.Errorf("volume detail = %+v", volume)
	}
	drive := result.Data[DataKeyHardwareRAIDDriveDetails].([]HardwareRAIDDriveStatus)[0]
	if drive.Model != "HPE DRIVE" || drive.SerialNumber != "DISK-SSA" || drive.MediaType != "HDD" || drive.SMARTAlert {
		t.Errorf("drive detail = %+v", drive)
	}
}

func TestSSACLIRebuildProgressIsAttachedToItsVolume(t *testing.T) {
	output := strings.Replace(healthySSACLI, "Logical Drive Label: system", "Logical Drive Label: system\n         Rebuild Status: Rebuilding 42.5%", 1)
	observation, err := parseSSACLIReport(output)
	if err != nil {
		t.Fatal(err)
	}
	volume := observation.VolumeDetails[0]
	if volume.Operation != "rebuild" || !volume.HasProgress || volume.ProgressPct != 42.5 {
		t.Fatalf("volume rebuild = %+v", volume)
	}
}

func TestSSACLICheckReportsMediaCacheBatteryDriveSMARTAndTemperatureFailures(t *testing.T) {
	output := strings.NewReplacer(
		"Controller Status: OK", "Controller Status: Degraded",
		"Cache Status: OK", "Cache Status: Failed",
		"Battery/Capacitor Status: OK", "Battery/Capacitor Status: Failed",
		"Controller Temperature (C): 50", "Controller Temperature (C): 75",
		"Unrecoverable Media Errors: None", "Unrecoverable Media Errors: Detected",
		"SSD Smart Trip Wearout: False", "SSD Smart Trip Wearout: True",
		"Drive Authentication Status: OK", "Drive Authentication Status: Failed",
	).Replace(healthySSACLI)
	runner := cliRunner(map[string]execx.Result{
		"/usr/bin/ssacli ctrl all show config detail": {Stdout: output},
	}, nil)
	check := hardwareRAIDCheck{
		name: CheckTypeSSACLI, timeout: time.Second, runner: runner,
		binary: "/usr/bin/ssacli", tool: CheckTypeSSACLI,
		preds: []levelPred{{field: SmartFieldTemperature, op: ">", value: 70}},
	}
	result := check.Run(context.Background())

	if result.OK || result.Unavailable {
		t.Fatalf("failed hardware result = %+v", result)
	}
	issues := strings.Join(result.Data[DataKeyHardwareRAIDIssues].([]string), "\n")
	for _, want := range []string{
		"controller slot 0 state Degraded", "cache slot 0 state Failed",
		"battery slot 0 state Failed", "volume 1 has unrecoverable media errors",
		"drive 1I:3:1 SMART wearout", "authentication state Failed", "temperature 75C",
	} {
		if !strings.Contains(issues, want) {
			t.Errorf("issues do not report %q: %s", want, issues)
		}
	}
}

func TestHardwareRAIDCheckMakesCommandAndParseFailuresUnavailable(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		binary  string
		results map[string]execx.Result
		err     error
	}{
		{
			name: "command failure", tool: CheckTypeSSACLI, binary: "/usr/bin/ssacli",
			results: map[string]execx.Result{}, err: context.DeadlineExceeded,
		},
		{
			name: "malformed JSON", tool: CheckTypeStorCLI, binary: "/usr/bin/storcli64",
			results: storCLIResults("not-json", healthyStorCLIDrives, healthyStorCLIVolumes),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runner := cliRunner(test.results, test.err)
			result := (hardwareRAIDCheck{
				name: test.tool, timeout: time.Second, runner: runner,
				binary: test.binary, tool: test.tool,
			}).Run(context.Background())
			if result.OK || !result.Unavailable {
				t.Fatalf("result = %+v, want unavailable failure", result)
			}
		})
	}
}

func TestBuildHardwareRAIDCheckValidation(t *testing.T) {
	tests := []struct {
		name  string
		entry map[string]any
		want  string
	}{
		{name: "missing binary", entry: map[string]any{}, want: "requires an absolute binary"},
		{name: "relative binary", entry: map[string]any{CheckKeyBinary: "storcli64"}, want: "requires an absolute binary"},
		{name: "invalid temperature", entry: map[string]any{CheckKeyBinary: "/usr/bin/storcli64", SmartFieldTemperature: map[string]any{CheckKeyOp: ">", CheckKeyValue: "hot"}}, want: "not numeric"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, warning := buildHardwareRAIDCheck(base{}, test.entry, nil, CheckTypeStorCLI)
			if !strings.Contains(warning, test.want) {
				t.Fatalf("warning = %q, want substring %q", warning, test.want)
			}
		})
	}

	check, warning := buildHardwareRAIDCheck(base{}, map[string]any{
		CheckKeyBinary:        "/usr/bin/storcli64",
		SmartFieldTemperature: map[string]any{CheckKeyOp: ">", CheckKeyValue: 70},
	}, cliRunner(nil, nil), CheckTypeStorCLI)
	if warning != "" || check == nil {
		t.Fatalf("valid check = %T warning=%q", check, warning)
	}
}

func TestHardwareRAIDRunnerErrorIsReported(t *testing.T) {
	runner := cliRunner(nil, errors.New("permission denied"))
	result := (hardwareRAIDCheck{
		name: CheckTypeSSACLI, timeout: time.Second, runner: runner,
		binary: "/usr/bin/ssacli", tool: CheckTypeSSACLI,
	}).Run(context.Background())
	if !result.Unavailable || !strings.Contains(result.Message, "permission denied") {
		t.Fatalf("result = %+v", result)
	}
}
