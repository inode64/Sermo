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

type cachedSLATimelines struct {
	windows []web.SLAWindow
	at      time.Time
}

// serviceSLAWindows is the rolling hour..year strip on the application card.
// Only the service scope needs it: a check's availability is served as a series
// from Series, on the window the detail's selector is on.
func (b *WebBackend) serviceSLAWindows(name string, now time.Time) []web.SLAWindow {
	if b.sla == nil {
		return nil
	}
	return b.cachedWindows(name, now, func() ([]web.SLAWindow, bool) {
		timelines, err := b.sla.SLATimelines(name, now)
		if err != nil {
			return nil, false
		}
		return toWebSLAWindows(timelines, now), true
	})
}

func (b *WebBackend) cachedWindows(key string, now time.Time, load func() ([]web.SLAWindow, bool)) []web.SLAWindow {
	b.slaCacheMu.Lock()
	if b.slaCache == nil {
		b.slaCache = map[string]cachedSLATimelines{}
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
		win := web.SLAWindow{
			Window:      timeline.Window,
			Up:          timeline.Up,
			Total:       timeline.Total,
			DownBuckets: timeline.DownBuckets,
			Ratio:       slaRatio(timeline.Up, timeline.Total, true),
		}
		if len(timeline.Segments) > 0 {
			segments := make([]web.SLASegment, len(timeline.Segments))
			for i, segment := range timeline.Segments {
				segments[i] = web.SLASegment{
					Up:          segment.Up,
					Total:       segment.Total,
					DownBuckets: segment.DownBuckets,
				}
			}
			win.Segments = segments
		}
		return win
	})
}
