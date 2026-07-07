package checks

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Sensor categories: the predicate fields and reported data keys of a sensors check.
const (
	sensorTemp    = "temp"
	sensorFan     = "fan"
	sensorVoltage = "voltage"
)

// sensorKindIn is the hwmon input-name prefix for voltage rails (inN_input).
// Unlike temp/fan, the hwmon kind ("in") differs from its predicate field and
// data key (sensorVoltage).
const sensorKindIn = "in"

// SensorReading is one hwmon input: the chip name, the kind (temp/fan/in), a
// label and the value in its natural unit (°C, RPM, V).
type SensorReading struct {
	Chip  string
	Kind  string // temp | fan | in
	Label string
	Value float64
}

// SensorValues is the aggregate view used by the sensors check: hottest
// temperature, slowest fan, and lowest voltage among the matching inputs.
type SensorValues struct {
	Temp       float64
	HasTemp    bool
	Fan        float64
	HasFan     bool
	Voltage    float64
	HasVoltage bool
	Count      int
}

// SensorSamplerFunc reads the current hardware sensor inputs. Injected for tests;
// the default reads /sys/class/hwmon.
type SensorSamplerFunc func() ([]SensorReading, error)

// sensorsCheck reads lm-sensors-style hwmon inputs and compares aggregates to
// thresholds (a level check: OK==true means a predicate holds, i.e. the alerting
// condition). `temp` is the hottest matching temperature (°C), `fan` the slowest
// matching fan (RPM, to catch a stalled fan) and `voltage` the lowest matching
// rail (V, to catch a brown-out). Optional `chip`/`label` substrings narrow which
// inputs are considered. Temperatures are recorded as a time series for graphing.
type sensorsCheck struct {
	base
	sampler SensorSamplerFunc
	chip    string
	label   string
	preds   []levelPred
}

func (c sensorsCheck) Run(_ context.Context) Result {
	start := time.Now()
	sampler := c.sampler
	if sampler == nil {
		sampler = defaultSensorSampler
	}
	readings, err := sampler()
	if err != nil {
		return c.result(false, "sensors: "+err.Error(), start)
	}

	summary := SummarizeSensors(readings, c.chip, c.label)
	values := sensorValueMap(summary)
	if len(values) == 0 {
		return c.result(false, "sensors: no matching inputs", start)
	}

	ok := levelPredsHold(c.preds, values)

	parts := make([]string, 0, 3)
	appendSensorPart := func(field string, value float64, ok bool) {
		if ok {
			parts = append(parts, fmt.Sprintf("%s=%.1f", field, value))
		}
	}
	appendSensorPart(sensorTemp, summary.Temp, summary.HasTemp)
	appendSensorPart(sensorFan, summary.Fan, summary.HasFan)
	appendSensorPart(sensorVoltage, summary.Voltage, summary.HasVoltage)
	r := c.result(ok, "sensors "+strings.Join(parts, " "), start)
	r.Data = map[string]any{}
	for k, v := range values {
		r.Data[k] = v
	}
	return r
}

// SummarizeSensors filters sensor readings by chip/label substring and returns
// the aggregate values evaluated by the sensors check.
func SummarizeSensors(readings []SensorReading, chip, label string) SensorValues {
	var temps, fans, volts []float64
	chip = strings.ToLower(chip)
	label = strings.ToLower(label)
	for _, r := range readings {
		if chip != "" && !strings.Contains(strings.ToLower(r.Chip), chip) {
			continue
		}
		if label != "" && !strings.Contains(strings.ToLower(r.Label), label) {
			continue
		}
		switch r.Kind {
		case sensorTemp:
			temps = append(temps, r.Value)
		case sensorFan:
			fans = append(fans, r.Value)
		case sensorKindIn:
			volts = append(volts, r.Value)
		}
	}
	values := SensorValues{Count: len(temps) + len(fans) + len(volts)}
	if len(temps) > 0 {
		values.Temp, values.HasTemp = maxFloat(temps), true
	}
	if len(fans) > 0 {
		values.Fan, values.HasFan = minFloat(fans), true
	}
	if len(volts) > 0 {
		values.Voltage, values.HasVoltage = minFloat(volts), true
	}
	return values
}

func sensorValueMap(summary SensorValues) map[string]float64 {
	values := map[string]float64{}
	if summary.HasTemp {
		values[sensorTemp] = summary.Temp
	}
	if summary.HasFan {
		values[sensorFan] = summary.Fan
	}
	if summary.HasVoltage {
		values[sensorVoltage] = summary.Voltage
	}
	return values
}

// defaultSensorSampler reads /sys/class/hwmon.
func defaultSensorSampler() ([]SensorReading, error) { return readHwmon(sysHwmonPath) }

// SampleSensors returns one live hardware-sensor observation using the default
// hwmon sampler.
func SampleSensors() ([]SensorReading, error) { return defaultSensorSampler() }

// readHwmon parses the hwmon tree at root into temperature (°C), fan (RPM) and
// voltage (V) readings.
func readHwmon(root string) ([]SensorReading, error) {
	dirs, err := filepath.Glob(filepath.Join(root, "hwmon*"))
	if err != nil {
		return nil, err
	}
	var out []SensorReading
	for _, d := range dirs {
		chip := readTrim(filepath.Join(d, "name"))
		out = append(out, readSensorKind(d, chip, sensorTemp, 1000)...)
		out = append(out, readSensorKind(d, chip, sensorFan, 1)...)
		out = append(out, readSensorKind(d, chip, sensorKindIn, 1000)...)
	}
	return out, nil
}

// readSensorKind reads every <kind>N_input under dir, scaled to its natural unit,
// labelled by the matching <kind>N_label (or chip/input name when unlabelled).
func readSensorKind(dir, chip, kind string, scale float64) []SensorReading {
	files, _ := filepath.Glob(filepath.Join(dir, kind+"[0-9]*_input"))
	var out []SensorReading
	for _, f := range files {
		v, err := strconv.ParseFloat(readTrim(f), 64)
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(f, "_input")
		label := readTrim(base + "_label")
		if label == "" {
			label = chip + "/" + filepath.Base(base)
		}
		out = append(out, SensorReading{Chip: chip, Kind: kind, Label: label, Value: v / scale})
	}
	return out
}

// readTrim reads a sysfs file and trims trailing whitespace, returning "" on error.
func readTrim(path string) string {
	b, err := os.ReadFile(path) //nolint:gosec // sysfs path derived from a fixed root
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func maxFloat(vs []float64) float64 {
	m := vs[0]
	for _, v := range vs[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

func minFloat(vs []float64) float64 {
	m := vs[0]
	for _, v := range vs[1:] {
		if v < m {
			m = v
		}
	}
	return m
}
