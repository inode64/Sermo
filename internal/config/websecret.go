package config

import (
	"fmt"
	"os"
	"path/filepath"

	"sermo/internal/cfgval"
	"sermo/internal/webcred"
)

// webCredentialKeys pairs each web password key with the `*_file` companion that
// supplies it from disk instead of from sermo.yml. Order matches the destination
// lists in resolveWebCredentials.
var webCredentialKeys = [][2]string{
	{WebKeyPassword, WebKeyPasswordFile},
	{WebKeyGuestPassword, WebKeyGuestPasswordFile},
}

// resolveWebCredentials parses the dashboard credentials once at load time and
// keeps them on g, so the passwords need never be written in sermo.yml — a
// `*_file` may hold hashes, which the daemon can verify but nothing can read
// back. A relative path is taken relative to the directory holding sermo.yml,
// like the paths.* directories.
//
// A bad source is recorded as a validation issue rather than a load error: an
// operator running `config validate` without access to a root-owned secret file
// gets a clear message instead of a loader that refuses to start.
func resolveWebCredentials(g *Global) {
	web, ok := g.Raw[SectionWeb].(map[string]any)
	if !ok {
		return
	}
	base := configBaseDir(g.Path)
	targets := []*webcred.List{&g.webCredentials, &g.webGuestCredentials}
	for i, keys := range webCredentialKeys {
		list, key, err := loadWebCredentials(web, base, keys)
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

// loadWebCredentials reads one role's credentials from its `*_file` key, or from
// the inline key when no file is configured. It reports which key the result (or
// the error) came from. Both keys set at once is caught by validateWeb; here the
// file wins, matching the documented precedence.
func loadWebCredentials(web map[string]any, base string, keys [2]string) (webcred.List, string, error) {
	inlineKey, fileKey := keys[0], keys[1]
	// A non-string or empty value is reported by validateWeb; there is nothing
	// to read here.
	if path := cfgval.AsString(web[fileKey]); path != "" {
		path = resolveConfigPath(base, path)
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return webcred.List{}, fileKey, fmt.Errorf("cannot be read: %w", err)
		}
		list, err := webcred.Parse(string(data))
		if err != nil {
			return webcred.List{}, fileKey, fmt.Errorf("%w: %s", err, path)
		}
		return list, fileKey, nil
	}
	list, err := inlineWebCredentials(web, inlineKey)
	return list, inlineKey, err
}

// inlineWebCredentials parses a password written directly in sermo.yml. An empty
// value is not an error: it is how the dashboard is left open on purpose.
func inlineWebCredentials(web map[string]any, key string) (webcred.List, error) {
	secret := cfgval.AsString(web[key])
	if secret == "" {
		return webcred.List{}, nil
	}
	list, err := webcred.ParseInline(secret)
	if err != nil {
		return webcred.List{}, fmt.Errorf("is invalid: %w", err)
	}
	return list, nil
}

// WebCredentialFiles returns the configured web.password_file /
// web.guest_password_file paths, resolved the same way the loader resolved them
// (relative to the directory holding sermo.yml). Callers that inspect the files
// themselves — sermod warns about lax permissions — must use these, not the raw
// values, which may be relative to a directory no longer current.
func (g Global) WebCredentialFiles() []string {
	web, ok := g.Raw[SectionWeb].(map[string]any)
	if !ok {
		return nil
	}
	base := configBaseDir(g.Path)
	var paths []string
	for _, keys := range webCredentialKeys {
		if path := cfgval.AsString(web[keys[1]]); path != "" {
			paths = append(paths, resolveConfigPath(base, path))
		}
	}
	return paths
}

// WebCredentials returns the credentials that grant admin access to the
// dashboard: the contents of web.password_file when that key is used, otherwise
// web.password.
func (g Global) WebCredentials() webcred.List {
	return g.webRoleCredentials(g.webCredentials, WebKeyPassword)
}

// WebGuestCredentials returns the credentials that grant read-only access.
func (g Global) WebGuestCredentials() webcred.List {
	return g.webRoleCredentials(g.webGuestCredentials, WebKeyGuestPassword)
}

// webRoleCredentials prefers the list parsed at load time, falling back to the
// inline key in Raw so a Global built without the loader still works. A bad
// inline value yields no credentials; the loader reports it as an issue.
func (g Global) webRoleCredentials(loaded webcred.List, key string) webcred.List {
	if !loaded.Empty() {
		return loaded
	}
	web, _ := g.Raw[SectionWeb].(map[string]any)
	list, _ := inlineWebCredentials(web, key)
	return list
}
