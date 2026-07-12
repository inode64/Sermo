package cli

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"sermo/internal/assist"
	"sermo/internal/cfgval"
	"sermo/internal/config"
	"sermo/internal/dockerctl"
	"sermo/internal/execx"
	"sermo/internal/procnet"
	"sermo/internal/servicemgr"
	"sermo/internal/virt"

	"github.com/goccy/go-yaml"
)

// listInstalledCatalogServices returns active service targets for the wizard: catalog
// catalog services whose init unit exists, plus active backend units not backed by the
// catalog. Catalog candidates keep their resolved unit/status/default port and
// config-file hints; generic candidates write self-contained check-only service
// watches.
func listInstalledCatalogServices(ctx context.Context, cfg *config.Config, backend servicemgr.Backend, runner execx.Runner, timeout time.Duration) ([]assist.ServiceCandidate, error) {
	if runner == nil {
		runner = execx.CommandRunner{}
	}
	resolver := servicemgr.NewUnitResolver()
	resolver.Runner = runner
	resolver.Timeout = timeout
	manager, _ := servicemgr.NewManager(backend)
	catalogUnits := map[string]struct{}{}
	var out []assist.ServiceCandidate
	for _, name := range cfg.CatalogNamesInCategory(config.CategoryService) {
		resolved, errs := cfg.ResolveCatalog(config.CategoryService, name)
		if len(errs) > 0 || resolved.Tree == nil {
			continue
		}
		candidates, _ := config.ServiceCandidates(resolved.Tree, string(backend), name)
		addWizardCatalogUnits(catalogUnits, backend, candidates...)
		unit, status, err := resolveWizardServiceUnit(ctx, resolver, manager, backend, candidates)
		if err != nil {
			continue // not installed on this backend
		}
		addWizardCatalogUnits(catalogUnits, backend, unit)
		c := assist.ServiceCandidate{
			Name:        name,
			Title:       serviceTitle(resolved.Tree, name),
			Unit:        unit,
			Status:      string(status),
			UnitPresent: true,
			Port:        servicePort(resolved.Tree),
			ConfigPaths: existingConfigFiles(resolved.Tree),
		}
		if name == "ceph-mon" {
			if host, port := detectCephMonEndpoint(ctx, runner, timeout, unit); host != "" && port > 0 {
				c.Variables = map[string]any{config.VariableKeyHost: host, config.VariableKeyPort: port}
				c.Port = port
			}
		}
		// Best-effort PID source for the wizard's pidfile/command_match question.
		proc := servicemgr.DetectProcInfo(ctx, runner, nil, backend, unit)
		c.Pidfile, c.Exe, c.Cmd, c.User = proc.Pidfile, proc.Exe, proc.Cmd, proc.User
		if c.Port > 0 {
			c.PortListening = portListening(c.Port)
			if host, ok := portListenerHost(c.Port); ok && serviceHasVariable(resolved.Tree, config.VariableKeyHost) {
				mergeCandidateVariables(&c, map[string]any{config.VariableKeyHost: host})
			}
		}
		out = append(out, c)
	}
	out = dedupeWizardCatalogCandidates(out, backend)

	if units, err := servicemgr.ListActiveUnits(ctx, backend, runner, timeout); err == nil {
		for _, unit := range units {
			if wizardUnitKnown(catalogUnits, backend, unit) {
				continue
			}
			name := wizardServiceNameForUnit(backend, unit)
			if name == "" {
				continue
			}
			c := assist.ServiceCandidate{
				Name:        name,
				Title:       name,
				Unit:        unit,
				Status:      string(servicemgr.StatusActive),
				Generic:     true,
				UnitPresent: true,
			}
			proc := servicemgr.DetectProcInfo(ctx, runner, nil, backend, unit)
			c.Pidfile, c.Exe, c.Cmd, c.User = proc.Pidfile, proc.Exe, proc.Cmd, proc.User
			out = append(out, c)
			addWizardCatalogUnits(catalogUnits, backend, unit)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func dedupeWizardCatalogCandidates(cands []assist.ServiceCandidate, backend servicemgr.Backend) []assist.ServiceCandidate {
	seen := map[string]struct{}{}
	out := cands[:0]
	for _, c := range cands {
		if c.Generic || c.Unit == "" {
			out = append(out, c)
			continue
		}
		if wizardUnitKnown(seen, backend, c.Unit) {
			continue
		}
		addWizardCatalogUnits(seen, backend, c.Unit)
		out = append(out, c)
	}
	return out
}

func addWizardCatalogUnits(keys map[string]struct{}, backend servicemgr.Backend, units ...string) {
	for _, unit := range units {
		unit = strings.TrimSpace(unit)
		if unit == "" {
			continue
		}
		keys[unit] = struct{}{}
		if backend == servicemgr.BackendSystemd {
			if !strings.Contains(unit, ".") {
				keys[unit+servicemgr.SystemdServiceSuffix] = struct{}{}
			}
			if name := strings.TrimSuffix(unit, servicemgr.SystemdServiceSuffix); name != unit {
				keys[name] = struct{}{}
			}
		}
	}
}

func wizardUnitKnown(keys map[string]struct{}, backend servicemgr.Backend, unit string) bool {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return true
	}
	if _, ok := keys[unit]; ok {
		return true
	}
	if backend == servicemgr.BackendSystemd {
		if strings.HasSuffix(unit, servicemgr.SystemdServiceSuffix) {
			_, ok := keys[strings.TrimSuffix(unit, servicemgr.SystemdServiceSuffix)]
			return ok
		}
		_, ok := keys[unit+servicemgr.SystemdServiceSuffix]
		return ok
	}
	return false
}

func wizardServiceNameForUnit(backend servicemgr.Backend, unit string) string {
	name := strings.TrimSpace(unit)
	if backend == servicemgr.BackendSystemd {
		name = strings.TrimSuffix(name, servicemgr.SystemdServiceSuffix)
	}
	return name
}

func resolveWizardServiceUnit(ctx context.Context, resolver servicemgr.UnitResolver, manager servicemgr.Manager, backend servicemgr.Backend, candidates []string) (string, servicemgr.Status, error) {
	var firstUnit string
	firstStatus := servicemgr.StatusUnknown
	for _, candidate := range candidates {
		unit, err := resolver.Resolve(ctx, backend, []string{candidate}, false)
		if err != nil {
			continue
		}
		status := serviceUnitStatus(ctx, manager, unit)
		if firstUnit == "" {
			firstUnit, firstStatus = unit, status
		}
		if status == servicemgr.StatusActive {
			return unit, status, nil
		}
	}
	if firstUnit != "" {
		return firstUnit, firstStatus, nil
	}
	unit, err := resolver.Resolve(ctx, backend, candidates, false)
	if err != nil {
		return "", servicemgr.StatusUnknown, err
	}
	return unit, serviceUnitStatus(ctx, manager, unit), nil
}

func serviceUnitStatus(ctx context.Context, manager servicemgr.Manager, unit string) servicemgr.Status {
	if manager == nil {
		return servicemgr.StatusUnknown
	}
	status, err := manager.Status(ctx, unit)
	if err != nil {
		return servicemgr.StatusUnknown
	}
	return status.Status
}

func serviceTitle(tree map[string]any, fallback string) string {
	if s := cfgval.AsString(tree[config.EntryKeyDisplayName]); s != "" {
		return s
	}
	return fallback
}

// servicePort reads the catalog service's default port from its variables (0 if none).
func servicePort(tree map[string]any) int {
	vars, ok := tree[config.SectionVariables].(map[string]any)
	if !ok {
		return 0
	}
	if p, ok := cfgval.Int(vars[config.VariableKeyPort]); ok {
		if cfgval.ValidTCPPort(p) {
			return p
		}
	}
	return 0
}

func serviceHasVariable(tree map[string]any, name string) bool {
	vars, ok := tree[config.SectionVariables].(map[string]any)
	if !ok {
		return false
	}
	_, ok = vars[name]
	return ok
}

func mergeCandidateVariables(c *assist.ServiceCandidate, vars map[string]any) {
	if len(vars) == 0 {
		return
	}
	if c.Variables == nil {
		c.Variables = map[string]any{}
	}
	for key, value := range vars {
		c.Variables[key] = value
	}
}

type cephMonMetadata struct {
	Addrs string `json:"addrs"`
}

const (
	cephCommand            = "ceph"
	cephSubcommandMon      = "mon"
	cephSubcommandMetadata = "metadata"
)

func detectCephMonEndpoint(ctx context.Context, runner execx.Runner, timeout time.Duration, unit string) (string, int) {
	id := cephMonID(unit)
	if id == "" {
		return "", 0
	}
	res, err := execx.Run(ctx, runner, timeout, cephCommand, cephSubcommandMon, cephSubcommandMetadata, id)
	if err != nil || strings.TrimSpace(res.Stdout) == "" {
		return "", 0
	}
	var meta cephMonMetadata
	if err := json.Unmarshal([]byte(res.Stdout), &meta); err != nil {
		return "", 0
	}
	return parseCephMonAddrs(meta.Addrs)
}

func cephMonID(unit string) string {
	unit = strings.TrimSuffix(strings.TrimSpace(unit), servicemgr.SystemdServiceSuffix)
	_, id, ok := strings.Cut(unit, "@")
	if !ok {
		return ""
	}
	return strings.TrimSpace(id)
}

func parseCephMonAddrs(addrs string) (string, int) {
	if host, port, ok := parseCephAddrVersion(addrs, "v2"); ok {
		return host, port
	}
	if host, port, ok := parseCephAddrVersion(addrs, "v1"); ok {
		return host, port
	}
	return "", 0
}

func parseCephAddrVersion(addrs, version string) (string, int, bool) {
	idx := strings.Index(addrs, version+":")
	if idx < 0 {
		return "", 0, false
	}
	rest := addrs[idx+len(version)+1:]
	slash := strings.Index(rest, "/")
	if slash < 0 {
		return "", 0, false
	}
	endpoint := strings.TrimSpace(rest[:slash])
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil {
		colon := strings.LastIndex(endpoint, ":")
		if colon < 0 {
			return "", 0, false
		}
		host, portText = endpoint[:colon], endpoint[colon+1:]
	}
	host = strings.Trim(host, "[]")
	port, err := strconv.Atoi(portText)
	if err != nil || host == "" || !cfgval.ValidTCPPort(port) {
		return "", 0, false
	}
	return host, port, true
}

// existingConfigFiles returns the catalog service's declared `config_files` that exist on
// the host (a catalog hint; empty when not declared).
func existingConfigFiles(tree map[string]any) []string {
	var out []string
	for _, f := range cfgval.StringList(tree[config.ServiceKeyConfigFiles]) {
		if pathExists(f) {
			out = append(out, f)
		}
	}
	return out
}

// portListening reports whether the kernel has a TCP listener or UDP socket on
// port. Reading /proc catches UDP daemons and services bound away from loopback,
// which a TCP dial to 127.0.0.1 cannot see.
func portListening(port int) bool {
	for _, table := range procSocketTables() {
		if procPortListening(table.path, port, table.states) {
			return true
		}
	}
	return false
}

type procSocketTable struct {
	path   string
	states map[string]bool
	ipv6   bool
}

func procSocketTables() []procSocketTable {
	return []procSocketTable{
		{path: procnet.PathTCP, states: map[string]bool{procnet.StateListen: true}},
		{path: procnet.PathTCP6, states: map[string]bool{procnet.StateListen: true}, ipv6: true},
		{path: procnet.PathUDP, states: map[string]bool{procnet.StateUDPReady: true}},
		{path: procnet.PathUDP6, states: map[string]bool{procnet.StateUDPReady: true}, ipv6: true},
	}
}

func procPortListening(path string, port int, states map[string]bool) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	ok, _ := parseProcSocketTable(f, port, states)
	return ok
}

func portListenerHost(port int) (string, bool) {
	var hosts []string
	for _, table := range procSocketTables() {
		hosts = append(hosts, procPortListenerHosts(table.path, port, table.states, table.ipv6)...)
	}
	return specificListenerHost(hosts)
}

func procPortListenerHosts(path string, port int, states map[string]bool, ipv6 bool) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer func() { _ = f.Close() }()
	hosts, _ := parseProcSocketTableHosts(f, port, states, ipv6)
	return hosts
}

func parseProcSocketTable(r io.Reader, port int, states map[string]bool) (bool, error) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < procnet.MinFields || fields[procnet.HeaderIndex] == procnet.HeaderField {
			continue
		}
		if !states[strings.ToUpper(fields[procnet.StateIndex])] {
			continue
		}
		_, portHex, ok := strings.Cut(fields[procnet.LocalAddressIndex], procnet.AddressSeparator)
		if !ok {
			continue
		}
		got, err := strconv.ParseUint(portHex, procnet.HexBase, procnet.PortBits)
		if err != nil {
			continue
		}
		if int(got) == port {
			return true, nil
		}
	}
	return false, sc.Err()
}

func parseProcSocketTableHosts(r io.Reader, port int, states map[string]bool, ipv6 bool) ([]string, error) {
	var hosts []string
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) < procnet.MinFields || fields[procnet.HeaderIndex] == procnet.HeaderField {
			continue
		}
		if !states[strings.ToUpper(fields[procnet.StateIndex])] {
			continue
		}
		hostHex, portHex, ok := strings.Cut(fields[procnet.LocalAddressIndex], procnet.AddressSeparator)
		if !ok {
			continue
		}
		got, err := strconv.ParseUint(portHex, procnet.HexBase, procnet.PortBits)
		if err != nil || int(got) != port {
			continue
		}
		host, ok := procSocketHost(hostHex, ipv6)
		if ok {
			hosts = append(hosts, host)
		}
	}
	return appendUniqueStrings(nil, hosts...), sc.Err()
}

func procSocketHost(hexAddr string, ipv6 bool) (string, bool) {
	if ipv6 {
		return procIPv6Host(hexAddr)
	}
	return procIPv4Host(hexAddr)
}

func procIPv4Host(hexAddr string) (string, bool) {
	if len(hexAddr) != procnet.IPv4HexChars {
		return "", false
	}
	raw, err := strconv.ParseUint(hexAddr, procnet.HexBase, procnet.IPv4Bits)
	if err != nil {
		return "", false
	}
	var b [net.IPv4len]byte
	binary.LittleEndian.PutUint32(b[:], uint32(raw))
	ip := net.IPv4(b[procnet.IPv4Byte0], b[procnet.IPv4Byte1], b[procnet.IPv4Byte2], b[procnet.IPv4Byte3])
	return ip.String(), true
}

func procIPv6Host(hexAddr string) (string, bool) {
	if len(hexAddr) != procnet.IPv6HexChars {
		return "", false
	}
	var b [net.IPv6len]byte
	for i := range procnet.IPv6Words {
		start := i * procnet.IPv6WordHexChars
		raw, err := strconv.ParseUint(hexAddr[start:start+procnet.IPv6WordHexChars], procnet.HexBase, procnet.IPv6WordBits)
		if err != nil {
			return "", false
		}
		binary.LittleEndian.PutUint32(b[i*net.IPv4len:], uint32(raw))
	}
	return net.IP(b[:]).String(), true
}

func specificListenerHost(hosts []string) (string, bool) {
	var specific []string
	for _, host := range appendUniqueStrings(nil, hosts...) {
		ip := net.ParseIP(host)
		if ip == nil || ip.IsUnspecified() || ip.IsLoopback() {
			continue
		}
		specific = append(specific, host)
	}
	if len(specific) != 1 {
		return "", false
	}
	return specific[0], true
}

func pathExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// servicesIncludeDir is the explicit services subdirectory the service wizard
// writes kind:service files into.
const servicesIncludeDir = config.ConfigDirServices

// writeWizardServices renders the generated services, confirms, then writes one
// `kind: service` file per service into the services directory and ensures that
// directory is listed in paths.services.
func (a App) writeWizardServices(p *assist.Prompt, opts options, globalPath string, cfg *config.Config, res assist.Result, env assist.Env) int {
	existing := serviceNameSet(cfg)
	docs := map[string]map[string]any{}
	for name, body := range res.Services {
		if _, dup := existing[name]; dup {
			return a.fail(opts, "service "+name+" is already configured; not overwriting")
		}
		// The services directory determines the kind on load, so no `kind:` is written.
		doc := map[string]any{wizardFieldName: name}
		if b, ok := body.(map[string]any); ok {
			for k, v := range b {
				doc[k] = v
			}
		}
		docs[name] = doc
	}

	preview, err := yaml.Marshal(docsPreview(docs))
	if err != nil {
		return a.fail(opts, fmt.Sprintf("render services: %v", err))
	}
	fmt.Fprintf(a.Stdout, "\nGenerated services (%s):\n\n%s\n", res.Summary, preview)
	if !p.Confirm("Write these service files and enable them?", false) {
		fmt.Fprintln(a.Stdout, "Not written — paste the blocks above into files under a paths.services directory.")
		return exitSuccess
	}

	// Step-9 cleanup: offer to delete managed service files whose catalog service
	// is no longer detected on this host (docs/wizards.md).
	var deletes []string
	for _, dir := range serviceCleanupDirs(globalPath, cfg) {
		more, err := planStaleServiceDeletes(p, dir, detectedTargetKeys(env, wizardAssistantService))
		if err != nil {
			return a.fail(opts, err.Error())
		}
		deletes = append(deletes, more...)
	}
	if err := deleteWizardConfigFiles(deletes); err != nil {
		return a.fail(opts, err.Error())
	}

	dir, written, err := writeServiceFiles(globalPath, docs)
	if err != nil {
		return a.fail(opts, err.Error())
	}
	if len(deletes) > 0 {
		fmt.Fprintf(a.Stdout, "Deleted %d stale service file(s).\n", len(deletes))
	}
	fmt.Fprintf(a.Stdout, "Wrote %d service file(s) under %s. Run `sermoctl daemon reload` to apply.\n", written, dir)
	return exitSuccess
}

func serviceCleanupDirs(globalPath string, _ *config.Config) []string {
	base := filepath.Dir(filepath.Clean(globalPath))
	return []string{filepath.Join(base, servicesIncludeDir)}
}

// planStaleServiceDeletes offers to delete managed `kind: service` files under
// a services dir whose `uses:` catalog service (or name) is no longer in the detected
// set. Mirrors planWizardWatchDeletes for the service wizard; a no-op when
// detection is empty so a valid file is never proposed for deletion.
func planStaleServiceDeletes(p *assist.Prompt, dir string, detected map[string]bool) ([]string, error) {
	if len(detected) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read services directory %s: %w", dir, err)
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
		target := serviceFileTarget(path)
		if target == "" || !serviceTargetFamilyDetected(target, detected) || detected[target] {
			continue
		}
		stale = append(stale, staleFile{path: path, label: path + " (" + target + ")"})
	}
	return confirmStaleDeletes(p, dir, wizardNounService, stale), nil
}

// serviceFileTarget returns the typed target a managed service file controls.
// Catalog/init services use "service:<name>", Docker services use
// "docker:<container>", and libvirt VM services use "vm:<domain>". "" when
// unreadable or not targetable.
func serviceFileTarget(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return ""
	}
	if control, ok := doc[config.SectionControl].(map[string]any); ok {
		switch cfgval.AsString(control[dockerctl.ControlKeyType]) {
		case dockerctl.ControlType:
			return serviceTargetKey(serviceFamilyDocker, cfgval.AsString(control[dockerctl.ControlKeyContainer]))
		case virt.ControlType:
			return serviceTargetKey(serviceFamilyVM, cfgval.AsString(control[virt.ControlKeyDomain]))
		}
	}
	if s, _ := doc[config.ServiceKeyUses].(string); s != "" {
		return serviceTargetKey(wizardNounService, s)
	}
	s, _ := doc[wizardFieldName].(string)
	return serviceTargetKey(wizardNounService, s)
}

func serviceTargetKey(family, name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	return family + serviceTargetSeparator + name
}

func serviceDetectedFamilyKey(family string) string {
	return "__detected:" + family
}

func serviceTargetFamilyDetected(target string, detected map[string]bool) bool {
	family, _, ok := strings.Cut(target, serviceTargetSeparator)
	return ok && detected[serviceDetectedFamilyKey(family)]
}

func docsPreview(docs map[string]map[string]any) []any {
	out := make([]any, 0, len(docs))
	for _, n := range slices.Sorted(maps.Keys(docs)) {
		out = append(out, docs[n])
	}
	return out
}

// writeServiceFiles writes each service doc to its own file under the services
// dir, ensuring that dir is in paths.services.
func writeServiceFiles(globalPath string, docs map[string]map[string]any) (string, int, error) {
	targetDir := filepath.Join(filepath.Dir(filepath.Clean(globalPath)), servicesIncludeDir)
	files, _, err := writeConfigDocs(globalPath, servicesIncludeDir, servicesIncludeDir, targetDir, wizardNounService, docs)
	if err != nil {
		return "", 0, err
	}
	return targetDir, len(files), nil
}

func serviceNameSet(cfg *config.Config) map[string]struct{} {
	out := make(map[string]struct{}, len(cfg.ServiceNames))
	for _, n := range cfg.ServiceNames {
		out[n] = struct{}{}
	}
	return out
}
