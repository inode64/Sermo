package checks

import (
	"context"
	"testing"
	"time"

	"sermo/internal/execx"
)

const smartATA = `{
  "smart_status": { "passed": true },
  "temperature": { "current": 38 },
  "power_on_time": { "hours": 12000 },
  "ata_smart_attributes": { "table": [
    { "id": 5, "name": "Reallocated_Sector_Ct", "raw": { "value": 4 } },
    { "id": 9, "name": "Power_On_Hours", "raw": { "value": 12000 } }
  ] }
}`

const smartNVMeFailing = `{
  "smart_status": { "passed": false },
  "temperature": { "current": 65 },
  "nvme_smart_health_information_log": { "percentage_used": 92 }
}`

func TestParseSmart(t *testing.T) {
	d, err := parseSmart(smartATA)
	if err != nil {
		t.Fatal(err)
	}
	if !d.healthKnown || !d.passed {
		t.Errorf("health = %+v, want known/passed", d)
	}
	if d.values["temperature"] != 38 || d.values["reallocated"] != 4 || d.values["power_on_hours"] != 12000 {
		t.Errorf("values = %v", d.values)
	}

	n, err := parseSmart(smartNVMeFailing)
	if err != nil {
		t.Fatal(err)
	}
	if n.passed || n.values["wear"] != 92 {
		t.Errorf("nvme = %+v, want failed / wear 92", n)
	}

	if _, err := parseSmart(""); err == nil {
		t.Error("empty output must error")
	}
}

func smartWith(out string, preds ...levelPred) smartCheck {
	return smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stdout: out}},
		device: "/dev/sda", preds: preds,
	}
}

func TestSmartCheck(t *testing.T) {
	// Default: alert when SMART health is FAILED.
	if res := smartWith(smartNVMeFailing).Run(context.Background()); !res.OK {
		t.Error("a FAILED SMART verdict should alert by default")
	}
	if res := smartWith(smartATA).Run(context.Background()); res.OK {
		t.Error("a PASSED verdict must not alert by default")
	}
	// Predicate: alert on reallocated sectors.
	if res := smartWith(smartATA, levelPred{"reallocated", ">", 0}).Run(context.Background()); !res.OK {
		t.Error("reallocated>0 predicate should alert")
	}
	if res := smartWith(smartATA, levelPred{"temperature", ">", 50}).Run(context.Background()); res.OK {
		t.Error("temperature 38 is not > 50")
	}
}

func TestSmartCheckError(t *testing.T) {
	c := smartCheck{
		base:   base{name: "sm", timeout: time.Second},
		runner: fakeRunner{execx.Result{Stderr: "/dev/sda: Unable to detect device type\n", ExitCode: 2}},
		device: "/dev/sda",
	}
	if res := c.Run(context.Background()); res.OK {
		t.Fatal("a smartctl error must fail the check")
	}
}
