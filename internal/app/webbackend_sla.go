package app

import (
	"slices"
	"time"

	"sermo/internal/state"
	"sermo/internal/web"
)

const (
	// slaTimelineCacheTTL caches SLA timeline strips for detail/expansion views.
	slaTimelineCacheTTL = 45 * time.Second
)

type slaCacheKey struct {
	service  string
	check    string // empty for service-level SLA
	evidence string // empty for observed SLA, "process" for process continuity
}

type cachedSLATimelines struct {
	windows []web.SLAWindow
	at      time.Time
}

func (b *WebBackend) serviceSLAWindows(name string, now time.Time) []web.SLAWindow {
	return b.cachedSLAWindows(name, "", now)
}

func (b *WebBackend) checkSLAWindows(service, check string, now time.Time) []web.SLAWindow {
	return b.cachedSLAWindows(service, check, now)
}

func (b *WebBackend) cachedSLAWindows(service, check string, now time.Time) []web.SLAWindow {
	if b.sla == nil {
		return nil
	}
	return b.cachedWindows(slaCacheKey{service: service, check: check}, now, func() ([]web.SLAWindow, bool) {
		var timelines []state.SLAWindowTimeline
		var err error
		if check == "" {
			timelines, err = b.sla.SLATimelines(service, now)
		} else {
			timelines, err = b.sla.CheckSLATimelines(service, check, now)
		}
		if err != nil {
			return nil, false
		}
		return toWebSLAWindows(timelines, now), true
	})
}

const slaEvidenceProcess = "process"

// processUptimeWindows renders separately confirmed process-continuity coverage.
// It intentionally is not part of the observed service/check SLA.
func (b *WebBackend) processUptimeWindows(service string, now time.Time) []web.SLAWindow {
	if b.processUptime == nil {
		return nil
	}
	return b.cachedWindows(slaCacheKey{service: service, evidence: slaEvidenceProcess}, now, func() ([]web.SLAWindow, bool) {
		timelines, err := b.processUptime.ProcessUptimeReport(service, now)
		if err != nil {
			return nil, false
		}
		return toWebProcessUptimeWindows(timelines, now), true
	})
}

func (b *WebBackend) cachedWindows(key slaCacheKey, now time.Time, load func() ([]web.SLAWindow, bool)) []web.SLAWindow {
	b.slaCacheMu.Lock()
	if b.slaCache == nil {
		b.slaCache = map[slaCacheKey]cachedSLATimelines{}
	}
	if cached, ok := b.slaCache[key]; ok && now.Sub(cached.at) < slaTimelineCacheTTL {
		out := cached.windows
		b.slaCacheMu.Unlock()
		return slices.Clone(out)
	}
	b.slaCacheMu.Unlock()

	windows, ok := load()
	if !ok {
		return nil
	}

	b.slaCacheMu.Lock()
	b.slaCache[key] = cachedSLATimelines{windows: slices.Clone(windows), at: now}
	b.slaCacheMu.Unlock()
	return windows
}

func toWebProcessUptimeWindows(timelines []state.ProcessUptimeWindow, observedAt time.Time) []web.SLAWindow {
	out := make([]web.SLAWindow, 0, len(timelines))
	for _, timeline := range timelines {
		win := web.SLAWindow{
			Window:     timeline.Window,
			Evidence:   slaEvidenceProcess,
			Up:         timeline.CoveredSeconds,
			Total:      timeline.TotalSeconds,
			ObservedAt: observedAt.UTC().Format(time.RFC3339),
		}
		if timeline.Known && timeline.TotalSeconds > 0 {
			ratio := float64(timeline.CoveredSeconds) / float64(timeline.TotalSeconds)
			win.Ratio = &ratio
		}
		if len(timeline.Segments) > 0 {
			segments := make([]*float64, len(timeline.Segments))
			for i, coverage := range timeline.Segments {
				if coverage > 0 {
					segments[i] = &coverage
				}
			}
			win.Segments = segments
		}
		out = append(out, win)
	}
	return out
}

func toWebSLAWindows(timelines []state.SLAWindowTimeline, observedAt time.Time) []web.SLAWindow {
	out := make([]web.SLAWindow, 0, len(timelines))
	for _, timeline := range timelines {
		win := web.SLAWindow{Window: timeline.Window, Up: timeline.Up, Total: timeline.Total, ObservedAt: observedAt.UTC().Format(time.RFC3339)}
		if timeline.Total > 0 {
			ratio := float64(timeline.Up) / float64(timeline.Total)
			win.Ratio = &ratio
		}
		if len(timeline.Segments) > 0 {
			segments := make([]*float64, len(timeline.Segments))
			for i, segment := range timeline.Segments {
				if segment.Total > 0 {
					ratio := float64(segment.Up) / float64(segment.Total)
					segments[i] = &ratio
				}
			}
			win.Segments = segments
		}
		out = append(out, win)
	}
	return out
}
