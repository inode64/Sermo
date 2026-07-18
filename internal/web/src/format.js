export const metricUnitPercent = "%";
export const metricUnitBytes = "bytes";
export const metricUnitBytesPerSecond = "B/s";
export const metricUnitMilliseconds = "ms";
export const percentMin = 0;
export const percentMax = 100;
export const percentScale = percentMax;
export const secondsPerMinute = 60;
export const minutesPerHour = 60;
export const hoursPerDay = 24;
export const rollingWeekDays = 7;
export const rollingMonthDays = 30;
export const rollingYearDays = 365;
export const secondsPerHour = secondsPerMinute * minutesPerHour;
export const secondsPerDay = secondsPerHour * hoursPerDay;
export const millisecondsPerSecond = 1000;
export const millisecondsPerMinute = millisecondsPerSecond * secondsPerMinute;
export const millisecondsPerHour = millisecondsPerMinute * minutesPerHour;
export const millisecondsPerDay = millisecondsPerHour * hoursPerDay;

// fmtNum is the base formatter for every user-visible number. It keeps at most
// decimals places, strips trailing zeros and groups thousands with commas
// (12,345.68) — the same canonical convention the daemon uses in events and
// notifications. Fixed precision remains reserved for chart geometry and CSS
// dimensions.
export function fmtNum(value, decimals = 2, fallback = "—") {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  const trimmed = number.toFixed(decimals).replace(/(\.\d*?)0+$/, "$1").replace(/\.$/, "");
  const [whole, fraction] = trimmed.split(".");
  const grouped = whole.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return fraction === undefined ? grouped : grouped + "." + fraction;
}

// Duration display constants: month = rollingMonthDays (30 days) and the
// display year is exactly 12 such months (360 days) so month components can
// never overflow into a 13th month. rollingYearDays (365) is a different
// concept — the length of rolling SLA chart windows — and stays as is. Each
// ceiling is the largest (inclusive) value still shown with that head unit;
// keep them in step with units.HumanizeDuration in internal/units/units.go.
const monthsPerYear = 12;
const durationSecondsCeiling = 360;
const durationHoursCeiling = 72;
const durationDaysCeiling = 120;
const durationMonthsCeiling = 24;

// fmtDuration is the one formatter for every user-visible duration. It renders
// space-separated whole components, greatest-first, skipping zeros ("2h 3m
// 20s", "3y 2mo 10d 12h 20s"), promoting the head unit with hysteresis: bare
// seconds up to 360s, hours up to 72h ("70h 15m"), days up to 120d, months up
// to 24mo, then years. Mirrors the daemon's units.HumanizeDuration exactly —
// tests/web/format.spec.js checks both sides against the same case table.
export function fmtDuration(value) {
  const seconds = Math.floor(Number(value));
  if (!Number.isFinite(seconds)) return "";
  if (seconds <= 0) return "0s";
  if (seconds <= durationSecondsCeiling) return seconds + "s";
  const month = rollingMonthDays * secondsPerDay;
  const year = monthsPerYear * month;
  const units = [
    [year, "y"], [month, "mo"], [secondsPerDay, "d"],
    [secondsPerHour, "h"], [secondsPerMinute, "m"], [1, "s"],
  ];
  // A unit leads only once the value exceeds the lower unit's ceiling
  // (e.g. days lead only past 72 hours).
  let head;
  if (seconds > durationMonthsCeiling * month) head = 0;
  else if (seconds > durationDaysCeiling * secondsPerDay) head = 1;
  else if (seconds > durationHoursCeiling * secondsPerHour) head = 2;
  else head = 3;
  const parts = [];
  let rest = seconds;
  for (const [size, suffix] of units.slice(head)) {
    if (rest >= size) {
      parts.push(Math.floor(rest / size) + suffix);
      rest %= size;
    }
  }
  return parts.join(" ");
}

// Sizes are IEC binary (KiB, base 1024); byte rates are SI decimal (KB/s,
// base 1000). The daemon's formatSummaryBytes/formatSummaryBytesPerSecond
// mirror this split exactly — keep both sides in step.
const byteBase = 1024;
const byteRateBase = 1000;

export function fmtBytes(value) {
  let number = Number(value);
  if (!Number.isFinite(number) || number < 0) return "0 B";
  const units = ["B", "KiB", "MiB", "GiB", "TiB"];
  let unit = 0;
  while (number >= byteBase && unit < units.length - 1) {
    number /= byteBase;
    unit++;
  }
  return fmtNum(number, 2, "0") + " " + units[unit];
}

export function fmtBytesPerSecond(value) {
  let number = Number(value);
  if (!Number.isFinite(number) || number < 0) return "0 B/s";
  const units = ["B/s", "KB/s", "MB/s", "GB/s", "TB/s"];
  let unit = 0;
  while (number >= byteRateBase && unit < units.length - 1) {
    number /= byteRateBase;
    unit++;
  }
  return fmtNum(number, 2, "0") + " " + units[unit];
}

export function fmtPct(value) {
  const number = Number(value);
  return Number.isFinite(number) ? fmtNum(number, 2) + metricUnitPercent : "—";
}

export function pctClamp(value) {
  const number = Number(value);
  if (!Number.isFinite(number)) return percentMin;
  return Math.max(percentMin, Math.min(percentMax, number));
}

export function fmtMetricValue(value, unit) {
  const number = Number(value || 0);
  switch (unit) {
    case metricUnitBytes:
      return fmtBytes(number);
    case metricUnitBytesPerSecond:
      return fmtBytesPerSecond(number);
    case metricUnitPercent:
      return fmtNum(number, 2) + metricUnitPercent;
    case metricUnitMilliseconds:
      return fmtNum(number, 2) + metricUnitMilliseconds;
    default:
      return fmtNum(number, 2) + (unit ? " " + unit : "");
  }
}

// fmtTime renders timestamps in UTC with an explicit suffix, matching the
// daemon's event timestamps so the same instant never reads differently
// between the event log, notifications and the dashboard.
export function fmtTime(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value || "";
  return date.toISOString().slice(0, 19).replace("T", " ") + " UTC";
}

export function fmtRemain(until) {
  const date = new Date(until);
  if (Number.isNaN(date.getTime())) return "";
  const seconds = Math.floor((date - Date.now()) / millisecondsPerSecond);
  if (seconds <= 0) return "elapsed";
  if (seconds < secondsPerHour) return fmtDuration(seconds) + " remaining";
  return fmtDuration(seconds) + " remaining · until " + fmtTime(until);
}

export function fmtUntilShort(until) {
  const date = new Date(until);
  if (Number.isNaN(date.getTime())) return "";
  const seconds = Math.floor((date - Date.now()) / millisecondsPerSecond);
  if (seconds <= 0) return "now";
  if (seconds < secondsPerDay) return "in " + fmtDuration(seconds);
  return date.toLocaleDateString();
}

export function fmtAge(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const seconds = Math.floor((Date.now() - date) / millisecondsPerSecond);
  if (seconds < 0) return "just now";
  if (seconds < secondsPerDay) return fmtDuration(seconds) + " ago";
  return fmtTime(value);
}

// fmtSince takes an elapsed time in milliseconds (the only duration helper
// that does) and renders it through the canonical duration formatter.
export function fmtSince(value) {
  return fmtDuration(Math.max(0, Math.round(value / millisecondsPerSecond)));
}
