package cli

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/goccy/go-yaml"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/config"
)

const mountsConfigDir = "mounts"

// writeWizardMounts renders generated kind: mount files under paths.mounts and
// ensures that directory is loaded as a mount config directory.
func (a App) writeWizardMounts(p *assist.Prompt, opts options, globalPath string, cfg *config.Config, res assist.Result, env assist.Env) int {
	docs := map[string]map[string]any{}
	paths := map[string]string{}
	for name, body := range res.Mounts {
		if _, dup := cfg.Mounts[name]; dup {
			return a.fail(opts, "mount "+name+" is already configured; not overwriting")
		}
		doc := map[string]any{"kind": "mount", "name": name}
		if b, ok := body.(map[string]any); ok {
			for k, v := range b {
				doc[k] = v
			}
		}
		path := filepath.Clean(cfgval.String(doc["path"]))
		if path == "." || path == "" {
			return a.fail(opts, "mount "+name+" has no path; not writing")
		}
		if existing := cfg.MountNameByPath(path); existing != "" {
			return a.fail(opts, fmt.Sprintf("mount path %s is already configured by %s; not overwriting", path, existing))
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
	fmt.Fprintf(a.Stdout, "\nGenerated mount units (%s):\n\n%s\n", res.Summary, preview)
	if !p.Confirm("Write these mount files and enable them?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the blocks above into files under a paths.mounts directory.")
		return exitSuccess
	}

	targetDir := wizardMountTargetDir(globalPath)
	deletes, err := planStaleMountDeletes(p, targetDir, detectedTargetKeys(env, "mount"))
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if err := deleteWizardWatchFiles(deletes); err != nil {
		return a.fail(opts, err.Error())
	}

	dir, written, err := writeMountFiles(globalPath, docs)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d stale mount file(s).\n", len(deletes))
	}
	fmt.Fprintf(a.Stdout, "Wrote %d mount file(s) under %s. Run `sermoctl daemon reload` to apply.\n", written, dir)
	return exitSuccess
}

func wizardMountTargetDir(globalPath string) string {
	return filepath.Join(filepath.Dir(filepath.Clean(globalPath)), mountsConfigDir)
}

func writeMountFiles(globalPath string, docs map[string]map[string]any) (string, int, error) {
	targetDir := wizardMountTargetDir(globalPath)
	files, err := planMountFiles(targetDir, docs)
	if err != nil {
		return "", 0, err
	}
	if _, err := ensureMountDir(globalPath, mountsConfigDir, targetDir); err != nil {
		return "", 0, err
	}
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return "", 0, fmt.Errorf("create %s: %w", targetDir, err)
	}
	for _, file := range files {
		if err := os.WriteFile(file.path, file.data, 0o644); err != nil { //nolint:gosec // config is world-readable by design
			return "", 0, fmt.Errorf("write %s: %w", file.path, err)
		}
	}
	return targetDir, len(files), nil
}

type plannedMountFile struct {
	path string
	data []byte
}

func planMountFiles(targetDir string, docs map[string]map[string]any) ([]plannedMountFile, error) {
	files := make([]plannedMountFile, 0, len(docs))
	for _, name := range slices.Sorted(maps.Keys(docs)) {
		file := filepath.Join(targetDir, watchConfigFileName(name))
		if _, err := os.Stat(file); err == nil {
			return nil, fmt.Errorf("mount file %s already exists; not overwriting", file)
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("stat %s: %w", file, err)
		}
		data, err := yaml.Marshal(docs[name])
		if err != nil {
			return nil, fmt.Errorf("render %s: %w", file, err)
		}
		files = append(files, plannedMountFile{path: file, data: data})
	}
	return files, nil
}

func ensureMountDir(globalPath, relDir, targetDir string) (string, error) {
	orig, err := os.ReadFile(globalPath)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", globalPath, err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(orig, &root); err != nil {
		return "", fmt.Errorf("parse %s: %w", globalPath, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	changed, err := ensureMountsPath(root, filepath.Dir(filepath.Clean(globalPath)), relDir, targetDir)
	if err != nil {
		return "", err
	}
	if !changed {
		return "", nil
	}
	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", globalPath, err)
	}
	bak := globalPath + ".bak"
	if err := os.WriteFile(bak, orig, 0o644); err != nil { //nolint:gosec // config is world-readable by design
		return "", fmt.Errorf("write backup %s: %w", bak, err)
	}
	if err := os.WriteFile(globalPath, out, 0o644); err != nil { //nolint:gosec // config is world-readable by design
		return "", fmt.Errorf("write %s: %w", globalPath, err)
	}
	return bak, nil
}

func ensureMountsPath(root map[string]any, base, relDir, targetDir string) (bool, error) {
	paths, _ := root["paths"].(map[string]any)
	if paths == nil {
		paths = map[string]any{}
		root["paths"] = paths
	}
	list, err := yamlStringList(paths["mounts"])
	if err != nil {
		return false, fmt.Errorf("paths.mounts must be a string or list before wizard can append")
	}
	for _, item := range list {
		if sameConfigPath(base, item, targetDir) {
			return false, nil
		}
	}
	paths["mounts"] = appendUniqueStrings(list, relDir)
	return true, nil
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
		return nil, fmt.Errorf("read mounts directory %s: %w", dir, err)
	}
	var stale []staleFile
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".yml") && !strings.HasSuffix(name, ".yaml") {
			continue
		}
		path := filepath.Join(dir, name)
		target := mountFileTarget(path)
		if target == "" || detected[target] {
			continue
		}
		stale = append(stale, staleFile{path: path, label: path + " (" + target + ")"})
	}
	return confirmStaleDeletes(p, dir, "mount", stale), nil
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
	if kind, _ := doc["kind"].(string); kind != "mount" {
		return ""
	}
	target := cfgval.String(doc["path"])
	if target == "" {
		return ""
	}
	return filepath.Clean(target)
}
