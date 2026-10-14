package conn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
)

// AsBusy classifies the driver errors the sql and db_queries checks see, through
// any wrapping; authentication, permission and missing-object errors are not busy.
func TestAsBusy(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		code string
		busy bool
	}{
		{"mysql too many connections", &mysql.MySQLError{Number: 1040, Message: "Too many connections"}, "1040", true},
		{"mysql wrapped", fmt.Errorf("processlist: %w", &mysql.MySQLError{Number: 1203, Message: "User x already has more than 'max_user_connections' active connections"}), "1203", true},
		{"mysql out of resources", &mysql.MySQLError{Number: 1041, Message: "Out of memory"}, "1041", true},
		{"mysql user limit", &mysql.MySQLError{Number: 1226, Message: "User 'x' has exceeded the 'max_connections_per_hour' resource"}, "1226", true},
		{"mysql thread", &mysql.MySQLError{Number: 1135, Message: "Can't create a new thread"}, "1135", true},
		{"mysql host blocked", &mysql.MySQLError{Number: 1129, Message: "Host 'x' is blocked"}, "1129", true},
		{"mysql access denied", &mysql.MySQLError{Number: 1045, Message: "Access denied"}, "", false},
		{"mysql host not allowed", &mysql.MySQLError{Number: 1130, Message: "Host 'x' is not allowed"}, "", false},
		{"postgres too many clients", &pgconn.PgError{Code: "53300", Message: "sorry, too many clients already"}, "53300", true},
		{"postgres disk full", fmt.Errorf("query: %w", &pgconn.PgError{Code: "53100", Message: "could not extend file"}), "53100", true},
		{"postgres starting up", &pgconn.PgError{Code: "57P03", Message: "the database system is starting up"}, "57P03", true},
		{"postgres auth", &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}, "", false},
		{"postgres missing table", &pgconn.PgError{Code: "42P01", Message: "relation does not exist"}, "", false},
		{"plain error", errors.New("dial tcp: connection refused"), "", false},
		{"nil", nil, "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			busy, ok := AsBusy(tc.err)
			if ok != tc.busy || busy.Code != tc.code {
				t.Fatalf("AsBusy = %+v, %v; want code %q busy %v", busy, ok, tc.code, tc.busy)
			}
			if ok && busy.Reason == "" {
				t.Fatal("a busy verdict must carry the server's reason")
			}
		})
	}
}

func TestBusyStringAndResult(t *testing.T) {
	if got := (Busy{Code: "1040", Reason: "Too many connections"}).String(); got != "Too many connections (1040)" {
		t.Fatalf("String = %q", got)
	}
	if got := (Busy{Reason: "max number of clients reached"}).String(); got != "max number of clients reached" {
		t.Fatalf("String without code = %q", got)
	}
	base := Result{Extra: map[string]string{extraGreeting: "421 busy"}}
	res := busyResult(base, Busy{Code: "421", Reason: "busy"})
	if !res.Busy || res.Failure != "server busy: busy (421)" || res.Extra[extraGreeting] != "421 busy" ||
		res.Extra[ExtraKeyErrorCode] != "421" || res.Extra[ExtraKeyBusyReason] != "busy" {
		t.Fatalf("busyResult = %+v", res)
	}
	if _, ok := base.Extra[ExtraKeyErrorCode]; ok {
		t.Fatal("busyResult must not mutate the caller's Extra")
	}
	if got := (Busy{Code: "1040", Reason: "Too many connections"}).Message(); got != "server busy: Too many connections (1040)" {
		t.Fatalf("Message = %q", got)
	}
}

// The credentialed path reads the same ERR handshake through the driver.
func TestMySQLAuthenticatedProbeBusy(t *testing.T) {
	port := serveMySQL(t, mysqlERRReply(1040, "Too many connections"))
	res, err := (mysqlProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port, User: "monitor", Password: "x"})
	assertBusy(t, res, err, "1040")
}

// servePostgresError answers the startup message (after declining SSL) with a
// FATAL ErrorResponse carrying code.
func servePostgresError(t *testing.T, code, message string) int {
	t.Helper()
	return serveOnce(t, func(c net.Conn) {
		backend := pgproto3.NewBackend(c, c)
		for {
			msg, err := backend.ReceiveStartupMessage()
			if err != nil {
				return
			}
			if _, ssl := msg.(*pgproto3.SSLRequest); ssl {
				if _, err := c.Write([]byte{'N'}); err != nil {
					return
				}
				continue
			}
			if _, gss := msg.(*pgproto3.GSSEncRequest); gss {
				if _, err := c.Write([]byte{'N'}); err != nil {
					return
				}
				continue
			}
			break
		}
		backend.Send(&pgproto3.ErrorResponse{Severity: "FATAL", Code: code, Message: message})
		_ = backend.Flush()
	})
}

func TestPostgresProbeBusy(t *testing.T) {
	for _, code := range []string{"53300", "57P03"} {
		port := servePostgresError(t, code, "sorry, too many clients already")
		res, err := (postgresProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port, User: "postgres", Database: "postgres"})
		assertBusy(t, res, err, code)
	}
	port := servePostgresError(t, "28P01", "password authentication failed")
	if _, err := (postgresProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port, User: "postgres", Database: "postgres"}); err == nil {
		t.Fatal("an authentication failure must stay an error")
	}
}

func TestMemcachedProbeBusy(t *testing.T) {
	port := fakeMemcached(t, "ERROR Too many open connections\r\n")
	res, err := (memcachedProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: port})
	assertBusy(t, res, err, "")
}

func TestMQTTProbeServerUnavailableIsBusy(t *testing.T) {
	res, err := (mqttProtocol{}).Probe(context.Background(), Config{Host: "127.0.0.1", Port: serveMQTT(t, 3)})
	assertBusy(t, res, err, "3")
}

// A 421 at RCPT keeps the smtp_* evidence and adds the busy verdict.
func TestSMTPAcceptance421IsBusy(t *testing.T) {
	port, commands, done := serveSMTPAcceptance(t, nil, "421 4.7.0 Too many connections, try again later")
	cfg := smtpAcceptanceTestConfig(port)
	cfg.Params[ParamKeySMTPStartTLS] = SMTPStartTLSOpportunistic
	res, err := smtpAcceptanceTestProtocol(nil).Probe(context.Background(), cfg)
	assertBusy(t, res, err, "421")
	if !strings.HasPrefix(res.Failure, smtpAcceptanceStageRCPTTo+" rejected: server busy:") {
		t.Fatalf("the busy message must keep the refusing stage: %q", res.Failure)
	}
	if res.Extra[ExtraKeySMTPStatusCode] != "421" || res.Extra[ExtraKeySMTPStatus] != smtpAcceptanceStatusTemporary {
		t.Fatalf("smtp evidence lost: %v", res.Extra)
	}
	_ = waitSMTPAcceptanceCommands(t, commands, done)
}
