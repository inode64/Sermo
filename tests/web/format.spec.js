// Parity harness for the canonical duration formatter: the SAME case table
// drives units.HumanizeDuration (internal/units/units_test.go) and fmtDuration
// here, so the daemon and the web UI cannot drift apart silently.
const { test, expect } = require("@playwright/test");
const path = require("path");
const { pathToFileURL } = require("url");

const cases = require("../../internal/units/testdata/duration_cases.json");
const formatURL = pathToFileURL(path.join(__dirname, "..", "..", "internal", "web", "src", "format.js")).href;

test("fmtDuration matches the shared Go parity table", async () => {
  const fmt = await import(formatURL);
  expect(cases.length).toBeGreaterThan(0);
  for (const c of cases) {
    expect(fmt.fmtDuration(c.seconds), `fmtDuration(${c.seconds})`).toBe(c.want);
  }
});

test("duration helpers route through fmtDuration", async () => {
  const fmt = await import(formatURL);
  expect(fmt.fmtDuration(-5)).toBe("0s");
  expect(fmt.fmtDuration(NaN)).toBe("");
  expect(fmt.fmtSince(7400 * 1000)).toBe("2h 3m 20s");
  // Clock-relative helpers: allow one second of skew between Date.now() calls.
  expect(fmt.fmtUntilShort(new Date(Date.now() + 400 * 1000).toISOString())).toMatch(/^in 6m (39|40)s$/);
  expect(fmt.fmtAge(new Date(Date.now() - 400 * 1000).toISOString())).toMatch(/^6m 4[01]s ago$/);
  // Ages beyond a day keep the absolute UTC timestamp fallback.
  expect(fmt.fmtAge(new Date(Date.now() - 2 * 86400 * 1000).toISOString())).toMatch(/ UTC$/);
});
