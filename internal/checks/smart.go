package checks

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"sermo/internal/cfgval"
	"sermo/internal/execx"
)

type smartPred struct {
	field string // temperature | reallocated | wear | power_on_hours
	op    string
	value float64
}

// smartCheck reads a drive's SMART health and attributes via `smartctl -j`. With
// no predicate it alerts when the overall SMART health verdict is FAILED;
// predicates on `temperature` (°C), `reallocated` (sectors), `wear` (SSD/NVMe
// percentage used) and `power_on_hours` override/augment that. The numeric
// attributes are recorded over time, so a rising reallocated-sector or wear count
// (a failing/aging drive) is visible on the graph. Needs smartmontools (and root).
type smartCheck struct {
	base
	runner execx.Runner
	device string
	preds  []smartPred
}

func (c smartCheck) Run(ctx context.Context) Result {
	start := time.Now()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	res, _ := c.runner.Run(ctx, "smartctl", "-H", "-A", "-j", c.device)
	data, err := parseSmart(res.Stdout)
	if err != nil {
		if s := firstLine(res.Stderr); s != "" {
			return c.result(false, "smart "+c.device+": "+s, start)
		}
		return c.result(false, "smart "+c.device+": "+err.Error(), start)
	}

	ok := data.healthKnown && !data.passed // default alert condition: health FAILED
	if len(c.preds) > 0 {
		ok = true
		for _, p := range c.preds {
			v, present := data.values[p.field]
			if !present || !compareFloat(v, p.op, p.value) {
				ok = false
			}
		}
	}

	health := "unknown"
	if data.healthKnown {
		if data.passed {
			health = "PASSED"
		} else {
			health = "FAILED"
		}
	}
	r := c.result(ok, "smart "+c.device+" health="+health, start)
	r.Data = map[string]any{"device": c.device, "health": health}
	for k, v := range data.values {
		r.Data[k] = v
	}
	return r
}

// smartData is the parsed subset of `smartctl -j` output.
type smartData struct {
	passed      bool
	healthKnown bool
	values      map[string]float64 // temperature, reallocated, wear, power_on_hours
}

// parseSmart extracts the health verdict and the graphable attributes from
// smartctl's JSON (ATA and NVMe shapes).
func parseSmart(out string) (smartData, error) {
	if strings.TrimSpace(out) == "" {
		return smartData{}, fmt.Errorf("no smartctl output")
	}
	var j struct {
		SmartStatus *struct {
			Passed bool `json:"passed"`
		} `json:"smart_status"`
		Temperature struct {
			Current *float64 `json:"current"`
		} `json:"temperature"`
		PowerOnTime struct {
			Hours *float64 `json:"hours"`
		} `json:"power_on_time"`
		AtaAttrs struct {
			Table []struct {
				ID  int `json:"id"`
				Raw struct {
					Value *float64 `json:"value"`
				} `json:"raw"`
			} `json:"table"`
		} `json:"ata_smart_attributes"`
		NVMe struct {
			PercentageUsed *float64 `json:"percentage_used"`
		} `json:"nvme_smart_health_information_log"`
	}
	if err := json.Unmarshal([]byte(out), &j); err != nil {
		return smartData{}, fmt.Errorf("invalid smartctl JSON: %w", err)
	}

	d := smartData{values: map[string]float64{}}
	if j.SmartStatus != nil {
		d.passed, d.healthKnown = j.SmartStatus.Passed, true
	}
	if j.Temperature.Current != nil {
		d.values["temperature"] = *j.Temperature.Current
	}
	if j.PowerOnTime.Hours != nil {
		d.values["power_on_hours"] = *j.PowerOnTime.Hours
	}
	for _, a := range j.AtaAttrs.Table {
		if a.ID == 5 && a.Raw.Value != nil { // Reallocated_Sector_Ct
			d.values["reallocated"] = *a.Raw.Value
		}
	}
	if j.NVMe.PercentageUsed != nil {
		d.values["wear"] = *j.NVMe.PercentageUsed
	}
	return d, nil
}

// parseSmartPreds reads the optional temperature/reallocated/wear/power_on_hours
// predicates.
func parseSmartPreds(entry map[string]any) ([]smartPred, error) {
	var preds []smartPred
	for _, field := range []string{"temperature", "reallocated", "wear", "power_on_hours"} {
		raw, ok := entry[field]
		if !ok {
			continue
		}
		m, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s must be a mapping {op, value}", field)
		}
		op := cfgval.AsString(m["op"])
		if !validDiskOp(op) {
			return nil, fmt.Errorf("%s has invalid op %q", field, op)
		}
		val, err := strconv.ParseFloat(cfgval.String(m["value"]), 64)
		if err != nil {
			return nil, fmt.Errorf("%s value %q is not numeric", field, cfgval.String(m["value"]))
		}
		preds = append(preds, smartPred{field: field, op: op, value: val})
	}
	return preds, nil
}
