package pkgdb

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"sermo/internal/execx"
)

// rpmCacheTTL bounds how long an rpm answer is reused when the database
// directory mtime never changes (for example when /var/lib/rpm is a symlink
// into /usr/lib/sysimage and only the target is rewritten).
const rpmCacheTTL = time.Hour

const (
	rpmCommand        = "rpm"
	rpmQueryFileFlag  = "-qf"
	rpmArgsEnd        = "--"
	rpmFilePrefix     = "file "
	rpmNotOwnedSuffix = " is not owned by any package"
	rpmErrorPrefix    = "error: "
	rpmWarningPrefix  = "warning: "
)

// rpmEntry is one cached `rpm -qf` answer.
type rpmEntry struct {
	own Ownership
	at  time.Time
}

// ownedRPM overwrites the Unknown entries of out with cached answers first,
// then runs ONE `rpm -qf -- <path>...` for everything still pending. A
// command that fails to run leaves its paths Unknown and caches nothing.
func (x *Index) ownedRPM(ctx context.Context, exes []string, out map[string]Ownership) {
	if x.runner == nil {
		return
	}
	now := x.now()
	// cleaned maps the argument sent to rpm to the input spellings behind it.
	cleaned := map[string][]string{}
	pending := make([]string, 0, len(exes))

	x.mu.RLock()
	gen := x.gen
	for _, exe := range exes {
		p := filepath.Clean(exe)
		if !filepath.IsAbs(p) {
			continue
		}
		if entry, ok := x.rpmCache[p]; ok && now.Sub(entry.at) < rpmCacheTTL {
			out[exe] = entry.own
			continue
		}
		if _, seen := cleaned[p]; !seen {
			pending = append(pending, p)
		}
		cleaned[p] = append(cleaned[p], exe)
	}
	x.mu.RUnlock()
	if len(pending) == 0 {
		return
	}

	answers, ok := x.queryRPM(ctx, pending)
	if !ok {
		return
	}
	x.mu.Lock()
	defer x.mu.Unlock()
	for p, own := range answers {
		for _, exe := range cleaned[p] {
			out[exe] = own
		}
		// Only a determined answer is cached, and only into the generation
		// of the database it was computed against.
		if own != Unknown && gen == x.gen {
			x.rpmCache[p] = rpmEntry{own: own, at: now}
		}
	}
}

func (x *Index) queryRPM(ctx context.Context, paths []string) (map[string]Ownership, bool) {
	args := append([]string{rpmQueryFileFlag, rpmArgsEnd}, paths...)
	res, err := execx.RunProbe(ctx, x.runner, x.timeout, rpmCommand, args...)
	if err != nil {
		return nil, false
	}
	return parseRPMQuery(paths, res.Stdout+"\n"+res.Stderr), true
}

// parseRPMQuery maps `rpm -qf` output back onto the queried paths. rpm prints
// at least one line per argument, in order: "file <path> is not owned by any
// package" for an unowned path, "error: file <path>: ..." for a path it could
// not stat, and one package name per owner otherwise (a file may have several
// owners). The named lines are mapped by their path; every other queried path
// then received at least one owner line, provided the owner lines are at
// least as many as those paths. Any other error line means the whole run is
// undetermined.
func parseRPMQuery(paths []string, output string) map[string]Ownership {
	out := make(map[string]Ownership, len(paths))
	for _, p := range paths {
		out[p] = Unknown
	}
	errored := map[string]bool{}
	owners := 0
	for raw := range strings.Lines(output) {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if name, ok := rpmNotOwnedPath(line); ok {
			if _, known := out[name]; known {
				out[name] = NotOwned
				continue
			}
		}
		if rest, ok := strings.CutPrefix(line, rpmErrorPrefix); ok {
			name, found := rpmErrorPath(rest)
			if _, known := out[name]; found && known {
				errored[name] = true
				continue
			}
			return out
		}
		if strings.HasPrefix(line, rpmWarningPrefix) {
			continue
		}
		owners++
	}
	remaining := make([]string, 0, len(paths))
	for _, p := range paths {
		if out[p] == Unknown && !errored[p] {
			remaining = append(remaining, p)
		}
	}
	if owners >= len(remaining) {
		for _, p := range remaining {
			out[p] = Owned
		}
	}
	return out
}

// rpmNotOwnedPath parses "file <path> is not owned by any package".
func rpmNotOwnedPath(line string) (string, bool) {
	rest, ok := strings.CutPrefix(line, rpmFilePrefix)
	if !ok {
		return "", false
	}
	name, ok := strings.CutSuffix(rest, rpmNotOwnedSuffix)
	if !ok || name == "" {
		return "", false
	}
	return name, true
}

// rpmErrorPath parses the tail of "error: file <path>: <reason>".
func rpmErrorPath(rest string) (string, bool) {
	rest, ok := strings.CutPrefix(rest, rpmFilePrefix)
	if !ok {
		return "", false
	}
	sep := strings.LastIndex(rest, ": ")
	if sep <= 0 {
		return "", false
	}
	return rest[:sep], true
}
