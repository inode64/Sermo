package checks

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestReadHwmon(t *testing.T) {
	root := t.TempDir()
	d := filepath.Join(root, "hwmon0")
	if err := os.Mkdir(d, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(d, "name"), "coretemp\n")
	writeFile(t, filepath.Join(d, "temp1_input"), "45000\n") // 45 °C
	writeFile(t, filepath.Join(d, "temp1_label"), "Core 0\n")
	writeFile(t, filepath.Join(d, "fan1_input"), "1200\n") // RPM
	writeFile(t, filepath.Join(d, "in1_input"), "3300\n")  // 3.3 V

	readings, err := readHwmon(root)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]float64{}
	for _, r := range readings {
		got[r.Kind] = r.Value
	}
	if got["temp"] != 45 || got["fan"] != 1200 || got["in"] != 3.3 {
		t.Fatalf("readings = %v, want temp 45 fan 1200 in 3.3", got)
	}
}

func sensorsWith(readings []SensorReading, chip, label string, preds ...sensorPred) sensorsCheck {
	return sensorsCheck{
		base:    base{name: "s", timeout: time.Second},
		sampler: func() ([]SensorReading, error) { return readings, nil },
		chip:    chip, label: label, preds: preds,
	}
}

func TestSensorsAggregatesAndThresholds(t *testing.T) {
	readings := []SensorReading{
		{Chip: "coretemp", Kind: "temp", Label: "Core 0", Value: 70},
		{Chip: "coretemp", Kind: "temp", Label: "Core 1", Value: 85}, // hottest
		{Chip: "nct6775", Kind: "fan", Label: "fan1", Value: 1500},
		{Chip: "nct6775", Kind: "fan", Label: "fan2", Value: 300}, // slowest
		{Chip: "nct6775", Kind: "in", Label: "Vcore", Value: 1.1},
	}
	// temp aggregate is the max -> alert when > 80.
	c := sensorsWith(readings, "", "", sensorPred{"temp", ">", 80})
	res := c.Run(context.Background())
	if !res.OK {
		t.Errorf("temp max 85 > 80 should meet the alert condition: %s", res.Message)
	}
	if res.Data["temp"] != 85.0 || res.Data["fan"] != 300.0 {
		t.Errorf("aggregates = %v, want temp 85 fan 300", res.Data)
	}

	// fan aggregate is the min -> alert when a fan drops below 500.
	c = sensorsWith(readings, "", "", sensorPred{"fan", "<", 500})
	if res := c.Run(context.Background()); !res.OK {
		t.Errorf("slowest fan 300 < 500 should alert: %s", res.Message)
	}

	// chip filter: only coretemp -> no fans matched.
	c = sensorsWith(readings, "coretemp", "", sensorPred{"temp", ">", 80})
	if res := c.Run(context.Background()); res.Data["fan"] != nil {
		t.Errorf("chip filter should exclude fans: %v", res.Data)
	}

	// no matching inputs -> failure.
	c = sensorsWith(readings, "doesnotexist", "", sensorPred{"temp", ">", 80})
	if res := c.Run(context.Background()); res.OK {
		t.Error("no matching inputs must fail")
	}
}
