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
	service string
	check   string // empty for service-level SLA
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

// toWebWindows converts each source timeline window with convert and stamps
// the shared observation time.
func toWebWindows[T any](timelines []T, observedAt time.Time, convert func(T) web.SLAWindow) []web.SLAWindow {
	at := observedAt.UTC().Format(time.RFC3339)
	out := make([]web.SLAWindow, 0, len(timelines))
	for _, timeline := range timelines {
		win := convert(timeline)
		win.ObservedAt = at
		out = append(out, win)
	}
	return out
}

// slaRatio returns up/total as an optional ratio: nil when the window is
// unknown or nothing was observed (total 0), which renders as a gap.
func slaRatio(up, total int64, known bool) *float64 {
	if !known || total <= 0 {
		return nil
	}
	ratio := float64(up) / float64(total)
	return &ratio
}

func toWebSLAWindows(timelines []state.SLAWindowTimeline, observedAt time.Time) []web.SLAWindow {
	return toWebWindows(timelines, observedAt, func(timeline state.SLAWindowTimeline) web.SLAWindow {
		win := web.SLAWindow{Window: timeline.Window, Up: timeline.Up, Total: timeline.Total, Ratio: slaRatio(timeline.Up, timeline.Total, true)}
		if len(timeline.Segments) > 0 {
			segments := make([]*float64, len(timeline.Segments))
			for i, segment := range timeline.Segments {
				segments[i] = slaRatio(segment.Up, segment.Total, true)
			}
			win.Segments = segments
		}
		return win
	})
}
