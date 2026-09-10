package checks

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type pathMatch struct {
	message     string
	data        map[string]any
	failure     string
	unavailable bool
	missing     bool
}

func firstMatchingPath(paths []string, predicate func(string, os.FileInfo) pathMatch, kindMsg string) pathMatch {
	match := firstPathMatch(paths, func(path string) pathMatch {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				return pathMatch{missing: true}
			}
			return pathMatch{failure: fmt.Sprintf("%s: %v", path, err), unavailable: true}
		}
		return predicate(path, info)
	}, kindMsg)
	if !match.missing {
		return match
	}
	if len(paths) == 1 {
		match.failure = paths[0] + " does not exist"
		return match
	}
	match.failure = fmt.Sprintf("none of %s candidates exist (%s)", kindMsg, strings.Join(paths, ", "))
	return match
}

// firstPathMatch tries ordered path candidates until predicate accepts one. A
// missing result is returned separately so callers with a valid fallback can
// distinguish it from stale or unreadable candidates.
func firstPathMatch(paths []string, predicate func(string) pathMatch, kindMsg string) pathMatch {
	if len(paths) == 0 {
		return pathMatch{failure: kindMsg + " check has no path candidates"}
	}
	var failures []string
	unavailable := false
	for _, path := range paths {
		match := predicate(path)
		if match.missing {
			continue
		}
		if match.failure != "" {
			unavailable = unavailable || match.unavailable
			failures = append(failures, match.failure)
			continue
		}
		return match
	}
	if len(failures) > 0 {
		return pathMatch{failure: strings.Join(failures, "; "), unavailable: unavailable}
	}
	return pathMatch{missing: true}
}

func pathMatchResult(b base, paths []string, predicate func(string, os.FileInfo) pathMatch, kindMsg string) Result {
	start := time.Now()
	match := firstMatchingPath(paths, predicate, kindMsg)
	if match.failure != "" {
		if match.unavailable {
			return b.unavailableResult(match.failure, start)
		}
		return b.result(false, match.failure, start)
	}
	res := b.result(true, match.message, start)
	res.Data = match.data
	return res
}
