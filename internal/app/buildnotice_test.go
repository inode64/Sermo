package app

import (
	"testing"
)

type fakeNoticeLogger struct {
	infos []string
	warns []string
}

func (l *fakeNoticeLogger) Info(_ string, args ...any) { l.infos = append(l.infos, noticeArg(args)) }
func (l *fakeNoticeLogger) Warn(_ string, args ...any) { l.warns = append(l.warns, noticeArg(args)) }

func noticeArg(args []any) string {
	if len(args) < 2 {
		return ""
	}
	s, _ := args[1].(string)
	return s
}

func TestLogBuildNoticesRoutesInfoAndWarn(t *testing.T) {
	logger := &fakeNoticeLogger{}
	LogBuildNotices(logger, "build workers", []string{
		"service web: no unit resolved on systemd; tried: web (using web)",
		infoNotice("service polkit: resolve service unit for polkit: service is not available on openrc"),
	})
	if len(logger.warns) != 1 || logger.warns[0] != "service web: no unit resolved on systemd; tried: web (using web)" {
		t.Fatalf("warns = %v, want the unresolved-candidate warning", logger.warns)
	}
	if len(logger.infos) != 1 || logger.infos[0] != "service polkit: resolve service unit for polkit: service is not available on openrc" {
		t.Fatalf("infos = %v, want the backend-unsupported notice without its marker", logger.infos)
	}
}
