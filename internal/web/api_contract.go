package web

import (
	"net/url"
)

// Shared daemon HTTP contract used by the dashboard and sermoctl. Keep these
// values stable: changing one changes the public daemon API.
const (
	HeaderConfirm    = "X-Sermo-Confirm"
	HeaderCSRF       = "X-Sermo-Csrf"
	HeaderGeneration = "X-Sermo-Generation"
	CSRFHeaderValue  = "1"

	APIPathRoot          = "/api"
	APIPathApplications  = APIPathRoot + "/applications"
	APIPathEvents        = APIPathRoot + "/events"
	APIPathEventsClear   = APIPathEvents + "/clear"
	APIPathLibraries     = APIPathRoot + "/libraries"
	APIPathServices      = APIPathRoot + "/services"
	APIPathWatches       = APIPathRoot + "/watches"
	APIPathServiceEvents = "/events"
	APIQueryBefore       = "before"
	APIQueryLimit        = "limit"
	APIQueryName         = "name"

	// APISegmentRefresh closes the manual refresh routes
	// POST /api/applications/refresh, POST /api/libraries/refresh and
	// POST /api/watches/refresh?name=WATCH (name repeatable, none for all).
	APISegmentRefresh = "refresh"

	// APIPathSessions is the session inventory; the segments below build the
	// database statement kill route
	// POST /api/services/{service}/db-queries/{watch}/kill?id=&identity=&mode=.
	APIPathSessions     = APIPathRoot + "/sessions"
	APISegmentDBQueries = "db-queries"
	APIActionKill       = "kill"
	APIQueryID          = "id"
	APIQueryIdentity    = "identity"
	APIQueryMode        = "mode"

	// APISegmentProcesses, APIQueryStartTicks and APIQueryEscalate build the
	// unowned_processes kill route
	// POST /api/watches/{watch}/processes/{pid}/kill?start_ticks=&escalate=.
	// start_ticks is the PID incarnation the watch displayed; the SSH session
	// close route names its session with the same query.
	APISegmentProcesses = "processes"
	APIQueryStartTicks  = "start_ticks"
	APIQueryEscalate    = "escalate"
)

// RefreshPath is the refresh route of a collection (APIPathApplications,
// APIPathLibraries or APIPathWatches); names pick watches, none means all.
func RefreshPath(collection string, names ...string) string {
	path := collection + "/" + APISegmentRefresh
	if len(names) == 0 {
		return path
	}
	return path + "?" + url.Values{APIQueryName: names}.Encode()
}

// DBQueryKillPath is the kill route of one service's db_queries watch, without
// its query string.
func DBQueryKillPath(service, watch string) string {
	return APIPathServices + "/" + url.PathEscape(service) + "/" + APISegmentDBQueries + "/" + url.PathEscape(watch) + "/" + APIActionKill
}
