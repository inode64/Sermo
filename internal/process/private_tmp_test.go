package process

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"
)

// mountinfo as a php-fpm unit saw it after its /tmp/systemd-private-* directory
// was deleted: the host /tmp underneath, the deleted private /tmp on top, and
// the private /var/tmp still in place.
const lostPrivateTmpMountInfo = "790 780 0:31 / /tmp rw,nosuid,nodev - tmpfs tmpfs rw\n" +
	"812 790 0:31 /systemd-private-1a-php-fpm@8.4.service-GD5BVE/tmp//deleted /tmp rw,nosuid,nodev - tmpfs tmpfs rw\n" +
	"813 781 8:2 /var/tmp/systemd-private-1a-php-fpm@8.4.service-5OjSqR/tmp /var/tmp rw - ext4 /dev/root rw\n"

const intactPrivateTmpMountInfo = "790 780 0:31 / /tmp rw - tmpfs tmpfs rw\n" +
	"812 790 0:31 /systemd-private-1a-apache2.service-Ab12Cd/tmp /tmp rw - tmpfs tmpfs rw\n"

// A deleted directory the kernel marks outside the PrivateTmp mounts (a bind
// mount of the unit's own) is not this condition.
const otherDeletedMountInfo = "820 780 8:2 /srv/old//deleted /srv rw - ext4 /dev/root rw\n"

func TestLostPrivateTmpIn(t *testing.T) {
	links := map[string]string{
		"/proc/self/ns/mnt": "mnt:[4026531841]",
		"/proc/10/ns/mnt":   "mnt:[4026532900]",
		"/proc/11/ns/mnt":   "mnt:[4026532900]", // the same unit's worker
		"/proc/20/ns/mnt":   "mnt:[4026531841]", // host namespace: no PrivateTmp
		"/proc/30/ns/mnt":   "mnt:[4026533000]",
		"/proc/40/ns/mnt":   "mnt:[4026533100]",
	}
	files := map[string]string{
		"/proc/10/mountinfo": lostPrivateTmpMountInfo,
		"/proc/20/mountinfo": lostPrivateTmpMountInfo, // never read: host namespace
		"/proc/30/mountinfo": intactPrivateTmpMountInfo,
		"/proc/40/mountinfo": otherDeletedMountInfo,
	}
	reads := map[string]int{}
	s := privateTmpScanner{
		readLink: func(path string) (string, error) {
			if link, ok := links[path]; ok {
				return link, nil
			}
			return "", fs.ErrNotExist
		},
		readFile: func(path string) ([]byte, error) {
			reads[path]++
			if data, ok := files[path]; ok {
				return []byte(data), nil
			}
			return nil, fs.ErrNotExist
		},
	}
	for name, tc := range map[string]struct {
		procs []Process
		want  []LostPrivateTmp
	}{
		"lost /tmp, read once per namespace": {
			procs: []Process{{PID: 10}, {PID: 11}},
			want:  []LostPrivateTmp{{PID: 10, MountPoint: "/tmp", Root: "/systemd-private-1a-php-fpm@8.4.service-GD5BVE/tmp//deleted"}},
		},
		"host namespace":                 {procs: []Process{{PID: 20}}},
		"intact private tmp":             {procs: []Process{{PID: 30}}},
		"other deleted mount":            {procs: []Process{{PID: 40}}},
		"process gone during the scan":   {procs: []Process{{PID: 99}}},
		"delegated and external skipped": {procs: []Process{{PID: 10, Delegated: true}, {PID: 11, External: true}}},
	} {
		t.Run(name, func(t *testing.T) {
			clear(reads)
			got, err := s.lost(tc.procs)
			if err != nil || !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("lost() = %+v, %v; want %+v", got, err, tc.want)
			}
			if reads["/proc/11/mountinfo"] != 0 || reads["/proc/20/mountinfo"] != 0 {
				t.Fatalf("read a namespace twice or the host's: %v", reads)
			}
		})
	}
}

// An unreadable namespace or mount table proves nothing, so the scan fails
// instead of reporting a clean unit.
func TestLostPrivateTmpInReadErrors(t *testing.T) {
	denied := errors.New("permission denied")
	for name, s := range map[string]privateTmpScanner{
		"own namespace": {readLink: func(string) (string, error) { return "", denied }},
		"process namespace": {readLink: func(path string) (string, error) {
			if path == "/proc/self/ns/mnt" {
				return "mnt:[1]", nil
			}
			return "", denied
		}},
		"mountinfo": {
			readLink: func(path string) (string, error) {
				if path == "/proc/self/ns/mnt" {
					return "mnt:[1]", nil
				}
				return "mnt:[2]", nil
			},
			readFile: func(string) ([]byte, error) { return nil, denied },
		},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := s.lost([]Process{{PID: 10}}); err == nil {
				t.Fatalf("lost() = %+v, want an error", got)
			}
		})
	}
}
