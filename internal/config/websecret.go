package config

import (
	"fmt"
	"os"
	"path/filepath"

	"sermo/internal/cfgval"
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
func resolveWebCredentials(g *Global) {
	web := g.WebSection()
	if web == nil {
		return
	}
	base := configBaseDir(g.Path)
	targets := []*webcred.List{&g.webCredentials, &g.webGuestCredentials}
	for i, key := range webCredentialFileKeys {
		list, err := loadWebCredentials(web, base, key)
		if err != nil {
			g.issues = append(g.issues, Issue{
				Scope: globalScope,
				Msg:   fmt.Sprintf("%s.%s %v", SectionWeb, key, err),
			})
			continue
		}
		*targets[i] = list
	}
}

// loadWebCredentials reads one role's hashed credentials from its file key.
func loadWebCredentials(web map[string]any, base, fileKey string) (webcred.List, error) {
	// A non-string or empty value is reported by validateWeb; there is nothing
	// to read here.
	if path := cfgval.AsString(web[fileKey]); path != "" {
		path = resolveConfigPath(base, path)
		data, err := os.ReadFile(filepath.Clean(path))
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
	m, _ := g.Raw[SectionWeb].(map[string]any)
	return m
}
