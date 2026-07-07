package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/checks"
	"sermo/internal/config"
)

// writeWizardMounts renders generated mount watches under paths.watches.
func (a App) writeWizardMounts(p *assist.Prompt, opts options, globalPath string, cfg *config.Config, res assist.Result, env assist.Env) int {
	docs := map[string]map[string]any{}
	paths := map[string]string{}
	watches, _ := cfg.ResolveWatches()
	for name, body := range res.Mounts {
		if _, dup := watches[name]; dup {
			return a.fail(opts, "watch "+name+" is already configured; not overwriting")
		}
		doc := wizardMountStorageDoc(name, body)
		check, _ := doc[wizardFieldCheck].(map[string]any)
		path := filepath.Clean(cfgval.String(check[wizardFieldPath]))
		if path == "." || path == "" {
			return a.fail(opts, "mount "+name+" has no path; not writing")
		}
		if existing := cfg.StorageNameByPath(path); existing != "" {
			return a.fail(opts, fmt.Sprintf("storage path %s is already configured by watch %s; not overwriting", path, existing))
		}
		if prev := paths[path]; prev != "" {
			return a.fail(opts, fmt.Sprintf("mount path %s selected more than once (%s and %s)", path, prev, name))
		}
		paths[path] = name
		docs[name] = doc
	}

	preview, err := yaml.Marshal(docsPreview(docs))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("render mounts: %v", err))
	}
	fmt.Fprintf(a.Stdout, "\nGenerated mount watches (%s):\n\n%s\n", res.Summary, preview)
	if !p.Confirm("Write these mount watch files and enable them?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the blocks above into files under a directory listed in paths.watches.")
		return exitSuccess
	}

	targetDir := wizardMountTargetDir(globalPath)
	deletes, err := planStaleMountDeletes(p, targetDir, detectedTargetKeys(env, wizardAssistantMount))
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if err := deleteWizardConfigFiles(deletes); err != nil {
		return a.fail(opts, err.Error())
	}

	dir, written, err := writeMountFiles(globalPath, docs)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d stale mount watch file(s).\n", len(deletes))
	}
	fmt.Fprintf(a.Stdout, "Wrote %d mount watch file(s) under %s. Run `sermoctl daemon reload` to apply.\n", written, dir)
	return exitSuccess
}

func wizardMountStorageDoc(name string, body any) map[string]any {
	doc := map[string]any{wizardFieldName: name}
	mount := map[string]any{}
	check := map[string]any{
		checks.CheckKeyType: checks.CheckTypeStorage,
		"mounted":           true,
	}
	if b, ok := body.(map[string]any); ok {
		for _, key := range []string{config.EntryKeyDisplayName, config.EntryKeyDescription, config.EntryKeyCategory, config.EntryKeyMonitor, config.EntryKeyInterval} {
			if v, present := b[key]; present {
				doc[key] = v
			}
		}
		if existing, ok := b[config.WatchKeyCheck].(map[string]any); ok {
			for k, v := range existing {
				check[k] = v
			}
		}
		for _, key := range []string{config.MountKeyRefcount, config.MountKeyUmount, config.MountKeyStopPolicy} {
			if v, present := b[key]; present {
				mount[key] = v
			}
		}
		if existing, ok := b[config.StorageKeyMount].(map[string]any); ok {
			for k, v := range existing {
				mount[k] = v
			}
		}
	}
	if len(mount) == 0 {
		mount[config.MountKeyRefcount] = true
	}
	doc[wizardFieldCheck] = check
	doc[config.StorageKeyMount] = mount
	return doc
}

func wizardMountTargetDir(globalPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(globalPath)), mountsConfigDir)
}

func writeMountFiles(globalPath string, docs map[string]map[string]any) (string, int, error) {
	targetDir := wizardMountTargetDir(globalPath)
	files, _, err := writeConfigDocs(globalPath, watchesConfigDir, mountsConfigDir, targetDir, wizardNounWatch, docs)
	if err != nil {
		return "", 0, err
	}
	return targetDir, len(files), nil
}

func planStaleMountDeletes(p *assist.Prompt, dir string, detected map[string]bool) ([]string, error) {
	if len(detected) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read mount watches directory %s: %w", dir, err)
	}
	var stale []staleFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, yamlFileExt) && !strings.HasSuffix(name, yamlLongFileExt) {
			continue
		}
		path := filepath.Join(dir, name)
		target := mountFileTarget(path)
		if target == "" || detected[target] {
			continue
		}
		stale = append(stale, staleFile{path: path, label: path + " (" + target + ")"})
	}
	return confirmStaleDeletes(p, dir, wizardNounMount, stale), nil
}

func mountFileTarget(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	if kind, _ := doc[wizardFieldKind].(string); kind != "" && kind != wizardNounWatch {
		return ""
	}
	if _, ok := doc[config.StorageKeyMount].(map[string]any); !ok {
		return ""
	}
	check, _ := doc[wizardFieldCheck].(map[string]any)
	if cfgval.String(check[wizardFieldType]) != checks.CheckTypeStorage {
		return ""
	}
	target := cfgval.String(check[wizardFieldPath])
	if target == "" {
		return ""
	}
	return filepath.Clean(target)
}
