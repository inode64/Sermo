package config

import (
	"fmt"
	"path/filepath"
)

// localDirSuffix names the per-host override sibling of a configured directory:
// /etc/sermo/services.local adjusts /etc/sermo/services, storages.local adjusts
// storages, and so on for every entry in paths.services, paths.apps,
// paths.notifiers and paths.watches.
//
// The layer is discovered from the directory layout rather than registered in
// sermo.yml on purpose. sermo.yml is regenerated wholesale by the deployment
// scripts, so an override registered there would be silently de-registered by
// the very event this layer exists to survive.
const localDirSuffix = ".local"

// localOverrideDir returns the override sibling of a configured directory.
// filepath.Clean is load-bearing: configured paths reach here uncleaned, so a
// trailing slash would otherwise derive a hidden ".local" *inside* the base
// directory instead of a sibling.
func localOverrideDir(dir string) string {
	return filepath.Clean(dir) + localDirSuffix
}

// isLocalOverrideDir reports whether a configured path already names an override
// directory. Listing one in paths.* would load it twice, once as a base
// directory whose duplicate names are fatal, so validation rejects it.
func isLocalOverrideDir(dir string) bool {
	return filepath.Ext(filepath.Clean(dir)) == localDirSuffix
}

// applyLocalOverrides folds every `<dir>.local` document onto the document of
// the same name, or loads it as an ordinary document when no base exists.
//
// It runs at the very end of Load, after applyOSSelectors, bakeBuiltins,
// expandBindir and materializeVersionTemplates, and that ordering is
// load-bearing in two ways. An `os:` branch is merged *on top of* its
// surrounding map, so an override folded in earlier would silently lose to a
// base `os:` branch. And version templates are materialized by then, so
// apps.local/php8.4.yml adjusts the instance an operator actually sees in
// `sermoctl apps` rather than a `php%v` template that no longer exists.
//
// Because override documents are deliberately not in c.docs, each body is
// prepared here with the same three passes the base documents already went
// through.
func (c *Config) applyLocalOverrides(servicePaths, appPaths, notifierPaths, watchPaths []PathSpec) error {
	kinds := []struct {
		specs []PathSpec
		load  func(dir string, recursive bool) error
	}{
		{servicePaths, c.loadServiceOverrideDir},
		{appPaths, c.loadAppOverrideDir},
		{notifierPaths, c.loadNotifierOverrideDir},
		{watchPaths, c.loadWatchOverrideDir},
	}
	for _, kind := range kinds {
		for _, spec := range uniquePathSpecs(kind.specs) {
			if err := kind.load(localOverrideDir(spec.Path), spec.Recursive); err != nil {
				return err
			}
		}
	}
	return nil
}

// prepareOverrideDocument runs the load-time passes an override document misses
// by not being in c.docs, so it merges in the same shape as its base.
func prepareOverrideDocument(doc *Document) error {
	if err := collapseDocumentOS(doc); err != nil {
		return err
	}
	doc.Body = bindTokensMap(doc.Body, builtinReplacer())
	expandBindirDocument(doc)
	return nil
}

func (c *Config) loadServiceOverrideDir(dir string, recursive bool) error {
	return c.loadKindOverrideDir(dir, kindService, recursive)
}

func (c *Config) loadAppOverrideDir(dir string, recursive bool) error {
	return c.loadKindOverrideDir(dir, kindApp, recursive)
}

// loadKindOverrideDir merges service or app overrides onto the indexed document
// of the same name. With no base document the override is registered as an
// ordinary document, which is what lets an operator add a host-only service
// alongside the generated ones.
func (c *Config) loadKindOverrideDir(dir, kind string, recursive bool) error {
	return loadDocumentTree(dir, kind, recursive, func(doc *Document) error {
		if err := assignKind(doc, kind); err != nil {
			return err
		}
		if err := prepareOverrideDocument(doc); err != nil {
			return err
		}
		if doc.Name == "" {
			return fmt.Errorf("%s: %s override documents must define name", doc.Path, kind)
		}
		base := c.registryFor(doc.registryKey())[doc.Name]
		if base == nil {
			doc.LocalOverride = doc.Path
			c.add(doc)
			return nil
		}
		if base.LocalOverride != "" {
			return fmt.Errorf("%s: %s %q is already overridden by %s", doc.Path, kind, doc.Name, base.LocalOverride)
		}
		// applyDeletes is deliberately not called here: mergedService runs it
		// after the catalog body is merged, and running it now would drop a
		// `delete: true` before it ever met the entry it names.
		base.Body = mergeMaps(base.Body, doc.Body)
		base.LocalOverride = doc.Path
		return nil
	})
}

// registryFor returns the document registry a registry key indexes into.
func (c *Config) registryFor(key string) map[string]*Document {
	switch key {
	case catalogServiceKey:
		return c.CatalogServices
	case kindApp:
		return c.Apps
	case kindLibrary:
		return c.Libraries
	case kindPatterns:
		return c.Patterns
	case kindService:
		return c.Services
	}
	return map[string]*Document{}
}

// loadWatchOverrideDir merges a watch override onto the entry the base
// directories folded into Global.Raw["watches"], or adds it when absent. All
// four classified watch directories share one namespace, so an override may sit
// in whichever `.local` sibling the operator finds natural.
func (c *Config) loadWatchOverrideDir(dir string, recursive bool) error {
	return loadDocumentTree(dir, pathKeyWatches, recursive, func(doc *Document) error {
		if err := prepareOverrideDocument(doc); err != nil {
			return err
		}
		entry, err := watchEntryFromDocument(doc)
		if err != nil {
			return err
		}
		if err := c.claimLocalOverride(pathKeyWatches, doc); err != nil {
			return err
		}
		dst := c.registry(pathKeyWatches)
		if existing, ok := dst[doc.Name].(map[string]any); ok {
			dst[doc.Name] = mergeMaps(existing, entry)
			return nil
		}
		dst[doc.Name] = entry
		return nil
	})
}

// loadNotifierOverrideDir merges a notifier override onto the entry of the same
// name, or adds it when absent. The fragment shape is the same single-entry
// `notifiers:` map the base directories accept.
func (c *Config) loadNotifierOverrideDir(dir string, recursive bool) error {
	return loadDocumentTree(dir, pathKeyNotifiers, recursive, func(doc *Document) error {
		if err := prepareOverrideDocument(doc); err != nil {
			return err
		}
		if _, present := doc.Body[pathKeyNotifiers]; !present {
			return fmt.Errorf("%s: %s override directories only support top-level %s", doc.Path, pathKeyNotifiers, pathKeyNotifiers)
		}
		for key := range doc.Body {
			if key != pathKeyNotifiers {
				return fmt.Errorf("%s: %s fragments only support top-level %s, got %q", doc.Path, pathKeyNotifiers, pathKeyNotifiers, key)
			}
		}
		raw := expandEnvTree(doc.Body[pathKeyNotifiers])
		entries, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: %s must be a mapping", doc.Path, pathKeyNotifiers)
		}
		if len(entries) != 1 {
			return fmt.Errorf("%s: %s fragments must contain exactly one entry", doc.Path, pathKeyNotifiers)
		}
		dst := c.registry(pathKeyNotifiers)
		for name, entry := range entries {
			doc.Name = name
			if err := c.claimLocalOverride(pathKeyNotifiers, doc); err != nil {
				return err
			}
			existing, isMap := dst[name].(map[string]any)
			entryMap, entryIsMap := entry.(map[string]any)
			if isMap && entryIsMap {
				dst[name] = mergeMaps(existing, entryMap)
				continue
			}
			dst[name] = entry
		}
		return nil
	})
}

// claimLocalOverride records that one override owns a watch or notifier name.
// Those live in Global.Raw rather than in a Document, so the one-override-per-name
// rule needs its own ledger.
func (c *Config) claimLocalOverride(kind string, doc *Document) error {
	if c.localOverrides == nil {
		c.localOverrides = map[string]string{}
	}
	key := kind + "/" + doc.Name
	if prev, exists := c.localOverrides[key]; exists {
		return fmt.Errorf("%s: %s %q is already overridden by %s", doc.Path, kind, doc.Name, prev)
	}
	c.localOverrides[key] = doc.Path
	return nil
}
