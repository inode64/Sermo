package servicemgr

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"
)

func nativeOpenRCFixture(files fstest.MapFS) openRCStatusFiles {
	return openRCStatusFiles{
		read: func(path string) ([]byte, error) { return fs.ReadFile(files, strings.TrimPrefix(path, "/")) },
		dirs: func(path string) ([]os.DirEntry, error) { return fs.ReadDir(files, strings.TrimPrefix(path, "/")) },
		stat: func(path string) (os.FileInfo, error) { return fs.Stat(files, strings.TrimPrefix(path, "/")) },
	}
}

func TestNativeOpenRCStatus(t *testing.T) {
	for _, tt := range []struct {
		name, marker, script, record string
		running, change, wantErr     bool
		want                         Status
	}{
		{name: "stopped", want: StatusInactive},
		{name: "oneshot", marker: "started", want: StatusActive},
		{name: "running", marker: "started", record: "pidfile=/run/app.pid\n", running: true, want: StatusActive},
		{name: "crashed", marker: "started", record: "pidfile=/run/app.pid\n", want: StatusFailed},
		{name: "starting", marker: "starting", want: StatusUnknown, wantErr: true},
		{name: "stopping", marker: "stopping", want: StatusUnknown, wantErr: true},
		{name: "inactive marker", marker: "inactive", want: StatusUnknown, wantErr: true},
		{name: "custom", script: "status() { custom_probe; }", want: StatusUnknown, wantErr: true},
		{name: "invalid metadata", marker: "started", record: "pidfile=\n", want: StatusUnknown, wantErr: true},
		{name: "changed", marker: "started", record: "pidfile=/run/app.pid\n", running: true, change: true, want: StatusUnknown, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			files := fstest.MapFS{"etc/init.d/app": {Data: []byte(tt.script)}, "run/openrc": {Mode: fs.ModeDir}}
			if tt.marker != "" {
				files["run/openrc/"+tt.marker+"/app"] = &fstest.MapFile{}
			}
			if tt.record != "" {
				files["run/openrc/daemons/app/001"] = &fstest.MapFile{Data: []byte(tt.record)}
			}
			state, err := nativeOpenRCFixture(files).status(t.Context(), "app", func(_ context.Context, info ProcInfo) (bool, error) {
				if info.Pidfile != "/run/app.pid" {
					t.Fatalf("unexpected record: %+v", info)
				}
				if tt.change {
					delete(files, "run/openrc/started/app")
				}
				return tt.running, nil
			})
			if state != tt.want || (err != nil) != tt.wantErr {
				t.Fatalf("got %s, %v; want %s, error=%v", state, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestNativeOpenRCUncertainty(t *testing.T) {
	files := fstest.MapFS{"etc/init.d/app": {}, "run/openrc/started/app": {}, "run/openrc/daemons/app/001": {Data: []byte("pidfile=/run/app.pid")}}
	f := nativeOpenRCFixture(files)
	if state, err := f.status(t.Context(), "../app", nil); state != StatusUnknown || err == nil {
		t.Fatalf("unsafe unit: %s, %v", state, err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.status(ctx, "app", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: %v", err)
	}
	if state, err := f.status(t.Context(), "app", func(context.Context, ProcInfo) (bool, error) { return false, errors.New("unreadable process") }); state != StatusUnknown || err == nil {
		t.Fatalf("uncertain process: %s, %v", state, err)
	}
}

func TestOpenRCRecordedCommand(t *testing.T) {
	info, err := openRCRecordedDaemon([]byte("exec=/usr/bin/worker\nargv_0=/usr/bin/worker\nargv_1=--group=one\npidfile=\n"))
	if err != nil || info.Cmd != `^/usr/bin/worker --group=one($|[[:space:]])` {
		t.Fatalf("record: %+v, %v", info, err)
	}
}

func TestNativeOpenRCRefusesChroot(t *testing.T) {
	files := fstest.MapFS{"etc/init.d/app": {}, "run/openrc/options/app/chroot": {Data: []byte("/srv/chroot")}}
	state, err := nativeOpenRCFixture(files).status(t.Context(), "app", nil)
	if state != StatusUnknown || err == nil {
		t.Fatalf("chroot state %s, %v", state, err)
	}
}
