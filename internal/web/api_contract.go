package web

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
)
