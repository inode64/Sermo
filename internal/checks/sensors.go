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

// SensorReading is one hwmon input: the chip name, the kind (temp/fan/in), a
// label and the value in its natural unit (°C, RPM, V).
type SensorReading struct {
	Chip  string
	Kind  string // temp | fan | in
	Label string
	Value float64
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

	var temps, fans, volts []float64
	for _, r := range readings {
		if c.chip != "" && !strings.Contains(strings.ToLower(r.Chip), strings.ToLower(c.chip)) {
			continue
		}
		if c.label != "" && !strings.Contains(strings.ToLower(r.Label), strings.ToLower(c.label)) {
			continue
		}
		switch r.Kind {
		case "temp":
			temps = append(temps, r.Value)
		case "fan":
			fans = append(fans, r.Value)
		case "in":
			volts = append(volts, r.Value)
		}
	}
	values := map[string]float64{}
	if len(temps) > 0 {
		values["temp"] = maxFloat(temps)
	}
	if len(fans) > 0 {
		values["fan"] = minFloat(fans)
	}
	if len(volts) > 0 {
		values["voltage"] = minFloat(volts)
	}
	if len(values) == 0 {
		return c.result(false, "sensors: no matching inputs", start)
	}

	ok := levelPredsHold(c.preds, values)

	parts := make([]string, 0, 3)
	for _, f := range []string{"temp", "fan", "voltage"} {
		if v, present := values[f]; present {
			parts = append(parts, fmt.Sprintf("%s=%.1f", f, v))
		}
	}
	r := c.result(ok, "sensors "+strings.Join(parts, " "), start)
	r.Data = map[string]any{}
	for k, v := range values {
		r.Data[k] = v
	}
	return r
}

// defaultSensorSampler reads /sys/class/hwmon.
func defaultSensorSampler() ([]SensorReading, error) { return readHwmon("/sys/class/hwmon") }

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
		out = append(out, readSensorKind(d, chip, "temp", 1000)...)
		out = append(out, readSensorKind(d, chip, "fan", 1)...)
		out = append(out, readSensorKind(d, chip, "in", 1000)...)
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
