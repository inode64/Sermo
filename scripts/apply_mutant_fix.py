#!/usr/bin/env python3
"""Apply a minimal test fix for a go-mutesting survivor (FAIL line = escaped mutant)."""
from __future__ import annotations

import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parent.parent

# Equivalent mutants: fixes cannot distinguish them on this platform.
SKIP_IDS: dict[str, set[str]] = {
    "cfgval.go": {"49"},  # int64Value dead branch on amd64
}


def read(path: Path) -> str:
    return path.read_text()


def write(path: Path, text: str) -> None:
    path.write_text(text)


def apply_cfgval(test: Path, mid: str) -> None:
    t = read(test)
    if mid in ("8", "12"):
        old = '\t\t{int64(-7), "-7"},\n\t\t{uint64(9), "9"},'
        new = '\t\t{int64(-7), "-7"},\n\t\t{int64(10), "10"}, // FormatInt decimal base (mutant .%s)\n\t\t{uint64(9), "9"},' % mid
        if old not in t:
            raise SystemExit(f"anchor missing for cfgval .{mid}")
        write(test, t.replace(old, new, 1))
        return
    if mid == "13":
        old = '\t\t{uint64(9), "9"},\n\t\t{3.5, "3.5"},'
        new = '\t\t{uint64(9), "9"},\n\t\t{uint64(10), "10"}, // FormatUint decimal base (mutant .13)\n\t\t{3.5, "3.5"},'
        if old not in t:
            raise SystemExit("anchor missing for cfgval .13")
        write(test, t.replace(old, new, 1))
        return
    if mid == "14":
        old = '\t\t{3.5, "3.5"},\n\t\t{1.0, "1"}, // trailing zeros trimmed by FormatFloat -1 precision'
        new = (
            '\t\t{3.5, "3.5"},\n'
            '\t\t{1.234, "1.234"}, // FormatFloat minimum digits (mutant .14 pin)\n'
            '\t\t{1.0, "1"},       // trailing zeros trimmed by FormatFloat -1 precision'
        )
        if old not in t:
            old2 = '\t\t{3.5, "3.5"},\n\t\t{1.0, "1"},'
            new2 = (
                '\t\t{3.5, "3.5"},\n'
                '\t\t{1.234, "1.234"}, // FormatFloat minimum digits (mutant .14 pin)\n'
                '\t\t{1.0, "1"},'
            )
            if old2 not in t:
                raise SystemExit("anchor missing for cfgval .14")
            write(test, t.replace(old2, new2, 1))
            return
        write(test, t.replace(old, new, 1))
        return
    if mid == "19":
        needle = "func TestStringMap(t *testing.T) {"
        insert = """// TestStringArrayBareStringNil pins StringArray !ok -> nil (mutant .19).
func TestStringArrayBareStringNil(t *testing.T) {
\tif got := StringArray("solo"); got != nil {
\t\tt.Errorf("StringArray(bare string) = %#v, want nil", got)
\t}
}

"""
        if "TestStringArrayBareStringNil" in t:
            raise SystemExit("TestStringArrayBareStringNil already present")
        if needle not in t:
            raise SystemExit("anchor missing for cfgval .19")
        write(test, t.replace(needle, insert + needle, 1))
        return
    if mid == "51":
        pairs = [
            (
                "\t\t{int64(6), 6, true},\n\t\t{int64(minInt), minInt, true}, // int64Value min boundary (mutant .50)",
                "\t\t{int64(6), 6, true},\n\t\t{int64(maxInt), maxInt, true}, // int64Value max boundary (mutant .51)\n\t\t{int64(minInt), minInt, true}, // int64Value min boundary (mutant .50)",
            ),
            (
                "\t\t{int64(6), 6, true},\n\t\t{uint64(7), 7, true},",
                "\t\t{int64(6), 6, true},\n\t\t{int64(maxInt), maxInt, true}, // int64Value max boundary (mutant .51)\n\t\t{uint64(7), 7, true},",
            ),
        ]
        for old, new in pairs:
            if old in t:
                write(test, t.replace(old, new, 1))
                return
        raise SystemExit("anchor missing for cfgval .51")
    if mid == "50":
        anchor = "{int64(maxInt), maxInt, true}, // int64Value max boundary (mutant .51)"
        if anchor in t:
            old = f"\t\t{anchor}\n\t\t{{uint64(7), 7, true}},"
            new = f"\t\t{anchor}\n\t\t{{int64(minInt), minInt, true}}, // int64Value min boundary (mutant .50)\n\t\t{{uint64(7), 7, true}},"
        else:
            old = "\t\t{int64(6), 6, true},\n\t\t{uint64(7), 7, true},"
            new = "\t\t{int64(6), 6, true},\n\t\t{int64(minInt), minInt, true}, // int64Value min boundary (mutant .50)\n\t\t{uint64(7), 7, true},"
        if old not in t:
            raise SystemExit("anchor missing for cfgval .50")
        write(test, t.replace(old, new, 1))
        return
    if mid == "62":
        old = '\t\t{8.9, 8, true}, // float truncates\n\t\t{"10", 10, true},'
        new = '\t\t{8.9, 8, true}, // float truncates\n\t\t{10.0, 10, true}, // float64Int ParseInt base (mutant .62)\n\t\t{"10", 10, true},'
        if old not in t:
            raise SystemExit("anchor missing for cfgval .62")
        write(test, t.replace(old, new, 1))
        return
    if mid == "107":
        old = '\t\t{"2T", 2 << 40, true},\n\t\t{"0", 0, false},'
        new = '\t\t{"2T", 2 << 40, true},\n\t\t{"1TiB", 1 << 40, true}, // TiB suffix (mutant .107)\n\t\t{"0", 0, false},'
        if old not in t:
            raise SystemExit("anchor missing for cfgval .107")
        write(test, t.replace(old, new, 1))
        return
    if mid == "113":
        old = '\t\t{"1TiB", 1 << 40, true}, // TiB suffix (mutant .107)\n\t\t{"0", 0, false},'
        new = '\t\t{"1TiB", 1 << 40, true}, // TiB suffix (mutant .107)\n\t\t{"1GiB", 1 << 30, true}, // GiB suffix (mutant .113)\n\t\t{"0", 0, false},'
        if old not in t:
            raise SystemExit("anchor missing for cfgval .113")
        write(test, t.replace(old, new, 1))
        return
    print(f"no fix recipe for cfgval.go.{mid}", file=sys.stderr)
    sys.exit(4)


def apply_lock(test: Path, src_name: str, mid: str) -> None:
    t = read(test)
    if src_name == "lock.go":
        if "TestValidateIdentifier" in t:
            raise SystemExit("TestValidateIdentifier already present")
        add = (
            """

func TestValidateIdentifier(t *testing.T) {
\tcases := []struct {
\t\tname       string
\t\tvalue      string
\t\tallowEmpty bool
\t\twantErr    bool
\t}{
\t\t{"empty disallowed", "", false, true},
\t\t{"dot segment", ".", false, true},
\t\t{"backslash separator", """
            + "`a\\b`"
            + """, false, true},
\t\t{"simple name", "deploy", false, false},
\t}
"""
        )
        add += """
\tfor _, c := range cases {
\t\tt.Run(c.name, func(t *testing.T) {
\t\t\terr := validateIdentifier("lock name", c.value, c.allowEmpty)
\t\t\tif (err != nil) != c.wantErr {
\t\t\t\tt.Fatalf("validateIdentifier(%q) err=%v wantErr=%v", c.value, err, c.wantErr)
\t\t\t}
\t\t})
\t}
}
"""
        write(test, t.rstrip() + add + "\n")
        return
    if src_name == "slotpool.go" and mid == "70":
        needle = "// TestSlotPoolReclaimsExpiredSlot covers the TTL safety net:"
        insert = """// TestSlotPoolDefaultTTL pins ttl<=0 default and sub-second ttl explicit (mutant slotpool .70).
func TestSlotPoolDefaultTTL(t *testing.T) {
\tnow := time.Unix(50_000, 0)
\tproc := fakeProc{alive: map[int]bool{9000: true}, ticks: map[int]uint64{9000: 1}}
\tself := func() (int, uint64) { return 9000, 1 }
\tt.Run("zero uses default", func(t *testing.T) {
\t\tdir := t.TempDir()
\t\tpool := SlotPool{Dir: dir, Slots: 1, TTL: 0, Proc: proc, Now: func() time.Time { return now }, Self: self, Sleep: time.Sleep}
\t\th, err := pool.Acquire(context.Background())
\t\tif err != nil { t.Fatalf("acquire: %v", err) }
\t\tdefer h.Release()
\t\tgot, err := readLockFile(h.path)
\t\tif err != nil { t.Fatalf("read: %v", err) }
\t\tif want := now.Add(time.Hour); !got.ExpiresAt.Equal(want) {
\t\t\tt.Fatalf("ExpiresAt = %v want %v", got.ExpiresAt, want)
\t\t}
\t})
\tt.Run("sub-second ttl stays explicit", func(t *testing.T) {
\t\tdir := t.TempDir()
\t\tpool := SlotPool{Dir: dir, Slots: 1, TTL: time.Nanosecond, Proc: proc, Now: func() time.Time { return now }, Self: self, Sleep: time.Sleep}
\t\th, err := pool.Acquire(context.Background())
\t\tif err != nil { t.Fatalf("acquire: %v", err) }
\t\tdefer h.Release()
\t\tgot, err := readLockFile(h.path)
\t\tif err != nil { t.Fatalf("read: %v", err) }
\t\tif want := now.Add(time.Nanosecond); !got.ExpiresAt.Equal(want) {
\t\t\tt.Fatalf("ExpiresAt = %v want %v", got.ExpiresAt, want)
\t\t}
\t})
}

"""
        if "TestSlotPoolDefaultTTL" in t:
            raise SystemExit("TestSlotPoolDefaultTTL already present")
        if needle not in t:
            raise SystemExit("anchor missing for slotpool .70")
        write(test, t.replace(needle, insert + needle, 1))
        return
    print(f"no fix recipe for {src_name}.{mid}", file=sys.stderr)
    sys.exit(4)


def main() -> None:
    if len(sys.argv) != 4:
        print("usage: apply_mutant_fix.py <source.go> <mutant-id> <test_file>", file=sys.stderr)
        sys.exit(2)
    src = Path(sys.argv[1])
    mid = sys.argv[2]
    test = Path(sys.argv[3])
    base = src.name
    if base in SKIP_IDS and mid in SKIP_IDS[base]:
        print(f"skip equivalent mutant {base}.{mid}", file=sys.stderr)
        sys.exit(3)
    if base == "cfgval.go":
        apply_cfgval(test, mid)
    elif base in ("lock.go", "slotpool.go"):
        apply_lock(test, base, mid)
    else:
        raise SystemExit(f"unsupported source {base}")


if __name__ == "__main__":
    main()