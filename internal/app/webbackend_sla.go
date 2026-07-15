package app

import (
	"time"

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
