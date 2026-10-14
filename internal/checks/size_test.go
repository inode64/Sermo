package checks

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeSizer drives sizeCheck with scripted sizes and a controllable clock.
type fakeSizer struct {
	sizes []int64
	i     int
	now   time.Time
}

func (f *fakeSizer) sample(context.Context, string, bool) (SizeMeasure, error) {
	s := f.sizes[f.i]
	if f.i < len(f.sizes)-1 {
		f.i++
	}
	return SizeMeasure{Bytes: s, Files: 1}, nil
}

func (f *fakeSizer) clock() time.Time { return f.now }

func newSizeCheck(fz *fakeSizer) *sizeCheck {
	return &sizeCheck{
		name: "s", timeout: time.Second,
		path:    "/x",
		growBy:  gib,
		window:  time.Hour,
		sampler: fz.sample,
		clock:   fz.clock,
		state:   &byteWindow{},
	}
}

const gib = 1 << 30

func TestSizeGrowthAlerts(t *testing.T) {
	fz := &fakeSizer{sizes: []int64{1 * gib, 1 * gib, 3 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(fz)

	// First cycle: baseline only, no growth -> ok (no alert).
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("first cycle must not alert: %s", r.Message)
	}
	// 20 min later, unchanged size -> no alert.
	fz.now = fz.now.Add(20 * time.Minute)
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("steady size must not alert: %s", r.Message)
	}
	// 20 min later, grew 1GiB -> 3GiB (=2GiB over window) -> alert.
	fz.now = fz.now.Add(20 * time.Minute)
	r := c.Run(context.Background())
	if !r.OK {
		t.Fatalf("a 2GiB growth over 40m must alert: %s", r.Message)
	}
	if r.Data[DataKeyGrowthBytes].(int64) != 2*gib {
		t.Fatalf("growth_bytes = %v, want %d", r.Data[DataKeyGrowthBytes], 2*gib)
	}
}

func TestSizeDecreaseDoesNotAlert(t *testing.T) {
	fz := &fakeSizer{sizes: []int64{5 * gib, 1 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(fz)
	_ = c.Run(context.Background()) // baseline 5GiB
	fz.now = fz.now.Add(10 * time.Minute)
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("a shrinking file must never alert: %s", r.Message)
	}
}

func TestSizeWindowPrunesOldGrowth(t *testing.T) {
	// Grows slowly: +0.5GiB every 40min. Over any 1h window the growth is <1GiB,
	// so it must never alert even though the total over 2h is >1GiB.
	fz := &fakeSizer{sizes: []int64{1 * gib, 1*gib + gib/2, 2 * gib, 2*gib + gib/2}, now: time.Unix(0, 0)}
	c := newSizeCheck(fz)
	for step := range 4 {
		if r := c.Run(context.Background()); r.OK {
			t.Fatalf("step %d: slow growth within window must not alert: %s", step, r.Message)
		}
		fz.now = fz.now.Add(40 * time.Minute)
	}
}

// With `within` no longer than the sampling interval (or a late cycle), every
// stored sample is already outside the window when the next one arrives; the
// newest of them must remain the baseline, or growth reads +0 forever.
func TestSizeWindowShorterThanIntervalKeepsLastBaseline(t *testing.T) {
	fz := &fakeSizer{sizes: []int64{1 * gib, 3 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(fz)
	c.window = 30 * time.Second
	if r := c.Run(t.Context()); r.OK {
		t.Fatalf("first cycle must not alert: %s", r.Message)
	}
	fz.now = fz.now.Add(31 * time.Second)
	r := c.Run(t.Context())
	if !r.OK || r.Data[DataKeyGrowthBytes].(int64) != 2*gib {
		t.Fatalf("a 2GiB jump one late cycle later must alert: %+v", r)
	}
	fz.now = fz.now.Add(31 * time.Second)
	if r := c.Run(t.Context()); r.OK {
		t.Fatalf("a settled size must stop alerting once the baseline moved: %s", r.Message)
	}
}

func TestBuildAndRunSizeCheckRealFile(t *testing.T) {
	// End-to-end via Build with the default sampler against a real file.
	path := filepath.Join(t.TempDir(), "f.bin")
	if err := os.WriteFile(path, make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	built, warns := Build(map[string]any{
		"grow": map[string]any{"type": "size", "path": path, "include_hidden": true, "grow_by": "1GB", "within": "1h"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("size check should build: warns=%v", warns)
	}
	sc, ok := built[0].Check.(*sizeCheck)
	if !ok || sc.growBy != 1<<30 || sc.window != time.Hour || !sc.includeHidden {
		t.Fatalf("built = %T %+v", built[0].Check, built[0].Check)
	}
	// First run baselines a small file: no alert, and it reads the real size.
	r := sc.Run(context.Background())
	if r.OK {
		t.Fatalf("baseline run must not alert: %s", r.Message)
	}
	if r.Data[DataKeyCurrentBytes].(int64) != 1024 {
		t.Fatalf("current_bytes = %v, want 1024", r.Data[DataKeyCurrentBytes])
	}
}

func TestDirOrFileSizeSkipsHiddenEntriesByDefault(t *testing.T) {
	root := t.TempDir()
	writeSizedTree(t, root, map[string]int{"visible.txt": 3, ".hidden.txt": 5, ".cache/nested.txt": 7})

	if m, err := dirOrFileSize(context.Background(), root, false); err != nil || m != (SizeMeasure{Bytes: 3, Files: 1}) {
		t.Fatalf("default recursive size = %+v, %v; want 3 bytes in 1 file, nil", m, err)
	}
	if m, err := dirOrFileSize(context.Background(), root, true); err != nil || m != (SizeMeasure{Bytes: 15, Files: 3}) {
		t.Fatalf("include_hidden recursive size = %+v, %v; want 15 bytes in 3 files, nil", m, err)
	}
	hiddenPath := filepath.Join(root, ".hidden.txt")
	if m, err := dirOrFileSize(context.Background(), hiddenPath, false); err != nil || m != (SizeMeasure{Bytes: 5, Files: 1}) {
		t.Fatalf("explicit hidden file size = %+v, %v; want 5 bytes in 1 file, nil", m, err)
	}
}

func TestDirOrFileSizeMissingPathAddsStatContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing")
	_, err := dirOrFileSize(context.Background(), path, false)
	if err == nil || !strings.Contains(err.Error(), "stat size path") {
		t.Fatalf("dirOrFileSize() error = %v, want stat context", err)
	}
}

func TestSizeCheckHonorsCanceledContext(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "f.bin"), make([]byte, 1024), 0o600); err != nil {
		t.Fatal(err)
	}
	c := &sizeCheck{
		name: "s", timeout: time.Second,
		path:   root,
		growBy: 1,
		window: time.Hour,
		state:  &byteWindow{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res := c.Run(ctx)
	if res.OK {
		t.Fatal("canceled size check should fail")
	}
	if !strings.Contains(res.Message, "cancelled") {
		t.Fatalf("message = %q, want cancelled", res.Message)
	}
}

func TestBuildSizeCheckErrors(t *testing.T) {
	cases := []map[string]any{
		{"type": "size", "grow_by": "1GB", "within": "1h"},                     // no path
		{"type": "size", "path": "x", "grow_by": "1GB", "within": "1h"},        // relative path
		{"type": "size", "path": "/x"},                                         // no threshold at all
		{"type": "size", "path": "/x", "within": "1h"},                         // within without grow_by
		{"type": "size", "path": "/x", "grow_by": "nonsense", "within": "1h"},  // bad grow_by
		{"type": "size", "path": "/x", "grow_by": "1GB"},                       // grow_by without within
		{"type": "size", "path": "/x", "grow_by": "1GB", "within": "nope"},     // bad within
		{"type": "size", "path": "/x", "max": "20"},                            // unitless max
		{"type": "size", "path": "/x", "max": "0G"},                            // zero max
		{"type": "size", "path": "/x/[bad", "max": "1G"},                       // malformed glob
		{"type": "size", "path": "/x", "max": "", "grow_by": "", "within": ""}, // every limit empty
	}
	for i, entry := range cases {
		_, warns := Build(map[string]any{"s": entry}, Deps{DefaultTimeout: time.Second})
		if len(warns) == 0 {
			t.Fatalf("case %d should warn: %v", i, entry)
		}
	}
}

func TestHumanizeSigned(t *testing.T) {
	// Negative deltas get a sign and the magnitude of |n|; zero and positive
	// deltas render without a sign.
	if got := HumanizeSignedBytes(-2048); got != "-2 KiB" {
		t.Errorf("HumanizeSignedBytes(-2048) = %q, want -2 KiB", got)
	}
	if got := HumanizeSignedBytes(2048); got != "2 KiB" {
		t.Errorf("HumanizeSignedBytes(2048) = %q, want 2 KiB", got)
	}
	if got := HumanizeSignedBytes(0); got != "0 B" {
		t.Errorf("HumanizeSignedBytes(0) = %q, want 0 B", got)
	}
}

func TestSizeGrowthAtExactThreshold(t *testing.T) {
	// Growth of exactly grow_by trips the check (growth >= growBy, not >).
	fz := &fakeSizer{sizes: []int64{1 * gib, 2 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(fz)
	if r := c.Run(context.Background()); r.OK {
		t.Fatalf("baseline cycle must not alert: %s", r.Message)
	}
	fz.now = fz.now.Add(20 * time.Minute)
	if r := c.Run(context.Background()); !r.OK {
		t.Fatalf("growth of exactly 1GiB == limit must alert: %s", r.Message)
	}
}

type vanishedSizeEntry struct{ fs.DirEntry }

func (vanishedSizeEntry) Name() string               { return "rotated.log" }
func (vanishedSizeEntry) IsDir() bool                { return false }
func (vanishedSizeEntry) Type() fs.FileMode          { return 0 }
func (vanishedSizeEntry) Info() (fs.FileInfo, error) { return nil, fs.ErrNotExist }

// Spool, tmp and rotated log directories lose entries between the directory
// read and the per-entry stat; such an entry no longer takes space and must
// not turn the whole measurement unavailable. Any other error still does.
func TestSizeWalkSkipsEntriesThatVanished(t *testing.T) {
	root := "/var/spool/sermo-test"
	var total SizeMeasure
	walk := sizeWalkFunc(t.Context(), root, true, &total)
	if err := walk(root+"/gone", nil, fs.ErrNotExist); err != nil {
		t.Fatalf("vanished entry: %v, want skipped", err)
	}
	if err := walk(root+"/rotated.log", vanishedSizeEntry{}, nil); err != nil {
		t.Fatalf("entry vanished before stat: %v, want skipped", err)
	}
	if err := walk(root+"/locked", nil, fs.ErrPermission); err == nil {
		t.Fatal("a permission error must still abort the measurement")
	}
	if err := walk(root, nil, fs.ErrNotExist); err == nil {
		t.Fatal("the measured path itself vanishing must still abort")
	}
	if total != (SizeMeasure{}) {
		t.Fatalf("total = %+v, want nothing counted", total)
	}
}

// A max threshold judges the total from the first sample: it is an absolute
// limit, not a growth, so it needs no baseline. It trips strictly above max.
func TestSizeMaxTripsWithoutWindow(t *testing.T) {
	fz := &fakeSizer{sizes: []int64{20 * gib, 20*gib + 1, 19 * gib}, now: time.Unix(0, 0)}
	c := &sizeCheck{name: "s", timeout: time.Second, path: "/x", max: 20 * gib, sampler: fz.sample, clock: fz.clock, state: &byteWindow{}}

	r := c.Run(t.Context())
	if r.OK {
		t.Fatalf("a total equal to max must not trip: %s", r.Message)
	}
	if _, present := r.Data[DataKeyGrowthBytes]; present {
		t.Fatalf("no window configured, growth must not be reported: %v", r.Data)
	}
	if r.Data[DataKeyMaxBytes].(int64) != 20*gib || r.Data[DataKeyValue].(int64) != 20*gib {
		t.Fatalf("max_bytes/value = %v/%v", r.Data[DataKeyMaxBytes], r.Data[DataKeyValue])
	}
	if r = c.Run(t.Context()); !r.OK || !strings.Contains(r.Message, "over max 20 GiB") {
		t.Fatalf("a total over max must trip naming the limit: ok=%v %s", r.OK, r.Message)
	}
	if r = c.Run(t.Context()); r.OK {
		t.Fatalf("a total back under max must recover: %s", r.Message)
	}
}

// max and grow_by are independent conditions on one check: either trips it,
// and the message names the one that did.
func TestSizeMaxAndGrowthAreIndependent(t *testing.T) {
	fz := &fakeSizer{sizes: []int64{10 * gib, 12 * gib, 12 * gib, 30 * gib}, now: time.Unix(0, 0)}
	c := newSizeCheck(fz)
	c.max = 20 * gib

	if r := c.Run(t.Context()); r.OK {
		t.Fatalf("baseline under max must not trip: %s", r.Message)
	}
	fz.now = fz.now.Add(10 * time.Minute)
	r := c.Run(t.Context())
	if !r.OK || !strings.Contains(r.Message, "(max 20 GiB); grew 2 GiB") {
		t.Fatalf("growth alone must trip while under max: ok=%v %s", r.OK, r.Message)
	}
	fz.now = fz.now.Add(2 * time.Hour) // window slid past the jump
	if r = c.Run(t.Context()); r.OK {
		t.Fatalf("settled size under max must recover: %s", r.Message)
	}
	fz.now = fz.now.Add(10 * time.Minute)
	r = c.Run(t.Context())
	if !r.OK || !strings.Contains(r.Message, "over max 20 GiB; grew 18 GiB") {
		t.Fatalf("both conditions must be named when both trip: ok=%v %s", r.OK, r.Message)
	}
	if r.Data[DataKeyValue].(int64) != 18*gib {
		t.Fatalf("value must stay the growth when a window is configured: %v", r.Data[DataKeyValue])
	}
}

// A glob pattern sums every match — files and directory trees — and reports
// the file count; nothing matching is a zero-byte healthy measurement, unlike
// a literal path that is missing.
func TestPathSizeGlobSumsMatches(t *testing.T) {
	root := t.TempDir()
	writeSizedTree(t, root, map[string]int{
		"mariadb-bin.000001": 10, "mariadb-bin.000002": 20, "mariadb-bin.index": 3,
		"mariadb-dir/inner.bin": 100, "mariadb-dir/.hidden": 1000,
		"ib_logfile0": 5000,
	})
	measure, err := pathSize(t.Context(), filepath.Join(root, "mariadb*"), false)
	if err != nil {
		t.Fatal(err)
	}
	if measure.Bytes != 133 || measure.Files != 4 {
		t.Fatalf("glob measure = %+v, want 133 bytes in 4 files", measure)
	}
	measure, err = pathSize(t.Context(), filepath.Join(root, "mariadb*"), true)
	if err != nil || measure.Bytes != 1133 || measure.Files != 5 {
		t.Fatalf("include_hidden glob measure = %+v, %v", measure, err)
	}
	measure, err = pathSize(t.Context(), filepath.Join(root, "nothing-here*"), false)
	if err != nil || measure.Bytes != 0 || measure.Files != 0 {
		t.Fatalf("an unmatched glob must measure zero: %+v, %v", measure, err)
	}
	if _, err = pathSize(t.Context(), filepath.Join(root, "nothing-here"), false); err == nil {
		t.Fatal("a missing literal path must stay unavailable")
	}
	if _, err = pathSize(t.Context(), filepath.Join(root, "[bad"), false); err == nil {
		t.Fatal("a malformed pattern must be an error")
	}
}

func TestBuildSizeCheckMaxOnlyGlob(t *testing.T) {
	root := t.TempDir()
	writeSizedTree(t, root, map[string]int{"a.bin": 1024, "b.bin": 1024})
	built, warns := Build(map[string]any{
		"binlogs": map[string]any{"type": "size", "path": filepath.Join(root, "*.bin"), "max": "1K"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 1 {
		t.Fatalf("max-only size check should build: warns=%v", warns)
	}
	r := built[0].Check.Run(t.Context())
	if !r.OK || r.Data[DataKeyCurrentBytes].(int64) != 2048 || r.Data[DataKeyFiles].(int) != 2 {
		t.Fatalf("2 KiB over a 1 KiB max must trip on the first cycle: ok=%v %s %v", r.OK, r.Message, r.Data)
	}
	if !strings.Contains(r.Message, "in 2 files, over max 1 KiB") {
		t.Fatalf("message = %q", r.Message)
	}
}

// An empty limit counts as absent: a catalog variable set to "" switches that
// half of the check off instead of failing the load.
func TestBuildSizeCheckEmptyHalvesAreAbsent(t *testing.T) {
	built, warns := Build(map[string]any{
		"max-only":    map[string]any{"type": "size", "path": "/x", "max": "20G", "grow_by": "", "within": ""},
		"window-only": map[string]any{"type": "size", "path": "/x", "max": "", "grow_by": "1G", "within": "30m"},
	}, Deps{DefaultTimeout: time.Second})
	if len(warns) != 0 || len(built) != 2 {
		t.Fatalf("both checks should build: warns=%v", warns)
	}
	for _, b := range built {
		c := b.Check.(*sizeCheck)
		switch c.name {
		case "max-only":
			if c.max != 20*gib || c.window != 0 || c.growBy != 0 {
				t.Fatalf("max-only = %+v, want max 20G and no window", c)
			}
		case "window-only":
			if c.max != 0 || c.window != 30*time.Minute || c.growBy != gib {
				t.Fatalf("window-only = %+v, want 1G/30m and no max", c)
			}
		}
	}
}

// The fixed directory of a pattern must exist: a wrong datadir is unavailable,
// never a healthy "no binlogs" reading of 0 bytes.
func TestPathSizeGlobRequiresItsDirectory(t *testing.T) {
	root := t.TempDir()
	if _, err := pathSize(t.Context(), filepath.Join(root, "missing", "*bin*.[0-9]*"), false); err == nil {
		t.Fatal("a pattern under a missing directory must be unavailable")
	}
}
