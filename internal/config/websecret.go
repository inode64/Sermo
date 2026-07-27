package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sermo/internal/cfgval"
)

// webPasswordFileKeys pairs each web password key with the `*_file` companion
// that supplies it from disk instead of from sermo.yml. Order matches the
// destination pointers in resolveWebPasswordFiles.
var webPasswordFileKeys = [][2]string{
	{WebKeyPassword, WebKeyPasswordFile},
	{WebKeyGuestPassword, WebKeyGuestPasswordFile},
}

// resolveWebPasswordFiles reads web.password_file / web.guest_password_file
// once at load time and keeps their contents on g, so the dashboard passwords
// need never be written in sermo.yml. A relative path is taken relative to the
// directory holding sermo.yml, like the paths.* directories.
//
// A read failure is recorded as a validation issue rather than a load error:
// an operator running `config validate` without access to a root-owned secret
// file gets a clear message instead of a loader that refuses to start.
func resolveWebPasswordFiles(g *Global) {
	web, ok := g.Raw[SectionWeb].(map[string]any)
	if !ok {
		return
	}
	base := configBaseDir(g.Path)
	targets := []*string{&g.webPassword, &g.webGuestPassword}
	for i, keys := range webPasswordFileKeys {
		fileKey := keys[1]
		raw, present := web[fileKey]
		if !present {
			continue
		}
		// A non-string or empty value is reported by validateWeb; there is
		// nothing to read here.
		path := cfgval.AsString(raw)
		if path == "" {
			continue
		}
		secret, err := readSecretFile(resolveConfigPath(base, path))
		if err != nil {
			g.issues = append(g.issues, Issue{
				Scope: globalScope,
				Msg:   fmt.Sprintf("%s.%s %v", SectionWeb, fileKey, err),
			})
			continue
		}
		*targets[i] = secret
	}
}

// readSecretFile returns the single secret held in path, with surrounding
// whitespace removed — notably the trailing newline that every editor and
// `echo` appends. An empty file is an error: taken literally it would silently
// leave the dashboard unauthenticated.
func readSecretFile(path string) (string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", fmt.Errorf("cannot be read: %w", err)
	}
	secret := strings.TrimSpace(string(data))
	if secret == "" {
		return "", fmt.Errorf("is empty: %s", path)
	}
	return secret, nil
}

// WebPassword returns the admin dashboard password: the contents of
// web.password_file when that key is used, otherwise web.password.
func (g Global) WebPassword() string {
	return g.webSecret(g.webPassword, WebKeyPassword)
}

// WebGuestPassword returns the read-only dashboard password: the contents of
// web.guest_password_file when that key is used, otherwise web.guest_password.
func (g Global) WebGuestPassword() string {
	return g.webSecret(g.webGuestPassword, WebKeyGuestPassword)
}

// webSecret prefers the value already read from a `*_file` key, falling back to
// the inline key in Raw so a Global built without the loader still works.
func (g Global) webSecret(fromFile, key string) string {
	if fromFile != "" {
		return fromFile
	}
	web, _ := g.Raw[SectionWeb].(map[string]any)
	return cfgval.AsString(web[key])
}
