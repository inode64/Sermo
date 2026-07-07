package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/goccy/go-yaml"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/config"
)

const (
	storagesConfigDir = "storages"
)

// writeWizardMounts renders generated storage files under paths.storages and
// ensures that directory is loaded as a storage config directory.
func (a App) writeWizardMounts(p *assist.Prompt, opts options, globalPath string, cfg *config.Config, res assist.Result, env assist.Env) int {
	docs := map[string]map[string]any{}
	paths := map[string]string{}
	for name, body := range res.Mounts {
		if _, dup := cfg.Storages[name]; dup {
			return a.fail(opts, "storage "+name+" is already configured; not overwriting")
		}
		// The storages directory determines the kind on load, so no `kind:` is written.
		doc := wizardMountStorageDoc(name, body)
		if b, ok := body.(map[string]any); ok {
			for k, v := range b {
				switch k {
				case config.MountKeyRefcount, config.MountKeyUmount, config.MountKeyStopPolicy, config.StorageKeyMount:
					// Moved into mount: below.
				default:
					doc[k] = v
				}
			}
		}
		path := filepath.Clean(cfgval.String(doc[wizardFieldPath]))
		if path == "." || path == "" {
			return a.fail(opts, "mount "+name+" has no path; not writing")
		}
		if existing := cfg.StorageNameByPath(path); existing != "" {
			return a.fail(opts, fmt.Sprintf("storage path %s is already configured by %s; not overwriting", path, existing))
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
	fmt.Fprintf(a.Stdout, "\nGenerated storage mount units (%s):\n\n%s\n", res.Summary, preview)
	if !p.Confirm("Write these storage files and enable them?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the blocks above into files under a paths.storages directory.")
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
		fmt.Fprintf(a.Stdout, "Deleted %d stale storage file(s).\n", len(deletes))
	}
	fmt.Fprintf(a.Stdout, "Wrote %d storage file(s) under %s. Run `sermoctl daemon reload` to apply.\n", written, dir)
	return exitSuccess
}

func wizardMountStorageDoc(name string, body any) map[string]any {
	doc := map[string]any{wizardFieldName: name}
	mount := map[string]any{}
	if b, ok := body.(map[string]any); ok {
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
	doc[config.StorageKeyMount] = mount
	return doc
}

func wizardMountTargetDir(globalPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(globalPath)), storagesConfigDir)
}

func writeMountFiles(globalPath string, docs map[string]map[string]any) (string, int, error) {
	targetDir := wizardMountTargetDir(globalPath)
	files, _, err := writeConfigDocs(globalPath, storagesConfigDir, storagesConfigDir, targetDir, wizardNounStorage, docs)
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
		return nil, fmt.Errorf("read storages directory %s: %w", dir, err)
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
	// Files in the storages directory are storage targets by location; a `kind:` is optional
	// and only rejected when it explicitly disagrees.
	if kind, _ := doc[wizardFieldKind].(string); kind != "" && kind != wizardNounStorage {
		return ""
	}
	if _, ok := doc[config.StorageKeyMount].(map[string]any); !ok {
		return ""
	}
	target := cfgval.String(doc[wizardFieldPath])
	if target == "" {
		return ""
	}
	return filepath.Clean(target)
}
