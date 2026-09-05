package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
	"sermo/internal/output"
)

const (
	hardwareRAIDHealthOK         = "ok"
	hardwareRAIDHealthError      = "error"
	hardwareRAIDOperationRebuild = "rebuild"
	hardwareRAIDDetailDrive      = "drive"
	hardwareRAIDDetailVolume     = "volume"
	hardwareRAIDIssueLimit       = 3
	regexpCaptureMatchLen        = 2
	hardwareRAIDSizeFieldParts   = 2

	storCLIControllerArgs = "/call show all J"
	storCLIDrivesArgs     = "/call/eall/sall show all J"
	storCLIRebuildArgs    = "/call/eall/sall show rebuild J"
	storCLIVolumesArgs    = "/call/vall show all J"

	ssaCLIConfigDetailArgs = "ctrl all show config detail"
)

var (
	temperatureCPattern = regexp.MustCompile(`(?i)(-?\d+(?:\.\d+)?)\s*C(?:\b|\s|\()`)
	percentagePattern   = regexp.MustCompile(`(?i)(\d+(?:\.\d+)?)\s*%`)
	ssaCLISlotPattern   = regexp.MustCompile(`(?i)\bin slot\s+([^ ]+)`)
)

// HardwareRAIDControllerStatus is one controller returned by StorCLI or SSA
// CLI. MemoryBytes is the controller's on-board RAM; CacheBytes is the portion
// currently assigned to firmware/cache operation when the utility reports it.
type HardwareRAIDControllerStatus struct {
	ID           string
	Model        string
	SerialNumber string
	Firmware     string
	Interface    string
	Driver       string
	State        string
	MemoryBytes  uint64
	CacheBytes   uint64
	Temperature  float64
}

// HardwareRAIDCacheStatus is one controller cache or cache-protection module.
type HardwareRAIDCacheStatus struct {
	ID             string
	Controller     string
	Model          string
	State          string
	Protection     string
	SizeBytes      uint64
	AvailableBytes uint64
	Temperature    float64
}

// HardwareRAIDVolumeStatus is one hardware virtual/logical drive.
type HardwareRAIDVolumeStatus struct {
	ID          string
	Controller  string
	Array       string
	Name        string
	State       string
	RAIDLevel   string
	Access      string
	CachePolicy string
	OSDevice    string
	SizeBytes   uint64
	Operation   string
	ProgressPct float64
	HasProgress bool
}

// HardwareRAIDDriveStatus is one physical drive as observed by its controller.
// SMARTAlert is the controller passthrough verdict, not smartctl run against a
// virtual volume exposed to Linux.
type HardwareRAIDDriveStatus struct {
	ID                 string
	Controller         string
	State              string
	MediaType          string
	Interface          string
	Model              string
	SerialNumber       string
	Firmware           string
	SizeBytes          uint64
	Temperature        float64
	MediaErrors        int
	OtherErrors        int
	PredictiveFailures int
	SMARTAlert         bool
	Operation          string
	ProgressPct        float64
	HasProgress        bool
}

// hardwareRAIDCheck runs one vendor CLI in its read-only reporting mode. It
// represents controller hardware as a host watch: neither utility is a daemon,
// and no command in this check changes controller configuration or state.
type hardwareRAIDCheck struct {
	base
	runner execx.Runner
	binary string
	tool   string
	preds  []levelPred
}

type hardwareRAIDObservation struct {
	Controllers int
	Volumes     int
	Drives      int
	Enclosures  int
	Caches      int
	Batteries   int

	MediaErrors         int
	OtherErrors         int
	PredictiveFailures  int
	SMARTAlerts         int
	CorrectableErrors   int
	UncorrectableErrors int
	MaxTemperature      float64
	Operation           string
	ProgressPct         float64
	HasProgress         bool

	ControllerDetails []HardwareRAIDControllerStatus
	CacheDetails      []HardwareRAIDCacheStatus
	VolumeDetails     []HardwareRAIDVolumeStatus
	DriveDetails      []HardwareRAIDDriveStatus

	Issues []string
	seen   map[string]struct{}
}

func (c hardwareRAIDCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()

	var observation hardwareRAIDObservation
	var err error
	switch c.tool {
	case CheckTypeStorCLI:
		observation, err = c.runStorCLI(ctx)
	case CheckTypeSSACLI:
		observation, err = c.runSSACLI(ctx)
	default:
		err = fmt.Errorf("unsupported hardware RAID tool %q", c.tool)
	}
	if err != nil {
		return c.unavailableResult(c.tool+": "+err.Error(), run.start)
	}
	if observation.Controllers == 0 {
		observation.addIssue("no controller found")
	}
	if len(c.preds) > 0 && levelPredsHold(c.preds, map[string]float64{fieldTemperature: observation.MaxTemperature}) {
		observation.addIssue(fmt.Sprintf("maximum temperature %s exceeds configured threshold", formatCelsius(observation.MaxTemperature)))
	}

	health := hardwareRAIDHealthOK
	if len(observation.Issues) > 0 {
		health = hardwareRAIDHealthError
	}
	message := observation.message(c.tool, health)
	result := c.result(health == hardwareRAIDHealthOK, message, run.start)
	result.Data = observation.data(health)
	return result
}

func (c hardwareRAIDCheck) runStorCLI(ctx context.Context) (hardwareRAIDObservation, error) {
	controller, err := runHardwareRAIDCommand(ctx, c.runner, c.binary, strings.Fields(storCLIControllerArgs)...)
	if err != nil {
		return hardwareRAIDObservation{}, fmt.Errorf("controller report: %w", err)
	}
	drives, err := runHardwareRAIDCommand(ctx, c.runner, c.binary, strings.Fields(storCLIDrivesArgs)...)
	if err != nil {
		return hardwareRAIDObservation{}, fmt.Errorf("drive report: %w", err)
	}
	// Older StorCLI variants return a failing command status when no drive is
	// rebuilding. Rebuild progress enriches the observation but must not make an
	// otherwise valid controller unavailable, so this fourth read-only report is
	// deliberately optional.
	rebuild, _ := runHardwareRAIDCommand(ctx, c.runner, c.binary, strings.Fields(storCLIRebuildArgs)...)
	volumes, err := runHardwareRAIDCommand(ctx, c.runner, c.binary, strings.Fields(storCLIVolumesArgs)...)
	if err != nil {
		return hardwareRAIDObservation{}, fmt.Errorf("volume report: %w", err)
	}
	return parseStorCLIReports(controller, drives, volumes, rebuild)
}

func (c hardwareRAIDCheck) runSSACLI(ctx context.Context) (hardwareRAIDObservation, error) {
	stdout, err := runHardwareRAIDCommand(ctx, c.runner, c.binary, strings.Fields(ssaCLIConfigDetailArgs)...)
	if err != nil {
		return hardwareRAIDObservation{}, fmt.Errorf("configuration detail: %w", err)
	}
	return parseSSACLIReport(stdout)
}

func runHardwareRAIDCommand(ctx context.Context, runner execx.Runner, binary string, args ...string) (string, error) {
	result, runErr := runner.Run(ctx, binary, args...)
	if result.ExitCode == execx.ExitCodeRunFailure {
		return "", errors.New(execx.OperatorFailureOr(runErr, result, 0, execx.CommandDidNotStart))
	}
	if result.ExitCode != execx.ExitCodeSuccess {
		detail := output.FirstNonEmptyLine(result.Stderr)
		if detail == "" && runErr != nil {
			detail = runErr.Error()
		}
		if detail == "" {
			detail = "no diagnostic output"
		}
		return "", fmt.Errorf("exit %d: %s", result.ExitCode, detail)
	}
	return result.Stdout, nil
}

func (o *hardwareRAIDObservation) addIssue(issue string) {
	issue = strings.TrimSpace(issue)
	if issue == "" {
		return
	}
	if o.seen == nil {
		o.seen = make(map[string]struct{})
	}
	if _, exists := o.seen[issue]; exists {
		return
	}
	o.seen[issue] = struct{}{}
	o.Issues = append(o.Issues, issue)
}

func (o *hardwareRAIDObservation) addTemperature(value any) {
	temperature, ok := hardwareRAIDNumber(value)
	if !ok {
		return
	}
	if temperature > o.MaxTemperature {
		o.MaxTemperature = temperature
	}
}

func (o *hardwareRAIDObservation) noteOperation(operation string, progress float64, hasProgress bool) {
	operation = normalizedRAIDOperation(operation)
	if operation == "" {
		return
	}
	if o.Operation == "" {
		o.Operation = operation
	}
	if hasProgress && (!o.HasProgress || progress < o.ProgressPct) {
		o.ProgressPct = progress
		o.HasProgress = true
	}
}

func (o *hardwareRAIDObservation) message(tool, health string) string {
	message := fmt.Sprintf("%s: health=%s controllers=%d volumes=%d drives=%d caches=%d batteries=%d max_temperature=%s",
		tool, health, o.Controllers, o.Volumes, o.Drives, o.Caches, o.Batteries, formatCelsius(o.MaxTemperature))
	if len(o.Issues) > 0 {
		message += "; " + strings.Join(o.Issues[:min(len(o.Issues), hardwareRAIDIssueLimit)], "; ")
		if len(o.Issues) > hardwareRAIDIssueLimit {
			message += fmt.Sprintf("; and %d more", len(o.Issues)-hardwareRAIDIssueLimit)
		}
	}
	return message
}

func (o *hardwareRAIDObservation) data(health string) map[string]any {
	data := map[string]any{
		DataKeyHealth:                          health,
		DataKeyHardwareRAIDControllers:         o.Controllers,
		DataKeyHardwareRAIDVolumes:             o.Volumes,
		DataKeyHardwareRAIDDrives:              o.Drives,
		DataKeyHardwareRAIDEnclosures:          o.Enclosures,
		DataKeyHardwareRAIDCaches:              o.Caches,
		DataKeyHardwareRAIDBatteries:           o.Batteries,
		DataKeyHardwareRAIDMediaErrors:         o.MediaErrors,
		DataKeyHardwareRAIDOtherErrors:         o.OtherErrors,
		DataKeyHardwareRAIDPredictiveFailures:  o.PredictiveFailures,
		DataKeyHardwareRAIDSMARTAlerts:         o.SMARTAlerts,
		DataKeyHardwareRAIDCorrectableErrors:   o.CorrectableErrors,
		DataKeyHardwareRAIDUncorrectableErrors: o.UncorrectableErrors,
		SmartFieldTemperature:                  o.MaxTemperature,
		DataKeyHardwareRAIDIssues:              slices.Clone(o.Issues),
		DataKeyHardwareRAIDControllerDetails:   slices.Clone(o.ControllerDetails),
		DataKeyHardwareRAIDCacheDetails:        slices.Clone(o.CacheDetails),
		DataKeyHardwareRAIDVolumeDetails:       slices.Clone(o.VolumeDetails),
		DataKeyHardwareRAIDDriveDetails:        slices.Clone(o.DriveDetails),
	}
	if o.Operation != "" {
		data[DataKeyRaidOperation] = o.Operation
	}
	if o.HasProgress {
		data[DataKeyRaidProgressPct] = o.ProgressPct
	}
	return data
}

func formatCelsius(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64) + "C"
}

type storCLIEnvelope struct {
	Controllers []storCLIController
}

type storCLIController struct {
	CommandStatus storCLICommandStatus
	ResponseData  map[string]json.RawMessage
	ResponseRows  []map[string]any
}

type storCLICommandStatus struct {
	Controller  int
	Status      string
	Description string
}

func parseStorCLIReports(controllerJSON, drivesJSON, volumesJSON, rebuildJSON string) (hardwareRAIDObservation, error) {
	controller, err := parseStorCLIEnvelope("controller", controllerJSON)
	if err != nil {
		return hardwareRAIDObservation{}, err
	}
	drives, err := parseStorCLIEnvelope("drive", drivesJSON)
	if err != nil {
		return hardwareRAIDObservation{}, err
	}
	volumes, err := parseStorCLIEnvelope("volume", volumesJSON)
	if err != nil {
		return hardwareRAIDObservation{}, err
	}

	var observation hardwareRAIDObservation
	parseStorCLIControllers(controller, &observation)
	parseStorCLIDrives(drives, &observation)
	parseStorCLIVolumes(volumes, &observation)
	if strings.TrimSpace(rebuildJSON) != "" {
		if rebuild, parseErr := parseStorCLIEnvelope("rebuild", rebuildJSON); parseErr == nil {
			parseStorCLIRebuild(rebuild, &observation)
		}
	}
	return observation, nil
}

func parseStorCLIEnvelope(label, raw string) (storCLIEnvelope, error) {
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return storCLIEnvelope{}, fmt.Errorf("parse %s JSON: %w", label, err)
	}
	var controllers []map[string]json.RawMessage
	if value := document["Controllers"]; value != nil {
		if err := json.Unmarshal(value, &controllers); err != nil {
			return storCLIEnvelope{}, fmt.Errorf("parse %s controllers: %w", label, err)
		}
	}
	envelope := storCLIEnvelope{Controllers: make([]storCLIController, 0, len(controllers))}
	for _, rawController := range controllers {
		var command map[string]any
		if value := rawController["Command Status"]; value != nil {
			if err := json.Unmarshal(value, &command); err != nil {
				return storCLIEnvelope{}, fmt.Errorf("parse %s command status: %w", label, err)
			}
		}
		var responseData map[string]json.RawMessage
		var responseRows []map[string]any
		if value := rawController["Response Data"]; value != nil {
			if err := json.Unmarshal(value, &responseData); err != nil {
				if rowsErr := json.Unmarshal(value, &responseRows); rowsErr != nil {
					return storCLIEnvelope{}, fmt.Errorf("parse %s response data: %w", label, err)
				}
			}
		}
		envelope.Controllers = append(envelope.Controllers, storCLIController{
			CommandStatus: storCLICommandStatus{
				Controller:  integerValue(command["Controller"]),
				Status:      stringValue(command["Status"]),
				Description: stringValue(command["Description"]),
			},
			ResponseData: responseData,
			ResponseRows: responseRows,
		})
	}
	return envelope, nil
}

func parseStorCLIControllers(envelope storCLIEnvelope, observation *hardwareRAIDObservation) {
	observation.Controllers = len(envelope.Controllers)
	for _, controller := range envelope.Controllers {
		id := fmt.Sprintf("c%d", controller.CommandStatus.Controller)
		checkStorCLICommandStatus(id, controller.CommandStatus.Status, controller.CommandStatus.Description, observation)

		status := storCLIMap(controller.ResponseData["Status"])
		state := stringValue(status["Controller Status"])
		if !hardwareRAIDStateOK(state, "optimal", "ok") {
			observation.addIssue(fmt.Sprintf("controller %s state %s", id, stateOrUnknown(state)))
		}
		observation.CorrectableErrors += integerValue(status["Memory Correctable Errors"])
		observation.UncorrectableErrors += integerValue(status["Memory Uncorrectable Errors"])
		if count := integerValue(status["Memory Correctable Errors"]); count > 0 {
			observation.addIssue(fmt.Sprintf("controller %s correctable memory errors %d", id, count))
		}
		if count := integerValue(status["Memory Uncorrectable Errors"]); count > 0 {
			observation.addIssue(fmt.Sprintf("controller %s uncorrectable memory errors %d", id, count))
		}
		for key, message := range map[string]string{
			"Any Offline VD Cache Preserved":       "has offline virtual-drive cache preserved",
			"Controller has booted into safe mode": "booted into safe mode",
			"Controller shutdown required":         "requires shutdown",
		} {
			if yesValue(status[key]) {
				observation.addIssue("controller " + id + " " + message)
			}
		}

		basics := storCLIMap(controller.ResponseData["Basics"])
		version := storCLIMap(controller.ResponseData["Version"])
		hardware := storCLIMap(controller.ResponseData["HwCfg"])
		detail := HardwareRAIDControllerStatus{
			ID:           id,
			Model:        stringValue(basics["Model"]),
			SerialNumber: stringValue(basics["Serial Number"]),
			Firmware:     firstHardwareRAIDValue(version, "Firmware Version", "Firmware Package Build"),
			Interface:    firstHardwareRAIDValue(basics, "PCI Address", "Bus Interface"),
			Driver:       stringValue(version["Driver Name"]),
			State:        state,
			MemoryBytes:  hardwareRAIDBytes(hardware["On Board Memory Size"]),
			CacheBytes:   hardwareRAIDBytesWithUnit(hardware["Current Size of FW Cache (MB)"], "MB"),
		}
		for key, value := range hardware {
			if strings.Contains(strings.ToLower(key), "temperature") {
				observation.addTemperature(value)
				if temperature, ok := hardwareRAIDNumber(value); ok && temperature > detail.Temperature {
					detail.Temperature = temperature
				}
			}
		}
		observation.ControllerDetails = append(observation.ControllerDetails, detail)
		if detail.CacheBytes > 0 {
			observation.Caches++
			observation.CacheDetails = append(observation.CacheDetails, HardwareRAIDCacheStatus{
				ID:         id + "/firmware",
				Controller: id,
				State:      state,
				SizeBytes:  detail.CacheBytes,
			})
		}
		parseStorCLIStateList(id, "enclosure", controller.ResponseData["Enclosure LIST"], []string{"ok", "optimal"}, &observation.Enclosures, observation)
		parseStorCLIEnergyStores(id, "cache", controller.ResponseData["Cachevault_Info"], observation)
		parseStorCLIEnergyStores(id, "battery", controller.ResponseData["BBU_Info"], observation)
	}
}

func parseStorCLIDrives(envelope storCLIEnvelope, observation *hardwareRAIDObservation) {
	for _, controller := range envelope.Controllers {
		id := fmt.Sprintf("c%d", controller.CommandStatus.Controller)
		checkStorCLICommandStatus(id, controller.CommandStatus.Status, controller.CommandStatus.Description, observation)
		for key, raw := range controller.ResponseData {
			if !strings.HasPrefix(key, "Drive /") || strings.HasSuffix(key, " - Detailed Information") {
				continue
			}
			var rows []map[string]any
			if json.Unmarshal(raw, &rows) != nil {
				continue
			}
			for _, row := range rows {
				parseStorCLIDriveRow(id, strings.TrimPrefix(key, "Drive /"), row, observation)
			}
		}
		// Detailed attributes deliberately run after the summary table. Both are
		// JSON object members and therefore unordered; the detail section may omit
		// media type while still supplying the more precise raw capacity.
		for key, raw := range controller.ResponseData {
			if strings.HasSuffix(key, " - Detailed Information") {
				parseStorCLIDriveDetail(raw, observation)
			}
		}
	}
}

func parseStorCLIDriveRow(controller, drive string, row map[string]any, observation *hardwareRAIDObservation) {
	state := stringValue(row["State"])
	if !hardwareRAIDStateOK(state, "onln", "ugood", "ghs", "dhs", "jbod") {
		observation.addIssue(fmt.Sprintf("drive %s state %s", drive, stateOrUnknown(state)))
	}
	detail := observation.storCLIDrive(drive)
	detail.Controller = controller
	detail.State = state
	detail.MediaType = firstHardwareRAIDValue(row, "Med", "Media Type")
	detail.Interface = firstHardwareRAIDValue(row, "Intf", "Interface")
	detail.Model = stringValue(row["Model"])
	detail.SizeBytes = hardwareRAIDBytes(firstHardwareRAIDAny(row, "Size", "Raw size"))
}

func parseStorCLIDriveDetail(raw json.RawMessage, observation *hardwareRAIDObservation) {
	var sections map[string]json.RawMessage
	if json.Unmarshal(raw, &sections) != nil {
		return
	}
	for key, sectionRaw := range sections {
		suffix, supported := storCLIDriveSectionSuffix(key)
		if !supported {
			continue
		}
		driveID := strings.TrimSuffix(strings.TrimPrefix(key, "Drive /"), suffix)
		detail := observation.storCLIDrive(driveID)
		section := storCLIMap(sectionRaw)
		if suffix == " Device attributes" {
			detail.SerialNumber = firstHardwareRAIDValue(section, "SN", "Serial Number")
			detail.Model = firstHardwareRAIDValue(section, "Model Number", "Model")
			detail.Firmware = firstHardwareRAIDValue(section, "Firmware Revision", "Firmware")
			if mediaType := firstHardwareRAIDValue(section, "Media Type", "Med"); mediaType != "" {
				detail.MediaType = mediaType
			}
			if detail.Interface == "" {
				detail.Interface = firstHardwareRAIDValue(section, "Interface Type", "Device Speed")
			}
			detail.SizeBytes = hardwareRAIDBytes(firstHardwareRAIDAny(section, "Raw size", "Coerced size"))
			continue
		}
		mediaErrors := integerValue(section["Media Error Count"])
		otherErrors := integerValue(section["Other Error Count"])
		predictiveFailures := integerValue(section["Predictive Failure Count"])
		detail.MediaErrors = mediaErrors
		detail.OtherErrors = otherErrors
		detail.PredictiveFailures = predictiveFailures
		observation.MediaErrors += mediaErrors
		observation.OtherErrors += otherErrors
		observation.PredictiveFailures += predictiveFailures
		if mediaErrors > 0 {
			observation.addIssue(fmt.Sprintf("drive %s media errors %d", driveID, mediaErrors))
		}
		if otherErrors > 0 {
			observation.addIssue(fmt.Sprintf("drive %s other errors %d", driveID, otherErrors))
		}
		if predictiveFailures > 0 {
			observation.addIssue(fmt.Sprintf("drive %s predictive failures %d", driveID, predictiveFailures))
		}
		detail.SMARTAlert = yesValue(section["S.M.A.R.T alert flagged by drive"])
		if detail.SMARTAlert {
			observation.SMARTAlerts++
			observation.addIssue("drive " + driveID + " SMART alert")
		}
		if temperature, ok := hardwareRAIDNumber(section["Drive Temperature"]); ok {
			detail.Temperature = temperature
		}
		observation.addTemperature(section["Drive Temperature"])
	}
}

func parseStorCLIVolumes(envelope storCLIEnvelope, observation *hardwareRAIDObservation) {
	for _, controller := range envelope.Controllers {
		id := fmt.Sprintf("c%d", controller.CommandStatus.Controller)
		checkStorCLICommandStatus(id, controller.CommandStatus.Status, controller.CommandStatus.Description, observation)
		parseStorCLIControllerVolumes(id, controller.ResponseData, observation)
	}
}

func parseStorCLIControllerVolumes(controller string, response map[string]json.RawMessage, observation *hardwareRAIDObservation) {
	for key, raw := range response {
		if strings.HasPrefix(key, "/"+controller+"/v") {
			parseStorCLIVolumeRows(controller, key, raw, observation)
		}
	}
	for key, raw := range response {
		if strings.HasPrefix(key, "VD") && strings.HasSuffix(key, " Properties") {
			parseStorCLIVolumeProperties(controller, key, raw, observation)
		}
	}
}

func parseStorCLIVolumeRows(controller, key string, raw json.RawMessage, observation *hardwareRAIDObservation) {
	var rows []map[string]any
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
	for _, row := range rows {
		volumeID := strings.TrimPrefix(key, "/")
		state := stringValue(row["State"])
		access := stringValue(row["Access"])
		consistent := stringValue(row["Consist"])
		checkStorCLIVolumeState(volumeID, state, access, consistent, observation)
		array, _, _ := strings.Cut(stringValue(row["DG/VD"]), "/")
		detail := observation.storCLIVolume(volumeID)
		detail.Controller = controller
		detail.Array = array
		detail.Name = stringValue(row["Name"])
		detail.State = state
		detail.RAIDLevel = firstHardwareRAIDValue(row, "TYPE", "Type")
		detail.Access = access
		detail.CachePolicy = firstHardwareRAIDValue(row, "Cache", "Cac")
		detail.SizeBytes = hardwareRAIDBytes(row["Size"])
	}
}

func checkStorCLIVolumeState(volume, state, access, consistent string, observation *hardwareRAIDObservation) {
	if !hardwareRAIDStateOK(state, "optl", "optimal", "ok") {
		observation.addIssue(fmt.Sprintf("volume %s state %s", volume, stateOrUnknown(state)))
	}
	if access != "" && !hardwareRAIDStateOK(access, "rw") {
		observation.addIssue(fmt.Sprintf("volume %s access %s", volume, access))
	}
	if consistent != "" && !hardwareRAIDStateOK(consistent, "yes", "na", "n/a") {
		observation.addIssue(fmt.Sprintf("volume %s consistency %s", volume, consistent))
	}
}

func parseStorCLIVolumeProperties(controller, key string, raw json.RawMessage, observation *hardwareRAIDObservation) {
	volumeNumber := strings.TrimSuffix(strings.TrimPrefix(key, "VD"), " Properties")
	detail := observation.storCLIVolume(controller + "/v" + volumeNumber)
	properties := storCLIMap(raw)
	detail.OSDevice = stringValue(properties["OS Drive Name"])
	if cache := firstHardwareRAIDValue(properties, "Write Cache(initial setting)", "Disk Cache Policy"); cache != "" {
		detail.CachePolicy = cache
	}
	operation := stringValue(properties["Active Operations"])
	if hardwareRAIDStateOK(operation, "", "none", "n/a") {
		return
	}
	detail.Operation = normalizedRAIDOperation(operation)
	if progress, ok := hardwareRAIDPercentage(firstHardwareRAIDAny(properties, "Progress", "Progress%", "Percentage")); ok {
		detail.ProgressPct, detail.HasProgress = progress, true
	}
	observation.noteOperation(detail.Operation, detail.ProgressPct, detail.HasProgress)
}

func parseStorCLIRebuild(envelope storCLIEnvelope, observation *hardwareRAIDObservation) {
	for _, controller := range envelope.Controllers {
		parseStorCLIRebuildRows(controller, controller.ResponseRows, observation)
		for key, raw := range controller.ResponseData {
			if !strings.Contains(strings.ToLower(key), "rebuild") {
				continue
			}
			var rows []map[string]any
			if json.Unmarshal(raw, &rows) != nil {
				continue
			}
			parseStorCLIRebuildRows(controller, rows, observation)
		}
	}
}

func parseStorCLIRebuildRows(controller storCLIController, rows []map[string]any, observation *hardwareRAIDObservation) {
	for _, row := range rows {
		driveID := strings.TrimPrefix(firstHardwareRAIDValue(row, "Drive-ID", "Drive", "EID:Slt"), "/")
		if driveID == "" {
			continue
		}
		if !strings.HasPrefix(driveID, "c") {
			driveID = fmt.Sprintf("c%d/e%s", controller.CommandStatus.Controller, driveID)
		}
		progress, hasProgress := hardwareRAIDPercentage(firstHardwareRAIDAny(row, "Progress%", "Progress", "Percentage"))
		status := firstHardwareRAIDValue(row, "Status", "State")
		if hardwareRAIDStateOK(status, "", "not in progress", "none", "n/a") && !hasProgress {
			continue
		}
		detail := observation.storCLIDrive(driveID)
		detail.Operation = hardwareRAIDOperationRebuild
		detail.ProgressPct, detail.HasProgress = progress, hasProgress
		observation.noteOperation(detail.Operation, progress, hasProgress)
		issue := "drive " + driveID + " rebuild in progress"
		if hasProgress {
			issue += fmt.Sprintf(" %.1f%%", progress)
		}
		observation.addIssue(issue)
	}
}

func checkStorCLICommandStatus(controller, state, description string, observation *hardwareRAIDObservation) {
	if hardwareRAIDStateOK(state, "success") {
		return
	}
	issue := fmt.Sprintf("controller %s command state %s", controller, stateOrUnknown(state))
	if description != "" && !strings.EqualFold(description, "none") {
		issue += ": " + description
	}
	observation.addIssue(issue)
}

func parseStorCLIStateList(controller, kind string, raw json.RawMessage, healthy []string, count *int, observation *hardwareRAIDObservation) {
	var rows []map[string]any
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
	for index, row := range rows {
		(*count)++
		label := stringValue(row["EID"])
		if label == "" {
			label = strconv.Itoa(index)
		}
		state := stringValue(row["State"])
		if !hardwareRAIDStateOK(state, healthy...) {
			observation.addIssue(fmt.Sprintf("%s %s/%s state %s", kind, controller, label, stateOrUnknown(state)))
		}
	}
}

func parseStorCLIEnergyStores(controller, kind string, raw json.RawMessage, observation *hardwareRAIDObservation) {
	var rows []map[string]any
	if json.Unmarshal(raw, &rows) != nil {
		return
	}
	for index, row := range rows {
		// Both CacheVault and a BBU are cache-protection energy stores.
		observation.Batteries++
		label := stringValue(row["Model"])
		if label == "" {
			label = strconv.Itoa(index)
		}
		state := stringValue(row["State"])
		if state == "" {
			state = stringValue(row["Battery State"])
		}
		if !hardwareRAIDStateOK(state, "optimal", "ok", "ready", "operational") {
			observation.addIssue(fmt.Sprintf("%s %s/%s state %s", kind, controller, label, stateOrUnknown(state)))
		}
		cache, created := observation.ensureStorCLICache(controller)
		if cache == nil {
			continue
		}
		if created {
			observation.Caches++
		}
		cache.Model = label
		cache.Protection = kind
		cache.State = state
		for key, value := range row {
			if strings.Contains(strings.ToLower(key), "temp") {
				observation.addTemperature(value)
				if temperature, ok := hardwareRAIDNumber(value); ok && temperature > cache.Temperature {
					cache.Temperature = temperature
				}
			}
		}
	}
}

func storCLIMap(raw json.RawMessage) map[string]any {
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return value
}

type ssaCLIParser struct {
	observation     hardwareRAIDObservation
	controller      string
	array           string
	volume          string
	drive           string
	controllerIndex int
	cacheIndex      int
	volumeIndex     int
	driveIndex      int
}

func parseSSACLIReport(raw string) (hardwareRAIDObservation, error) {
	if strings.TrimSpace(raw) == "" {
		return hardwareRAIDObservation{}, errors.New("empty output")
	}
	parser := ssaCLIParser{controllerIndex: -1, cacheIndex: -1, volumeIndex: -1, driveIndex: -1}
	for line := range strings.SplitSeq(raw, "\n") {
		parser.parseLine(strings.TrimSpace(line))
	}
	if parser.observation.Controllers == 0 {
		return parser.observation, errors.New("no Smart Array controller in output")
	}
	return parser.observation, nil
}

func (p *ssaCLIParser) parseLine(line string) {
	if line == "" {
		return
	}
	lower := strings.ToLower(line)
	if strings.Contains(lower, "smart array") && !strings.Contains(line, ":") {
		p.controller = ssaCLIControllerLabel(line, p.observation.Controllers)
		model := line
		if before, _, found := strings.Cut(line, " in Slot"); found {
			model = strings.TrimSpace(before)
		}
		p.observation.ControllerDetails = append(p.observation.ControllerDetails, HardwareRAIDControllerStatus{ID: p.controller, Model: model})
		p.controllerIndex = len(p.observation.ControllerDetails) - 1
		p.observation.Controllers++
		p.array, p.volume, p.drive = "", "", ""
		p.cacheIndex, p.volumeIndex, p.driveIndex = -1, -1, -1
		return
	}
	if value, ok := afterPrefixFold(line, "Array:"); ok {
		p.array, p.volume, p.drive = value, "", ""
		return
	}
	if value, ok := afterPrefixFold(line, "HPE SmartCache Array:"); ok {
		p.array, p.volume, p.drive = value, "", ""
		return
	}
	if value, ok := afterPrefixFold(line, "Logical Drive:"); ok {
		p.volume, p.drive = value, ""
		p.observation.VolumeDetails = append(p.observation.VolumeDetails, HardwareRAIDVolumeStatus{
			ID: value, Controller: p.controller, Array: p.array,
		})
		p.volumeIndex = len(p.observation.VolumeDetails) - 1
		p.driveIndex = -1
		p.observation.Volumes++
		return
	}
	if strings.HasPrefix(lower, "physicaldrive ") && !strings.Contains(line, "(") {
		p.drive = strings.TrimSpace(line[len("physicaldrive "):])
		p.observation.DriveDetails = append(p.observation.DriveDetails, HardwareRAIDDriveStatus{
			ID: p.drive, Controller: p.controller,
		})
		p.driveIndex = len(p.observation.DriveDetails) - 1
		p.observation.Drives++
		return
	}
	key, value, found := strings.Cut(line, ":")
	if !found {
		return
	}
	p.parseField(strings.TrimSpace(key), strings.TrimSpace(value))
}

func (p *ssaCLIParser) parseField(key, value string) {
	lowerKey := strings.ToLower(key)
	if !p.parseControllerField(lowerKey, value) && !p.parseIdentityField(lowerKey, value) {
		p.parseHealthField(lowerKey, key, value)
	}
	p.parseTemperature(key, value)
}

func (p *ssaCLIParser) parseControllerField(key, value string) bool {
	switch key {
	case "bus interface":
		if controller := p.currentController(); controller != nil {
			controller.Interface = value
		}
	case "driver name":
		if controller := p.currentController(); controller != nil {
			controller.Driver = value
		}
	case "controller status":
		if controller := p.currentController(); controller != nil {
			controller.State = value
		}
		if !hardwareRAIDStateOK(value, "ok", "optimal") {
			p.observation.addIssue(fmt.Sprintf("controller %s state %s", controllerOrUnknown(p.controller), stateOrUnknown(value)))
		}
	case "cache status":
		cache := p.ensureSSACache()
		if cache != nil {
			cache.State = value
		}
		if !hardwareRAIDStateOK(value, "ok", "optimal", "not configured", "not present") {
			p.observation.addIssue(fmt.Sprintf("cache %s state %s", controllerOrUnknown(p.controller), stateOrUnknown(value)))
		}
	case "battery/capacitor status":
		p.observation.Batteries++
		if cache := p.currentCache(); cache != nil {
			cache.Protection = "battery/capacitor " + value
		}
		if !hardwareRAIDStateOK(value, "ok", "optimal", "not configured", "not present") {
			p.observation.addIssue(fmt.Sprintf("battery %s state %s", controllerOrUnknown(p.controller), stateOrUnknown(value)))
		}
	case "total cache size":
		size := hardwareRAIDBytesWithUnit(value, "GB")
		if controller := p.currentController(); controller != nil {
			controller.MemoryBytes = size
			controller.CacheBytes = size
		}
		if cache := p.currentCache(); cache != nil {
			cache.SizeBytes = size
		}
	case "total cache memory available":
		if cache := p.currentCache(); cache != nil {
			cache.AvailableBytes = hardwareRAIDBytesWithUnit(value, "GB")
		}
	default:
		return false
	}
	return true
}

func (p *ssaCLIParser) parseIdentityField(key, value string) bool {
	switch key {
	case "firmware version", "firmware revision":
		if drive := p.currentDrive(); drive != nil {
			drive.Firmware = value
		} else if controller := p.currentController(); controller != nil {
			controller.Firmware = value
		}
	case "serial number":
		if drive := p.currentDrive(); drive != nil {
			drive.SerialNumber = value
		} else if controller := p.currentController(); controller != nil {
			controller.SerialNumber = value
		}
	case "size":
		if drive := p.currentDrive(); drive != nil {
			drive.SizeBytes = hardwareRAIDBytes(value)
		} else if volume := p.currentVolume(); volume != nil {
			volume.SizeBytes = hardwareRAIDBytes(value)
		}
	case "fault tolerance":
		if volume := p.currentVolume(); volume != nil {
			volume.RAIDLevel = "RAID" + strings.TrimSpace(value)
		}
	case "disk name":
		if volume := p.currentVolume(); volume != nil {
			volume.OSDevice = value
		}
	case "logical drive label":
		if volume := p.currentVolume(); volume != nil {
			volume.Name = value
		}
	case "caching", "cache write policy":
		if volume := p.currentVolume(); volume != nil {
			volume.CachePolicy = value
		}
	case "drive type":
		p.parseDriveType(value)
	case "rotational speed":
		p.parseDriveRotation(value)
	case "interface type":
		p.parseDriveInterface(value)
	case "model":
		if drive := p.currentDrive(); drive != nil {
			drive.Model = strings.Join(strings.Fields(value), " ")
		}
	default:
		return false
	}
	return true
}

func (p *ssaCLIParser) parseDriveType(value string) {
	drive := p.currentDrive()
	if drive == nil {
		return
	}
	lowerValue := strings.ToLower(value)
	if strings.Contains(lowerValue, "ssd") || strings.Contains(lowerValue, "solid state") {
		drive.MediaType = "SSD"
	} else if strings.Contains(lowerValue, "hdd") {
		drive.MediaType = "HDD"
	}
}

func (p *ssaCLIParser) parseDriveRotation(value string) {
	if drive := p.currentDrive(); drive != nil && integerValue(value) > 0 {
		drive.MediaType = "HDD"
	}
}

func (p *ssaCLIParser) parseDriveInterface(value string) {
	drive := p.currentDrive()
	if drive == nil {
		return
	}
	drive.Interface = value
	if strings.Contains(strings.ToLower(value), "solid state") {
		drive.MediaType = "SSD"
	}
}

func (p *ssaCLIParser) parseHealthField(key, originalKey, value string) {
	switch key {
	case "status":
		p.parseStatus(value)
	case "unrecoverable media errors":
		if !hardwareRAIDStateOK(value, "none", "no", "0") {
			p.observation.MediaErrors++
			p.observation.addIssue(fmt.Sprintf("volume %s has unrecoverable media errors: %s", valueOrUnknown(p.volume), value))
		}
	case "ssd smart trip wearout":
		if drive := p.currentDrive(); drive != nil {
			drive.SMARTAlert = yesValue(value) || strings.EqualFold(value, "true")
		}
		if yesValue(value) || strings.EqualFold(value, "true") {
			p.observation.SMARTAlerts++
			p.observation.addIssue("drive " + valueOrUnknown(p.drive) + " SMART wearout")
		}
	case "drive authentication status":
		if !hardwareRAIDStateOK(value, "ok", "not supported") {
			p.observation.addIssue(fmt.Sprintf("drive %s authentication state %s", valueOrUnknown(p.drive), stateOrUnknown(value)))
		}
	case "cache write policy status", "lu cache state":
		if !hardwareRAIDStateOK(value, "ok", "good", "not configured", "not supported") {
			p.observation.addIssue(fmt.Sprintf("volume %s %s %s", valueOrUnknown(p.volume), key, value))
		}
	case "parity initialization status", "rebuild status", "transform status":
		p.parseOperation(originalKey, value)
		if !hardwareRAIDStateOK(value, "initialization completed", "completed", "none", "not required") {
			kind, id := "volume", valueOrUnknown(p.volume)
			if p.currentDrive() != nil {
				kind, id = "drive", valueOrUnknown(p.drive)
			}
			p.observation.addIssue(fmt.Sprintf("%s %s %s %s", kind, id, key, value))
		}
	}
}

func (p *ssaCLIParser) ensureSSACache() *HardwareRAIDCacheStatus {
	if cache := p.currentCache(); cache != nil {
		return cache
	}
	p.observation.CacheDetails = append(p.observation.CacheDetails, HardwareRAIDCacheStatus{
		ID: p.controller + "/cache", Controller: p.controller,
	})
	p.cacheIndex = len(p.observation.CacheDetails) - 1
	p.observation.Caches++
	return p.currentCache()
}

func (p *ssaCLIParser) parseStatus(value string) {
	if drive := p.currentDrive(); drive != nil {
		drive.State = value
	} else if volume := p.currentVolume(); volume != nil {
		volume.State = value
	}
	if progress, ok := hardwareRAIDPercentage(value); ok && strings.Contains(strings.ToLower(value), "rebuild") {
		if drive := p.currentDrive(); drive != nil {
			drive.Operation, drive.ProgressPct, drive.HasProgress = hardwareRAIDOperationRebuild, progress, true
			p.observation.noteOperation(hardwareRAIDOperationRebuild, progress, true)
		} else if volume := p.currentVolume(); volume != nil {
			volume.Operation, volume.ProgressPct, volume.HasProgress = hardwareRAIDOperationRebuild, progress, true
			p.observation.noteOperation(hardwareRAIDOperationRebuild, progress, true)
		}
	}
	if hardwareRAIDStateOK(value, "ok", "optimal") {
		return
	}
	switch {
	case p.drive != "":
		p.observation.addIssue(fmt.Sprintf("drive %s state %s", p.drive, stateOrUnknown(value)))
	case p.volume != "":
		p.observation.addIssue(fmt.Sprintf("volume %s state %s", p.volume, stateOrUnknown(value)))
	case p.array != "":
		p.observation.addIssue(fmt.Sprintf("array %s state %s", p.array, stateOrUnknown(value)))
	}
}

func (p *ssaCLIParser) parseTemperature(key, value string) {
	lower := strings.ToLower(key)
	if (strings.Contains(lower, "temperature") && strings.Contains(lower, "current")) ||
		strings.Contains(lower, "controller temperature") ||
		strings.Contains(lower, "cache module temperature") ||
		strings.Contains(lower, "capacitor temperature") {
		p.observation.addTemperature(value)
		temperature, ok := hardwareRAIDNumber(value)
		if !ok {
			return
		}
		switch {
		case strings.Contains(lower, "controller temperature"):
			if controller := p.currentController(); controller != nil && temperature > controller.Temperature {
				controller.Temperature = temperature
			}
		case strings.Contains(lower, "cache module temperature"), strings.Contains(lower, "capacitor temperature"):
			if cache := p.currentCache(); cache != nil && temperature > cache.Temperature {
				cache.Temperature = temperature
			}
		default:
			if drive := p.currentDrive(); drive != nil && temperature > drive.Temperature {
				drive.Temperature = temperature
			}
		}
	}
}

func (p *ssaCLIParser) parseOperation(key, value string) {
	if hardwareRAIDStateOK(value, "initialization completed", "completed", "none", "not required") {
		return
	}
	operation := normalizedRAIDOperation(strings.TrimSuffix(strings.ToLower(key), " status"))
	progress, hasProgress := hardwareRAIDPercentage(value)
	if drive := p.currentDrive(); drive != nil {
		drive.Operation = operation
		drive.ProgressPct, drive.HasProgress = progress, hasProgress
	} else if volume := p.currentVolume(); volume != nil {
		volume.Operation = operation
		volume.ProgressPct, volume.HasProgress = progress, hasProgress
	} else {
		return
	}
	p.observation.noteOperation(operation, progress, hasProgress)
}

// elementAt returns a pointer into items at index, or nil when the parser has
// not opened that section yet or the index ran past it.
func elementAt[T any](items []T, index int) *T {
	if index < 0 || index >= len(items) {
		return nil
	}
	return &items[index]
}

func (p *ssaCLIParser) currentController() *HardwareRAIDControllerStatus {
	return elementAt(p.observation.ControllerDetails, p.controllerIndex)
}

func (p *ssaCLIParser) currentCache() *HardwareRAIDCacheStatus {
	return elementAt(p.observation.CacheDetails, p.cacheIndex)
}

func (p *ssaCLIParser) currentVolume() *HardwareRAIDVolumeStatus {
	return elementAt(p.observation.VolumeDetails, p.volumeIndex)
}

func (p *ssaCLIParser) currentDrive() *HardwareRAIDDriveStatus {
	return elementAt(p.observation.DriveDetails, p.driveIndex)
}

func ssaCLIControllerLabel(line string, index int) string {
	if match := ssaCLISlotPattern.FindStringSubmatch(line); len(match) == regexpCaptureMatchLen {
		return "slot " + strings.Trim(match[1], "()")
	}
	return "controller " + strconv.Itoa(index)
}

func controllerOrUnknown(controller string) string {
	if controller == "" {
		return smartHealthUnknown
	}
	return controller
}

func valueOrUnknown(value string) string {
	if value == "" {
		return smartHealthUnknown
	}
	return value
}

func afterPrefixFold(value, prefix string) (string, bool) {
	if len(value) < len(prefix) || !strings.EqualFold(value[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(value[len(prefix):]), true
}

func storCLIDriveSectionSuffix(key string) (string, bool) {
	for _, suffix := range []string{" State", " Device attributes"} {
		if strings.HasSuffix(key, suffix) {
			return suffix, true
		}
	}
	return "", false
}

func (o *hardwareRAIDObservation) storCLIDrive(id string) *HardwareRAIDDriveStatus {
	index := o.ensureStorCLIDetail(hardwareRAIDDetailDrive, id)
	return &o.DriveDetails[index]
}

func (o *hardwareRAIDObservation) storCLIVolume(id string) *HardwareRAIDVolumeStatus {
	index := o.ensureStorCLIDetail(hardwareRAIDDetailVolume, id)
	return &o.VolumeDetails[index]
}

func (o *hardwareRAIDObservation) ensureStorCLIDetail(kind, id string) int {
	id = strings.TrimPrefix(strings.TrimSpace(id), "/")
	controller := hardwareRAIDParentID(id)
	if kind == hardwareRAIDDetailDrive {
		for index := range o.DriveDetails {
			if o.DriveDetails[index].ID == id {
				return index
			}
		}
		o.DriveDetails = append(o.DriveDetails, HardwareRAIDDriveStatus{ID: id, Controller: controller})
		o.Drives++
		return len(o.DriveDetails) - 1
	}
	for index := range o.VolumeDetails {
		if o.VolumeDetails[index].ID == id {
			return index
		}
	}
	o.VolumeDetails = append(o.VolumeDetails, HardwareRAIDVolumeStatus{ID: id, Controller: controller})
	o.Volumes++
	return len(o.VolumeDetails) - 1
}

func hardwareRAIDParentID(id string) string {
	parent, _, _ := strings.Cut(id, "/")
	return parent
}

func (o *hardwareRAIDObservation) ensureStorCLICache(controller string) (*HardwareRAIDCacheStatus, bool) {
	for index := range o.CacheDetails {
		if o.CacheDetails[index].Controller == controller {
			return &o.CacheDetails[index], false
		}
	}
	o.CacheDetails = append(o.CacheDetails, HardwareRAIDCacheStatus{
		ID: controller + "/firmware", Controller: controller,
	})
	return &o.CacheDetails[len(o.CacheDetails)-1], true
}

func firstHardwareRAIDAny(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, exists := values[key]; exists && stringValue(value) != "" {
			return value
		}
	}
	return nil
}

func firstHardwareRAIDValue(values map[string]any, keys ...string) string {
	return stringValue(firstHardwareRAIDAny(values, keys...))
}

func hardwareRAIDBytes(value any) uint64 {
	text := stringValue(value)
	if before, _, found := strings.Cut(text, "["); found {
		text = before
	}
	fields := strings.Fields(text)
	if len(fields) >= hardwareRAIDSizeFieldParts {
		text = fields[0] + fields[1]
	}
	bytes, ok := cfgval.ByteSize(text)
	if !ok {
		return 0
	}
	return bytes
}

func hardwareRAIDBytesWithUnit(value any, unit string) uint64 {
	text := stringValue(value)
	if text == "" {
		return 0
	}
	if _, ok := cfgval.ByteSize(text); !ok {
		text += unit
	}
	return hardwareRAIDBytes(text)
}

func hardwareRAIDPercentage(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, typed >= 0 && typed <= 100
	case int:
		return float64(typed), typed >= 0 && typed <= 100
	}
	match := percentagePattern.FindStringSubmatch(stringValue(value))
	if len(match) != regexpCaptureMatchLen {
		return 0, false
	}
	percentage, err := strconv.ParseFloat(match[1], 64)
	return percentage, err == nil && percentage >= 0 && percentage <= 100
}

func normalizedRAIDOperation(value string) string {
	normalized := strings.ToLower(strings.TrimSpace(value))
	switch {
	case normalized == "", normalized == "none", normalized == "n/a":
		return ""
	case strings.Contains(normalized, hardwareRAIDOperationRebuild):
		return hardwareRAIDOperationRebuild
	case strings.Contains(normalized, "reconstruct"):
		return "reconstruct"
	case strings.Contains(normalized, "parity") && strings.Contains(normalized, "initial"):
		return "parity_initialization"
	case strings.Contains(normalized, "transform"):
		return "transform"
	default:
		return strings.Join(strings.Fields(normalized), "_")
	}
}

func hardwareRAIDStateOK(state string, healthy ...string) bool {
	state = strings.TrimSpace(state)
	return slices.ContainsFunc(healthy, func(candidate string) bool { return strings.EqualFold(state, candidate) })
}

func stateOrUnknown(state string) string {
	if strings.TrimSpace(state) == "" {
		return smartHealthUnknown
	}
	return strings.TrimSpace(state)
}

// stringValue reads a scalar the RAID tools emit (a string, a JSON number or
// a decoded float) as trimmed text; anything else is absent.
func stringValue(value any) string {
	switch value.(type) {
	case string, float64, json.Number:
		return strings.TrimSpace(cfgval.String(value))
	default:
		return ""
	}
}

// hardwareRAIDNumber reads a numeric tool value, accepting the "62C"
// temperature form on top of the scalars cfgval.Float understands.
func hardwareRAIDNumber(value any) (float64, bool) {
	if text, ok := value.(string); ok {
		if match := temperatureCPattern.FindStringSubmatch(strings.TrimSpace(text)); len(match) == regexpCaptureMatchLen {
			number, err := strconv.ParseFloat(match[1], 64)
			return number, err == nil
		}
	}
	return cfgval.Float(value)
}

func integerValue(value any) int {
	number, ok := hardwareRAIDNumber(value)
	if !ok {
		return 0
	}
	return int(number)
}

func yesValue(value any) bool {
	text := stringValue(value)
	return strings.EqualFold(text, "yes") || strings.EqualFold(text, "true") || strings.EqualFold(text, "on")
}
