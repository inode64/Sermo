package web

import "net/url"

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
	APIPathServices      = APIPathRoot + "/services"
	APIPathWatches       = APIPathRoot + "/watches"
	APIPathServiceEvents = "/events"
	APIQueryBefore       = "before"
	APIQueryLimit        = "limit"

	// APIPathSessions is the session inventory; the segments below build the
	// database statement kill route
	// POST /api/services/{service}/db-queries/{watch}/kill?id=&identity=&mode=.
	APIPathSessions     = APIPathRoot + "/sessions"
	APISegmentDBQueries = "db-queries"
	APIActionKill       = "kill"
	APIQueryID          = "id"
	APIQueryIdentity    = "identity"
	APIQueryMode        = "mode"
)

// DBQueryKillPath is the kill route of one service's db_queries watch, without
// its query string.
func DBQueryKillPath(service, watch string) string {
	return APIPathServices + "/" + url.PathEscape(service) + "/" + APISegmentDBQueries + "/" + url.PathEscape(watch) + "/" + APIActionKill
}
