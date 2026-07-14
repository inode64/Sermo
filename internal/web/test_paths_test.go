package web

import "strings"

func testAPIPath(segments ...string) string {
	if len(segments) == 0 {
		return routePathAPI
	}
	return apiPathPrefix + strings.Join(segments, "/")
}

func testServicePath(name string, segments ...string) string {
	return testTargetPath(apiSegmentServices, name, segments...)
}

func testWatchPath(name string, segments ...string) string {
	return testTargetPath(apiSegmentWatches, name, segments...)
}

func testMountPath(name string, segments ...string) string {
	return testTargetPath(apiSegmentMounts, name, segments...)
}

func testLockPath(segments ...string) string {
	return testTargetPath(apiSegmentLocks, "mysql", segments...)
}

func testTargetPath(segment, name string, segments ...string) string {
	allSegments := make([]string, 0, 2+len(segments))
	allSegments = append(allSegments, segment, name)
	allSegments = append(allSegments, segments...)
	return testAPIPath(allSegments...)
}

func testPathQuery(path, query string) string {
	if query == "" {
		return path
	}
	return path + "?" + query
}

func testFlagQuery(path string) string {
	return testPathQuery(path, apiQueryVerbose)
}

func testQueryParam(name, value string) string {
	return name + "=" + value
}

func testQueryParams(pairs ...string) string {
	params := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		params = append(params, testQueryParam(pairs[i], pairs[i+1]))
	}
	return strings.Join(params, "&")
}
