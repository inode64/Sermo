package checks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sermo/internal/process"
)

func TestPrivateTmpCheck(t *testing.T) {
	lost := []process.LostPrivateTmp{
		{PID: 3821, MountPoint: "/tmp", Root: "/systemd-private-1a-php-fpm@8.4.service-GD5BVE/tmp//deleted"},
	}
	for name, tc := range map[string]struct {
		lost        []process.LostPrivateTmp
		err         error
		ok, unavail bool
		message     string
	}{
		"intact":     {ok: true, message: "no private /tmp lost"},
		"lost":       {lost: lost, message: "private /tmp of pid 3821 deleted on the host (/systemd-private-1a-php-fpm@8.4.service-GD5BVE/tmp//deleted); " + PrivateTmpRestartHint},
		"scan error": {err: errors.New("permission denied"), unavail: true, message: "cannot inspect private /tmp: permission denied"},
	} {
		t.Run(name, func(t *testing.T) {
			check, issue := buildPrivateTmpCheck(base{name: "private-tmp"}, Deps{PrivateTmp: func() ([]process.LostPrivateTmp, error) { return tc.lost, tc.err }})
			if issue != "" {
				t.Fatal(issue)
			}
			res := check.Run(context.Background())
			if res.OK != tc.ok || res.Unavailable != tc.unavail || !strings.Contains(res.Message, tc.message) {
				t.Fatalf("result = %+v, want ok=%v unavailable=%v message %q", res, tc.ok, tc.unavail, tc.message)
			}
			if len(tc.lost) > 0 && (res.Data[DataKeyPIDs] != "3821" || res.Data[DataKeyValue] != float64(1)) {
				t.Fatalf("data = %v", res.Data)
			}
		})
	}
	if _, issue := buildPrivateTmpCheck(base{name: "private-tmp"}, Deps{}); issue == "" {
		t.Fatal("built without process discovery")
	}
}
