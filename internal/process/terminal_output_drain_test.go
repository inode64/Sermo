package process

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func drainFixture(t *testing.T) (outputDrainer, TerminalOutput, *os.File, *[]int) {
	t.Helper()
	r, selectors, _ := outputFixture()
	targets, err := r.scan(t.Context(), selectors)
	if err != nil || len(targets) != 1 {
		t.Fatalf("fixture targets %+v: %v", targets, err)
	}
	target := targets[0]
	closed := []int{}
	capture, err := os.CreateTemp(t.TempDir(), "capture-")
	if err != nil {
		t.Fatal(err)
	}
	data := bytes.NewReader([]byte("recovered output\n"))
	k := outputKernel{
		open: func(pid, _ int) (int, error) { return pid + 1000, nil },
		get: func(pidfd, _, _ int) (int, error) {
			if pidfd == 1042 {
				return 200, nil
			}
			return 201, nil
		},
		close:      func(fd int) error { closed = append(closed, fd); return nil },
		identity:   r.reader.Identity,
		index:      func(int) (int, error) { return 11, nil },
		packetMode: func(int) (int, error) { return 0, nil },
		peer:       func(int) (int, error) { return 202, nil },
		stat:       func(int) (unix.Stat_t, error) { return unix.Stat_t{Mode: unix.S_IFCHR, Dev: 1, Ino: 12, Rdev: 11}, nil },
		flags:      func(int) (int, error) { return unix.O_NONBLOCK, nil },
		read: func(_ int, p []byte) (int, error) {
			n, err := data.Read(p)
			if errors.Is(err, io.EOF) {
				err = unix.EIO
			}
			return n, err
		},
		poll: func(int, time.Duration) (bool, error) { return true, nil },
	}
	d := outputDrainer{kernel: k,
		scan:    func(context.Context, []Selector) ([]TerminalOutput, error) { return []TerminalOutput{target}, nil },
		waiting: func(context.Context, TerminalOutput) (bool, error) { return true, nil },
		capture: func(string) (*os.File, int64, error) { return capture, outputCaptureLimit, nil },
	}
	return d, target, capture, &closed
}

func TestDrainTerminalOutputPreservesBytesAndDescriptors(t *testing.T) {
	d, target, capture, closed := drainFixture(t)
	result, err := d.drain(t.Context(), target, "/var/log/output.log")
	if err != nil || result.Bytes != int64(len("recovered output\n")) || readOutputCapture(t, capture) != "recovered output\n" || len(*closed) != 5 || result.Limited {
		t.Fatalf("drain=%+v error=%v capture=%q closed=%v", result, err, readOutputCapture(t, capture), closed)
	}
}

func TestDrainTerminalOutputRejectsChangedOrUnsafeEvidence(t *testing.T) {
	for _, name := range []string{"changed sample", "unavailable sample", "pidfd unavailable", "recycled owner", "different exe", "different user", "unresolved exe", "borrow denied", "different terminal", "blocking master", "write-only master", "packet mode", "different peer", "regular file peer", "capture unavailable", "capture write failure", "cancelled"} {
		t.Run(name, func(t *testing.T) {
			d, target, capture, _ := drainFixture(t)
			reads := 0
			originalRead := d.kernel.read
			d.kernel.read = func(fd int, p []byte) (int, error) { reads++; return originalRead(fd, p) }
			switch name {
			case "changed sample":
				d.scan = func(context.Context, []Selector) ([]TerminalOutput, error) { return nil, nil }
			case "unavailable sample":
				d.scan = func(context.Context, []Selector) ([]TerminalOutput, error) { return nil, fs.ErrPermission }
			case "pidfd unavailable":
				d.kernel.open = func(int, int) (int, error) { return 0, unix.ENOSYS }
			case "recycled owner", "different exe", "different user", "unresolved exe":
				read := d.kernel.identity
				d.kernel.identity = func(pid int) (Identity, bool) {
					id, ok := read(pid)
					if pid == target.Owner.PID {
						switch name {
						case "recycled owner":
							id.StartTicks++
						case "different exe":
							id.Exe = "/usr/bin/other"
						case "different user":
							id.UID++
						case "unresolved exe":
							id.ExeOK = false
						}
					}
					return id, ok
				}
			case "borrow denied":
				d.kernel.get = func(int, int, int) (int, error) { return 0, unix.EPERM }
			case "different terminal":
				d.kernel.index = func(int) (int, error) { return 12, nil }
			case "blocking master":
				d.kernel.flags = func(int) (int, error) { return 0, nil }
			case "write-only master":
				d.kernel.flags = func(int) (int, error) { return unix.O_NONBLOCK | unix.O_WRONLY, nil }
			case "packet mode":
				d.kernel.packetMode = func(int) (int, error) { return 1, nil }
			case "different peer", "regular file peer":
				d.kernel.stat = func(fd int) (unix.Stat_t, error) {
					s := unix.Stat_t{Mode: unix.S_IFCHR, Dev: 1, Ino: 12, Rdev: 11}
					if fd == 201 {
						if name == "different peer" {
							s.Ino++
						} else {
							s.Mode = unix.S_IFREG
						}
					}
					return s, nil
				}
			case "capture unavailable":
				d.capture = func(string) (*os.File, int64, error) { return nil, 0, unix.ENOSPC }
			case "capture write failure":
				_ = capture.Close()
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "cancelled" {
				cancel()
			}
			_, err := d.drain(ctx, target, "/var/log/output.log")
			if err == nil {
				t.Fatal("unsafe recovery succeeded")
			}
			if name != "capture write failure" && reads != 0 {
				t.Fatalf("unproven terminal was read %d times", reads)
			}
		})
	}
}

func TestDrainTerminalOutputIsBoundedAndStopsWhenReaderReturns(t *testing.T) {
	for _, limited := range []bool{false, true} {
		d, target, capture, _ := drainFixture(t)
		if limited {
			d.capture = func(string) (*os.File, int64, error) { return capture, 4, nil }
		} else {
			d.waiting = func(context.Context, TerminalOutput) (bool, error) { return false, nil }
		}
		res, err := d.drain(t.Context(), target, "/var/log/output.log")
		if err != nil || res.Limited != limited || (limited && res.Bytes != 4) || (!limited && res.Bytes != 0) {
			t.Fatalf("drain=%+v: %v", res, err)
		}
	}
}

func readOutputCapture(t *testing.T, file *os.File) string {
	t.Helper()
	data, err := os.ReadFile(file.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDrainTerminalOutputRejectsDeserializedAuthority(t *testing.T) {
	d, target, _, _ := drainFixture(t)
	data, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var untrusted TerminalOutput
	if err := json.Unmarshal(data, &untrusted); err != nil {
		t.Fatal(err)
	}
	d.scan = func(context.Context, []Selector) ([]TerminalOutput, error) {
		t.Fatal("serialized evidence reached process inspection")
		return nil, nil
	}
	if _, err := d.drain(t.Context(), untrusted, "/var/log/output.log"); !errors.Is(err, ErrTerminalOutputChanged) {
		t.Fatalf("deserialized authority accepted: %v", err)
	}
}

func TestDrainTerminalOutputCancellationAfterPollDoesNotRead(t *testing.T) {
	d, target, _, _ := drainFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d.kernel.poll = func(int, time.Duration) (bool, error) { cancel(); return true, nil }
	d.kernel.read = func(int, []byte) (int, error) {
		t.Fatal("terminal read after cancellation")
		return 0, nil
	}
	if _, err := d.drain(ctx, target, "/var/log/output.log"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestTerminalOutputCaptureSafety(t *testing.T) {
	for _, name := range []string{"private", "symlink", "hard link", "public file", "public directory", "full", "locked"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "output.log")
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			switch name {
			case "symlink":
				link := filepath.Join(dir, "link")
				if err := os.Symlink(path, link); err != nil {
					t.Fatal(err)
				}
				path = link
			case "hard link":
				if err := os.Link(path, filepath.Join(dir, "other")); err != nil {
					t.Fatal(err)
				}
			case "public file":
				if err := os.Chmod(path, 0o644); err != nil {
					t.Fatal(err)
				}
			case "public directory":
				if err := os.Chmod(dir, 0o777); err != nil {
					t.Fatal(err)
				}
			case "full":
				if err := os.Truncate(path, outputCaptureLimit); err != nil {
					t.Fatal(err)
				}
			case "locked":
				locked, _, err := openOutputCapture(path)
				if err != nil {
					t.Fatal(err)
				}
				defer locked.Close()
			}
			capture, budget, err := openOutputCapture(path)
			if capture != nil {
				defer capture.Close()
			}
			if name == "private" {
				if err != nil || budget != outputCaptureLimit {
					t.Fatalf("private capture: budget=%d err=%v", budget, err)
				}
			} else if err == nil {
				t.Fatal("unsafe capture accepted")
			}
		})
	}
}
