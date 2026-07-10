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
// decimals places and strips trailing zeros; fixed precision remains reserved
// for chart geometry and CSS dimensions.
export function fmtNum(value, decimals = 2, fallback = "—") {
  const number = Number(value);
  if (!Number.isFinite(number)) return fallback;
  return number.toFixed(decimals).replace(/(\.\d*?)0+$/, "$1").replace(/\.$/, "");
}

export function fmtUptime(value) {
  const seconds = Math.floor(Number(value));
  if (!Number.isFinite(seconds) || seconds < 0) return "";
  const days = Math.floor(seconds / secondsPerDay);
  const hours = Math.floor((seconds % secondsPerDay) / secondsPerHour);
  const minutes = Math.floor((seconds % secondsPerHour) / secondsPerMinute);
  const remainingSeconds = seconds % secondsPerMinute;
  const parts = [];
  if (days) parts.push(days + "d");
  if (days || hours) parts.push(hours + "h");
  if (days || hours || minutes) parts.push(minutes + "m");
  parts.push(remainingSeconds + "s");
  return parts.join(" ");
}

export function fmtBytes(value) {
  let number = Number(value);
  if (!Number.isFinite(number) || number < 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB"];
  let unit = 0;
  while (number >= 1024 && unit < units.length - 1) {
    number /= 1024;
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

export function shortDur(value) {
  const seconds = Math.max(0, Math.floor(Number(value) || 0));
  if (seconds < secondsPerMinute) return seconds + "s";
  if (seconds < secondsPerHour) return Math.floor(seconds / secondsPerMinute) + "m";
  if (seconds < secondsPerDay) return Math.floor(seconds / secondsPerHour) + "h";
  return Math.floor(seconds / secondsPerDay) + "d";
}

export function fmtSeconds(value) { return shortDur(value); }

export function fmtMetricValue(value, unit) {
  const number = Number(value || 0);
  switch (unit) {
    case metricUnitBytes:
      return fmtBytes(number);
    case metricUnitBytesPerSecond:
      return fmtBytes(number) + "/s";
    case metricUnitPercent:
      return fmtNum(number, 2) + metricUnitPercent;
    case metricUnitMilliseconds:
      return fmtNum(number, 2) + metricUnitMilliseconds;
    default:
      return fmtNum(number, 2) + (unit || "");
  }
}

export function fmtTime(value) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? (value || "") : date.toLocaleString();
}

export function fmtRemain(until) {
  const date = new Date(until);
  if (Number.isNaN(date.getTime())) return "";
  const seconds = Math.floor((date - Date.now()) / millisecondsPerSecond);
  if (seconds <= 0) return "elapsed";
  if (seconds < secondsPerHour) return shortDur(seconds) + " remaining";
  return Math.floor(seconds / secondsPerHour) + "h remaining · until " + fmtTime(until);
}

export function fmtUntilShort(until) {
  const date = new Date(until);
  if (Number.isNaN(date.getTime())) return "";
  const seconds = Math.floor((date - Date.now()) / millisecondsPerSecond);
  if (seconds <= 0) return "now";
  if (seconds < secondsPerDay) return "in " + shortDur(seconds);
  return date.toLocaleDateString();
}

export function fmtAge(value) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  const seconds = Math.floor((Date.now() - date) / millisecondsPerSecond);
  if (seconds < 0) return "just now";
  if (seconds < secondsPerDay) return shortDur(seconds) + " ago";
  return fmtTime(value);
}

export function fmtSince(value) {
  const seconds = Math.max(0, Math.round(value / millisecondsPerSecond));
  if (seconds < secondsPerMinute) return seconds + "s";
  const minutes = Math.floor(seconds / secondsPerMinute);
  const remainingSeconds = seconds % secondsPerMinute;
  return remainingSeconds ? `${minutes}m ${remainingSeconds}s` : `${minutes}m`;
}
