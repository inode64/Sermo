package checks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const (
	// inotifyFDTargetPlain and inotifyFDTargetBracketed are the two spellings the
	// kernel uses for an inotify fd's /proc/<pid>/fd symlink target.
	inotifyFDTargetPlain     = "anon_inode:inotify"
	inotifyFDTargetBracketed = "anon_inode:[inotify]"
	// inotifyWatchLinePrefix starts each watch descriptor line of an inotify fd's
	// fdinfo, e.g. "inotify wd:1 ino:38125e sdev:...".
	inotifyWatchLinePrefix = "inotify wd:"
	// inotifyHolderLimit bounds the process names named in the message: enough to
	// identify a leak, short enough for one alert line.
	inotifyHolderLimit = 3
	// inotifyCancelStride bounds how often the walk checks the context, so the
	// check timeout still applies to a host with thousands of processes.
	inotifyCancelStride = 64
	inotifyDimInstances = "instances"
	// statusUIDEffectiveIndex is the effective uid's position in a
	// /proc/<pid>/status "Uid:" line (real, effective, saved, filesystem). The
	// kernel charges both inotify limits to the effective uid.
	statusUIDEffectiveIndex = 1
	inotifyDimWatches       = "watches"
)

// InotifyHolder is one process holding inotify instances, for the message.
type InotifyHolder struct {
	Command   string
	Instances uint64
}

// InotifyUserUsage is one user's inotify usage. Both kernel limits are charged
// per user, so this is the unit the check compares against them.
type InotifyUserUsage struct {
	UID       uint32
	Instances uint64
	Watches   uint64
	Holders   []InotifyHolder
}

// InotifySample is one observation of the host's inotify usage: the two kernel
// limits and the per-user usage of every user holding at least one instance.
type InotifySample struct {
	MaxInstances uint64
	MaxWatches   uint64
	Users        []InotifyUserUsage
	// WatchesRead reports whether watch descriptors were counted; they are only
	// read when a predicate needs them.
	WatchesRead bool
	// Unreadable counts processes whose fd table was permission-denied, which
	// makes the usage a lower bound rather than the truth.
	Unreadable uint64
}

// InotifySamplerFunc reads the host's inotify usage. countWatches asks for the
// more expensive watch-descriptor count. Injected for tests; the default walks
// /proc.
type InotifySamplerFunc func(ctx context.Context, countWatches bool) (InotifySample, error)

// inotifyCheck is a level check for the per-user inotify limits.
//
// It exists because no other check can see this exhaustion: `fds` compares
// system-wide allocated descriptors against fs.file-max, which is effectively
// unlimited on a modern host, so a host whose uid 0 held all 1024 inotify
// instances — no new user manager, no new session bus, systemd degraded —
// reported fds at 0.0%.
type inotifyCheck struct {
	base
	preds   []levelPred
	sampler InotifySamplerFunc
}

func (c inotifyCheck) Run(ctx context.Context) Result {
	ctx, run := c.begin(ctx)
	defer run.close()

	sampler := c.sampler
	if sampler == nil {
		sampler = defaultInotifySampler
	}
	sample, err := sampler(ctx, c.needsWatches())
	if err != nil {
		return c.unavailableResult(CheckTypeInotify+": "+err.Error(), run.start)
	}
	if sample.MaxInstances == 0 && sample.MaxWatches == 0 {
		return c.unavailableResult(CheckTypeInotify+": kernel exposes no inotify limits", run.start)
	}

	values, worst := inotifyValues(sample)
	held := levelPredsHold(c.preds, values)
	// Permission-denied fd tables make every count a lower bound: report that
	// rather than a reassuring ok derived from what little was readable.
	if !held && sample.Unreadable > 0 {
		return c.unavailableResult(fmt.Sprintf("%s: %d process fd table(s) unreadable, usage is a lower bound",
			CheckTypeInotify, sample.Unreadable), run.start)
	}
	res := c.result(held, inotifyMessage(sample, worst), run.start)
	res.Data = inotifyData(sample, worst, values, c.preds)
	return res
}

// needsWatches reports whether any configured predicate reads the watch
// dimension. Counting watch descriptors costs one fdinfo read per inotify fd, so
// a check that only asks about instances never pays for it — the same
// only-measure-what-a-predicate-needs rule the hdparm check follows.
func (c inotifyCheck) needsWatches() bool {
	for _, pred := range c.preds {
		switch pred.field {
		case fieldWatches, fieldWatchesUsedPct, fieldWatchesFree, fieldUsedPct:
			return true
		}
	}
	return false
}

// inotifyWorst names the user closest to each limit. The two can be different
// users — root leaking instances while a developer holds every watch — so each
// dimension keeps its own.
type inotifyWorst struct {
	instances InotifyUserUsage
	watches   InotifyUserUsage
	dimension string
}

// inotifyValues projects the sample onto the predicate fields. Both limits are
// per user, so the worst single user is what matters: summing users would report
// 120% while nobody is anywhere near being denied an inotify_init.
func inotifyValues(s InotifySample) (map[string]float64, inotifyWorst) {
	var worst inotifyWorst
	for _, user := range s.Users {
		if user.Instances > worst.instances.Instances {
			worst.instances = user
		}
		if user.Watches > worst.watches.Watches {
			worst.watches = user
		}
	}
	values := map[string]float64{
		fieldInstances: float64(worst.instances.Instances),
		fieldWatches:   float64(worst.watches.Watches),
	}
	instancesPct := levelCountFields(values, fieldInstancesUsedPct, fieldInstancesFree, worst.instances.Instances, s.MaxInstances)
	watchesPct := 0.0
	if s.WatchesRead {
		watchesPct = levelCountFields(values, fieldWatchesUsedPct, fieldWatchesFree, worst.watches.Watches, s.MaxWatches)
	}
	// used_pct is the worse of the two: level predicates are ANDed, so a watch
	// carrying both dimensions would have stayed silent on a host at 100% of the
	// instance limit and 0.7% of the watch limit. One headline field keeps a
	// single-predicate watch from being blind to one limit.
	worst.dimension = inotifyDimInstances
	if watchesPct > instancesPct {
		worst.dimension = inotifyDimWatches
	}
	values[fieldUsedPct] = max(instancesPct, watchesPct)
	return values, worst
}

func inotifyMessage(s InotifySample, worst inotifyWorst) string {
	var b strings.Builder
	fmt.Fprintf(&b, "inotify instances %d/%d uid %d", worst.instances.Instances, s.MaxInstances, worst.instances.UID)
	if s.WatchesRead {
		fmt.Fprintf(&b, ", watches %d/%d uid %d", worst.watches.Watches, s.MaxWatches, worst.watches.UID)
	}
	// Name the holders: on the host this check was written for, "dbus-daemon
	// (1005)" is the whole diagnosis.
	if holders := inotifyHolderText(worst.instances.Holders); holders != "" {
		b.WriteString("; top " + holders)
	}
	return b.String()
}

func inotifyHolderText(holders []InotifyHolder) string {
	parts := make([]string, 0, len(holders))
	for _, holder := range holders {
		parts = append(parts, fmt.Sprintf("%s (%d)", holder.Command, holder.Instances))
	}
	return strings.Join(parts, ", ")
}

func inotifyData(s InotifySample, worst inotifyWorst, values map[string]float64, preds []levelPred) map[string]any {
	data := map[string]any{
		DataKeyInstances:      worst.instances.Instances,
		DataKeyInstancesMax:   s.MaxInstances,
		DataKeyInstancesUID:   worst.instances.UID,
		DataKeyDimension:      worst.dimension,
		DataKeyUsers:          uint64(len(s.Users)),
		fieldInstancesUsedPct: values[fieldInstancesUsedPct],
		DataKeyValue:          firstPredValue(preds, values, values[fieldUsedPct]),
		DataKeyUsedPct:        values[fieldUsedPct],
	}
	if s.WatchesRead {
		data[DataKeyWatches] = worst.watches.Watches
		data[DataKeyWatchesMax] = s.MaxWatches
		data[DataKeyWatchesUID] = worst.watches.UID
		data[fieldWatchesUsedPct] = values[fieldWatchesUsedPct]
	}
	if holders := inotifyHolderText(worst.instances.Holders); holders != "" {
		data[DataKeyHolders] = holders
	}
	if s.Unreadable > 0 {
		data[DataKeyUnreadable] = s.Unreadable
	}
	return data
}

func buildInotifyCheck(b base, entry map[string]any, deps Deps) (Check, string) {
	return buildLevelCheck(entry, InotifyPredFields, CheckTypeInotify+" check", func(preds []levelPred) Check {
		return inotifyCheck{base: b, preds: preds, sampler: deps.InotifySampler}
	})
}

// isInotifyFDTarget reports whether an fd symlink target is an inotify instance.
func isInotifyFDTarget(target string) bool {
	return target == inotifyFDTargetPlain || target == inotifyFDTargetBracketed
}

// countInotifyWatchLines counts the watch descriptors an inotify fd's fdinfo
// reports. It streams: one instance watching a large tree produces a line per
// watch, which is megabytes the kernel formats on demand.
func countInotifyWatchLines(r io.Reader) (uint64, error) {
	var watches uint64
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		if strings.HasPrefix(scanner.Text(), inotifyWatchLinePrefix) {
			watches++
		}
	}
	if err := scanner.Err(); err != nil {
		return watches, fmt.Errorf("read inotify fdinfo: %w", err)
	}
	return watches, nil
}

// topHolders reduces per-command instance counts to the few worth naming, most
// instances first and ties broken by name so the message is stable.
func topHolders(byCommand map[string]uint64, limit int) []InotifyHolder {
	holders := make([]InotifyHolder, 0, len(byCommand))
	for command, instances := range byCommand {
		holders = append(holders, InotifyHolder{Command: command, Instances: instances})
	}
	sort.Slice(holders, func(i, j int) bool {
		if holders[i].Instances != holders[j].Instances {
			return holders[i].Instances > holders[j].Instances
		}
		return holders[i].Command < holders[j].Command
	})
	if len(holders) > limit {
		holders = holders[:limit]
	}
	return holders
}

// defaultInotifySampler reads the kernel limits and walks /proc.
func defaultInotifySampler(ctx context.Context, countWatches bool) (InotifySample, error) {
	return readInotifyUsage(ctx, procRootPath, countWatches)
}

// readInotifyUsage walks a procfs root and totals inotify usage per user. The
// root is a parameter so the walk is testable against a fixture tree.
//
// Cost is one readlink per open descriptor for instances, and — only when asked
// — one streamed fdinfo read per descriptor already known to be an inotify one,
// so the watch count is proportional to inotify fds and not to all fds.
func readInotifyUsage(ctx context.Context, root string, countWatches bool) (InotifySample, error) {
	sample := InotifySample{WatchesRead: countWatches,
		MaxInstances: readInotifyLimit(root, "max_user_instances"),
		MaxWatches:   readInotifyLimit(root, "max_user_watches")}

	entries, err := os.ReadDir(root)
	if err != nil {
		return InotifySample{}, fmt.Errorf("read %s: %w", root, err)
	}
	type usage struct {
		instances uint64
		watches   uint64
		commands  map[string]uint64
	}
	byUID := map[uint32]*usage{}
	for i, entry := range entries {
		if i%inotifyCancelStride == 0 {
			if err := ctx.Err(); err != nil {
				return InotifySample{}, fmt.Errorf("walk %s: %w", root, err)
			}
		}
		pid, convErr := strconv.Atoi(entry.Name())
		if convErr != nil || pid <= 0 {
			continue
		}
		instances, watches, unreadable := readPIDInotify(filepath.Join(root, entry.Name()), countWatches)
		sample.Unreadable += unreadable
		if instances == 0 {
			continue
		}
		// uid and command are read only for a process that holds an instance,
		// which is a handful of processes rather than all of them.
		uid, ok := readPIDUID(filepath.Join(root, entry.Name()))
		if !ok {
			continue
		}
		bucket := byUID[uid]
		if bucket == nil {
			bucket = &usage{commands: map[string]uint64{}}
			byUID[uid] = bucket
		}
		bucket.instances += instances
		bucket.watches += watches
		bucket.commands[readPIDCommand(filepath.Join(root, entry.Name()))] += instances
	}
	for uid, bucket := range byUID {
		sample.Users = append(sample.Users, InotifyUserUsage{
			UID:       uid,
			Instances: bucket.instances,
			Watches:   bucket.watches,
			Holders:   topHolders(bucket.commands, inotifyHolderLimit),
		})
	}
	slices.SortFunc(sample.Users, func(a, b InotifyUserUsage) int { return int(a.UID) - int(b.UID) })
	return sample, nil
}

func readInotifyLimit(root, name string) uint64 {
	data, err := os.ReadFile(filepath.Join(root, "sys", "fs", "inotify", name)) //nolint:gosec // G304: a path under the configured procfs root
	if err != nil {
		return 0
	}
	limit, err := strconv.ParseUint(strings.TrimSpace(string(data)), numericBaseDecimal, numericBits64)
	if err != nil {
		return 0
	}
	return limit
}

// readPIDInotify counts one process's inotify descriptors, and their watches when
// asked. A process that exits mid-walk is skipped silently; a permission-denied
// fd table is counted, because it makes the total a lower bound.
func readPIDInotify(pidPath string, countWatches bool) (instances, watches, unreadable uint64) {
	fdDir := filepath.Join(pidPath, "fd")
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		if errorIsPermission(err) {
			return 0, 0, 1
		}
		return 0, 0, 0
	}
	for _, entry := range entries {
		target, err := os.Readlink(filepath.Join(fdDir, entry.Name()))
		if err != nil || !isInotifyFDTarget(target) {
			continue
		}
		instances++
		if !countWatches {
			continue
		}
		file, err := os.Open(filepath.Join(pidPath, "fdinfo", entry.Name())) //nolint:gosec // G304: a path under the configured procfs root
		if err != nil {
			continue
		}
		count, err := countInotifyWatchLines(file)
		_ = file.Close()
		if err == nil {
			watches += count
		}
	}
	return instances, watches, 0
}

func errorIsPermission(err error) bool {
	return errors.Is(err, fs.ErrPermission)
}

func readPIDUID(pidPath string) (uint32, bool) {
	data, err := os.ReadFile(filepath.Join(pidPath, "status")) //nolint:gosec // G304: a path under the configured procfs root
	if err != nil {
		return 0, false
	}
	return parseStatusEffectiveUID(string(data))
}

// parseStatusEffectiveUID reads the effective uid from a /proc/<pid>/status
// body. The kernel charges both inotify limits to the effective uid, so a
// setuid process counts against the user it runs as.
func parseStatusEffectiveUID(status string) (uint32, bool) {
	for line := range strings.Lines(status) {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Uid:")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) <= statusUIDEffectiveIndex {
			return 0, false
		}
		uid, err := strconv.ParseUint(fields[statusUIDEffectiveIndex], numericBaseDecimal, 32)
		if err != nil {
			return 0, false
		}
		return uint32(uid), true
	}
	return 0, false
}

func readPIDCommand(pidPath string) string {
	data, err := os.ReadFile(filepath.Join(pidPath, "comm")) //nolint:gosec // G304: a path under the configured procfs root
	if err != nil {
		return unnamedProcess
	}
	if command := strings.TrimSpace(string(data)); command != "" {
		return command
	}
	return unnamedProcess
}
