package config

import (
	"fmt"
	"slices"

	"sermo/internal/cfgval"
	"sermo/internal/hostfs"
	"sermo/internal/webcred"
)

// webCredentialFileKeys lists the admin and guest hashed-credential sources.
// Order matches the destination lists in resolveWebCredentials.
var webCredentialFileKeys = [...]string{WebKeyPasswordFile, WebKeyGuestPasswordFile}

// resolveWebCredentials parses the dashboard credentials once at load time and
// keeps them on g, so the passwords need never be written in sermo.yml — every
// `*_file` holds hashes, which the daemon can verify but nothing can read
// back. A relative path is taken relative to the directory holding sermo.yml.
//
// A bad source is recorded as a validation issue rather than a load error: an
// operator running `config validate` without access to a root-owned secret file
// gets a clear message instead of a loader that refuses to start.
func resolveWebCredentials(g *Global) []Issue {
	web := g.WebSection()
	if web == nil {
		return nil
	}
	var issues []Issue
	base := configBaseDir(g.Path)
	targets := []*webcred.List{&g.webCredentials, &g.webGuestCredentials}
	for i, key := range webCredentialFileKeys {
		list, err := loadWebCredentials(web, base, key)
		if err != nil {
			issues = append(issues, Issue{
				Scope: globalScope,
				Msg:   fmt.Sprintf("%s.%s %v", SectionWeb, key, err),
			})
			continue
		}
		*targets[i] = list
	}
	return issues
}

// unselectedWebCredentialIssues reports a listening web block whose `os:`
// selector names credentials for other hosts and has no branch for this one.
// Collapsing it would leave no trace of them, and no credentials is an open
// dashboard, so this host must fail validation instead. An explicit empty branch
// still declares an open dashboard on purpose. raw is the tree before `os:`
// selectors collapse.
func unselectedWebCredentialIssues(raw map[string]any) []Issue {
	web, _ := raw[SectionWeb].(map[string]any)
	selector, isSelector := web[keyOS].(map[string]any)
	_, listens := web[WebKeyPort]
	if !listens || !isSelector || selectOSBranch(selector, detectedOS) != nil ||
		namesWebCredentials(web) || cfgval.Bool(web[WebKeyGuest]) {
		return nil
	}
	for _, branch := range selector {
		if block, ok := branch.(map[string]any); ok && namesWebCredentials(block) {
			return []Issue{{Scope: globalScope, Msg: fmt.Sprintf(
				"%s.%s has no branch for %s, so this host would serve an open dashboard; add a %s branch with %s",
				SectionWeb, keyOS, detectedOS, keyOSDefault, WebKeyPasswordFile)}}
		}
	}
	return nil
}

func namesWebCredentials(block map[string]any) bool {
	return slices.ContainsFunc(webCredentialFileKeys[:], func(key string) bool {
		_, named := block[key]
		return named
	})
}

// loadWebCredentials reads one role's hashed credentials from its file key.
func loadWebCredentials(web map[string]any, base, fileKey string) (webcred.List, error) {
	// A non-string or empty value is reported by validateWeb; there is nothing
	// to read here.
	if path := cfgval.AsString(web[fileKey]); path != "" {
		path = resolveConfigPath(base, path)
		data, err := hostfs.ReadFile(path)
		if err != nil {
			return webcred.List{}, fmt.Errorf("cannot be read: %w", err)
		}
		list, err := webcred.Parse(string(data))
		if err != nil {
			return webcred.List{}, fmt.Errorf("%w: %s", err, path)
		}
		return list, nil
	}
	return webcred.List{}, nil
}

// WebCredentialFiles returns the configured web.password_file /
// web.guest_password_file paths, resolved the same way the loader resolved them
// (relative to the directory holding sermo.yml). Callers that inspect the files
// themselves — sermod warns about lax permissions — must use these, not the raw
// values, which may be relative to a directory no longer current.
func (g Global) WebCredentialFiles() []string {
	web := g.WebSection()
	if web == nil {
		return nil
	}
	base := configBaseDir(g.Path)
	var paths []string
	for _, key := range webCredentialFileKeys {
		if path := cfgval.AsString(web[key]); path != "" {
			paths = append(paths, resolveConfigPath(base, path))
		}
	}
	return paths
}

// WebCredentials returns the credentials that grant admin access to the
// dashboard from web.password_file.
func (g Global) WebCredentials() webcred.List {
	return g.webCredentials
}

// WebGuestCredentials returns the credentials that grant read-only access.
func (g Global) WebGuestCredentials() webcred.List {
	return g.webGuestCredentials
}

// WebSection returns the raw [web] section, or nil when the config does not
// configure one.
func (g Global) WebSection() map[string]any {
	return SectionMap(g.Raw, SectionWeb)
}
