package checks

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type pathMatch struct {
	message string
	data    map[string]any
	failure string
}

func firstMatchingPath(paths []string, predicate func(string, os.FileInfo) pathMatch, kindMsg string) pathMatch {
	if len(paths) == 0 {
		return pathMatch{failure: kindMsg + " check has no path candidates"}
	}
	var failures []string
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			failures = append(failures, fmt.Sprintf("%s: %v", path, err))
			continue
		}
		match := predicate(path, info)
		if match.failure != "" {
			failures = append(failures, match.failure)
			continue
		}
		return match
	}
	if len(failures) > 0 {
		return pathMatch{failure: strings.Join(failures, "; ")}
	}
	if len(paths) == 1 {
		return pathMatch{failure: paths[0] + " does not exist"}
	}
	return pathMatch{failure: fmt.Sprintf("none of %s candidates exist (%s)", kindMsg, strings.Join(paths, ", "))}
}

func pathMatchResult(b base, paths []string, predicate func(string, os.FileInfo) pathMatch, kindMsg string) Result {
	start := time.Now()
	match := firstMatchingPath(paths, predicate, kindMsg)
	if match.failure != "" {
		return b.result(false, match.failure, start)
	}
	res := b.result(true, match.message, start)
	res.Data = match.data
	return res
}
