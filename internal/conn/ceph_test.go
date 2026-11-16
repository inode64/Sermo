package conn

import (
	"bufio"
	"context"
	"encoding/binary"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseCephBanner(t *testing.T) {
	if m, ok := parseCephBanner([]byte("ceph v2\n")); !ok || m != "v2" {
		t.Fatalf("got %q/%v, want v2/true", m, ok)
	}
	if m, ok := parseCephBanner([]byte("ceph v02")); !ok || m != "v1" {
		t.Fatalf("got %q/%v, want v1/true", m, ok)
	}
	if _, ok := parseCephBanner([]byte("HTTP/1.1")); ok {
		t.Fatal("a non-Ceph banner must be rejected")
	}
}

// serveCeph writes a fixed banner and closes.
func serveCeph(t *testing.T, banner string) int {
	t.Helper()
	return serveBanner(t, banner, nil)
}

func TestCephProbeMsgrV2(t *testing.T) {
	// "ceph v2\n" + the le16 length and feature bits that follow on the wire.
	assertProbeExtra(t, cephProtocol{}, serveCeph(t, "ceph v2\n\x10\x00abcdefgh"), "messenger", "v2")
}

func TestCephProbeMsgrV1(t *testing.T) {
	assertProbeExtra(t, cephProtocol{}, serveCeph(t, "ceph v027\x00\x00"), "messenger", "v1")
}

func TestCephProbeNotCeph(t *testing.T) {
	if _, err := (cephProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveCeph(t, "HTTP/1.1 200 OK\r\n")}); err == nil {
		t.Fatal("a non-Ceph banner must fail the probe")
	}
}

// serveCephAdmin answers one admin-socket command the way a Ceph daemon does:
// it reads the NUL-terminated JSON command and writes reply behind a 32-bit
// big-endian length. announce, when positive, replaces the real length.
func serveCephAdmin(t *testing.T, reply string, announce uint32, got chan<- string) string {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "ceph-osd.0.asok")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		command, err := bufio.NewReader(c).ReadString(0)
		if err != nil {
			return
		}
		if got != nil {
			got <- strings.TrimSuffix(command, "\x00")
		}
		length := announce
		if length == 0 {
			length = uint32(len(reply))
		}
		header := binary.BigEndian.AppendUint32(nil, length)
		_, _ = c.Write(append(header, reply...))
	}()
	return sock
}

func TestCephAdminSocketReportsTheDaemonState(t *testing.T) {
	got := make(chan string, 1)
	sock := serveCephAdmin(t, `{"state":"peon","rank":1}`+"\n", 0, got)
	res, err := cephProtocol{}.Probe(context.Background(), Config{Socket: sock, Query: "mon_status"})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if command := <-got; command != `{"prefix":"mon_status"}` {
		t.Fatalf("command = %q, want the mon_status prefix", command)
	}
	if res.Extra[ExtraKeyState] != "peon" || res.Extra[extraQuery] != "mon_status" || res.Extra[extraSocket] != sock {
		t.Fatalf("extra = %v", res.Extra)
	}
}

// A manager answers `status` with an empty object: alive, with no state to report.
func TestCephAdminSocketDefaultsToStatus(t *testing.T) {
	got := make(chan string, 1)
	sock := serveCephAdmin(t, "{}\n", 0, got)
	res, err := cephProtocol{}.Probe(context.Background(), Config{Socket: sock})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if command := <-got; command != `{"prefix":"status"}` {
		t.Fatalf("command = %q, want the status prefix", command)
	}
	if _, has := res.Extra[ExtraKeyState]; has {
		t.Fatalf("extra = %v, want no state", res.Extra)
	}
}

func TestCephAdminSocketRejectsWhatIsNotAnAnswer(t *testing.T) {
	for name, tc := range map[string]struct {
		reply    string
		announce uint32
		want     string
	}{
		"unknown command": {reply: "ERROR: (22) Invalid argument\nunknown command prefix nope\n", want: "ERROR: (22) Invalid argument"},
		"oversized reply": {reply: "{}", announce: cephAdminMaxReplyBytes + 1, want: "exceeds"},
		"truncated reply": {reply: `{"state":`, announce: 64, want: "admin reply"},
		"empty reply":     {reply: "\n", want: "empty reply"},
	} {
		t.Run(name, func(t *testing.T) {
			sock := serveCephAdmin(t, tc.reply, tc.announce, nil)
			_, err := cephProtocol{}.Probe(context.Background(), Config{Socket: sock, Query: "nope"})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
