package cli

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"sort"

	"github.com/goccy/go-yaml"

	"sermo/internal/assist"
	"sermo/internal/config"
	"sermo/internal/volume"
)

// runWizard drives the interactive assistant that generates `watches:` config.
// `sermoctl wizard [name]` runs the named assistant, or lists them to choose.
func (a App) runWizard(_ context.Context, opts options) int {
	globalPath := opts.config
	if globalPath == "" {
		globalPath = config.DefaultGlobalPath
	}
	cfg, err := a.LoadConfig(globalPath)
	if err != nil {
		a.reportError(opts, fmt.Sprintf("load config failed: %v", err))
		return exitRuntimeError
	}

	p := assist.NewPrompt(a.wizardStdin(), a.Stdout)

	as, code := a.selectAssistant(p, opts)
	if as == nil {
		return code
	}

	res, err := as.Run(p, a.wizardEnv(cfg))
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}
	if len(res.Watches) == 0 {
		fmt.Fprintln(a.Stdout, "Nothing selected; no configuration generated.")
		return exitSuccess
	}

	data, err := yaml.Marshal(map[string]any{"watches": res.Watches})
	if err != nil {
		a.reportError(opts, fmt.Sprintf("render config: %v", err))
		return exitRuntimeError
	}
	fmt.Fprintf(a.Stdout, "\nGenerated configuration (%s):\n\n%s\n", res.Summary, data)

	if !p.Confirm("Merge this into "+globalPath+"?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the block above under `watches:` in your config.")
		return exitSuccess
	}
	bak, err := mergeWatches(globalPath, res.Watches)
	if err != nil {
		a.reportError(opts, err.Error())
		return exitRuntimeError
	}
	fmt.Fprintf(a.Stdout, "Merged into %s (backup: %s). Run `sermoctl reload` to apply.\n", globalPath, bak)
	return exitSuccess
}

// selectAssistant resolves the assistant from the first positional argument, or
// asks the user to pick one. It returns a nil assistant with an exit code when
// it cannot proceed.
func (a App) selectAssistant(p *assist.Prompt, opts options) (assist.Assistant, int) {
	if name := opts.service(); name != "" {
		as, ok := assist.Lookup(name)
		if !ok {
			a.reportError(opts, fmt.Sprintf("unknown assistant %q", name))
			return nil, exitUsage
		}
		return as, exitSuccess
	}
	all := assist.Assistants()
	labels := make([]string, len(all))
	for i, as := range all {
		labels[i] = as.Title()
	}
	return all[p.Choose("Which kind of check do you want to add?", labels)], exitSuccess
}

func (a App) wizardStdin() io.Reader {
	if a.Stdin != nil {
		return a.Stdin
	}
	return os.Stdin
}

// wizardEnv builds the host facts an assistant needs. The wizardEnvFunc seam
// lets tests supply controlled volumes/interfaces.
func (a App) wizardEnv(cfg *config.Config) assist.Env {
	if a.wizardEnvFunc != nil {
		return a.wizardEnvFunc(cfg)
	}
	return assist.Env{
		Notifiers:     notifierNames(cfg),
		DefaultNotify: config.NotifyDefault(cfg.Global.Raw),
		Volumes:       listVolumes,
		Ifaces:        listIfaces,
	}
}

// notifierNames returns the configured notifier names, sorted.
func notifierNames(cfg *config.Config) []string {
	raw, _ := cfg.Global.Raw["notifiers"].(map[string]any)
	names := make([]string, 0, len(raw))
	for n := range raw {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func listVolumes() ([]assist.Volume, error) {
	mounts, err := volume.List(nil)
	if err != nil {
		return nil, err
	}
	out := make([]assist.Volume, len(mounts))
	for i, m := range mounts {
		out[i] = assist.Volume{Mountpoint: m.Mountpoint, FSType: m.FSType, Device: m.Device}
	}
	return out, nil
}

func listIfaces() ([]assist.Iface, error) {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	out := make([]assist.Iface, 0, len(ifs))
	for _, in := range ifs {
		out = append(out, assist.Iface{
			Name:     in.Name,
			Up:       in.Flags&net.FlagUp != 0 && in.Flags&net.FlagRunning != 0,
			Loopback: in.Flags&net.FlagLoopback != 0,
		})
	}
	return out, nil
}

// mergeWatches merges fragment (watch name -> entry) into the global config's
// `watches:` section. It writes a `.bak` of the original first and refuses to
// overwrite an existing watch of the same name. Re-serializing drops comments
// and may reorder keys, which is why the original is backed up.
func mergeWatches(path string, fragment map[string]any) (string, error) {
	orig, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	var root map[string]any
	if err := yaml.Unmarshal(orig, &root); err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	watches, _ := root["watches"].(map[string]any)
	if watches == nil {
		watches = map[string]any{}
	}
	for name, entry := range fragment {
		if _, exists := watches[name]; exists {
			return "", fmt.Errorf("watch %q already exists in %s; not overwriting", name, path)
		}
		watches[name] = entry
	}
	root["watches"] = watches

	out, err := yaml.Marshal(root)
	if err != nil {
		return "", fmt.Errorf("render %s: %w", path, err)
	}
	bak := path + ".bak"
	if err := os.WriteFile(bak, orig, 0o644); err != nil { //nolint:gosec // config is world-readable by design
		return "", fmt.Errorf("write backup %s: %w", bak, err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil { //nolint:gosec // config is world-readable by design
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return bak, nil
}
