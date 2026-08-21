package checks

import (
	"maps"
	"time"
)

// lastSamplePrefix marks a reading as the newest value the device answered with
// rather than a current measurement. Retained readings deliberately never reuse
// their live key: the graph recorder persists every numeric field a result
// carries, so republishing "temperature" for a drive that stopped answering
// would draw a flat line at the last known temperature forever.
const lastSamplePrefix = "last_"

// Retained-reading data keys. DataKeyLastSeenSeconds is how long before this
// sample the retained one was taken, so the dashboard can say how old the last
// known values are without a second clock.
const (
	DataKeyLastSeenSeconds = "last_seen_seconds"
	DataKeyLastHealth      = lastSamplePrefix + DataKeyHealth
)

// LastSampleKey names the retained counterpart of one reading field, for the
// consumers that render "Temperature (last)" next to the live rows.
func LastSampleKey(field string) string { return lastSamplePrefix + field }

// lastSample retains the newest reading a device-addressing check managed to
// take, so a device that stops answering still reports what it last said. A
// drive that dies keeps its /dev node and simply goes quiet, and the readings
// that would tell an operator how it looked just before it went are exactly the
// ones the failed sample can no longer produce.
//
// The check instance outlives the cycle, so this is per-check memory: it starts
// empty after a daemon restart, which is honest — Sermo reports only samples it
// actually took. Device identity, which survives a restart because the kernel
// keeps publishing it, is resolved separately (see BlockDeviceIdentity).
//
// One watch ticks sequentially, so no lock, exactly as for diskIOState.
type lastSample struct {
	health string
	values map[string]float64
	at     time.Time
}

// record keeps one successful sample as the newest known state of the device.
func (s *lastSample) record(health string, values map[string]float64, at time.Time) {
	if s == nil {
		return
	}
	s.health, s.values, s.at = health, maps.Clone(values), at
}

// into adds the retained sample to a failed sample's reading data. It is a
// no-op until the device has answered at least once, so a check that never got
// a reading reports no invented history.
func (s *lastSample) into(data map[string]any, now time.Time) map[string]any {
	if s == nil || s.at.IsZero() {
		return data
	}
	if data == nil {
		data = map[string]any{}
	}
	if age := now.Sub(s.at); age > 0 {
		data[DataKeyLastSeenSeconds] = age.Seconds()
	}
	if s.health != "" {
		data[DataKeyLastHealth] = s.health
	}
	for field, value := range s.values {
		data[LastSampleKey(field)] = value
	}
	return data
}
