package conn

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
)

// Busy is the protocol verdict of a peer that is alive and answered that it
// cannot serve this client now: saturated (too many connections), loading its
// dataset, starting up or recovering, or temporarily refusing work. The
// origin of that state is never the daemon itself, so restarting it cannot fix
// it and drops every client it is still serving. A busy result is therefore
// reported as a failure but is never a reason for a lifecycle action (see
// rules.Evaluator.EvalRule).
type Busy struct {
	// Code is the server's own code for the refusal when it sends one: a MySQL
	// error number, a PostgreSQL SQLSTATE, a Redis error prefix or a reply code.
	Code string
	// Reason is the server's message.
	Reason string
}

// String renders the verdict the way operators read it, "Too many connections
// (1040)".
func (b Busy) String() string {
	switch {
	case b.Code == "":
		return b.Reason
	case b.Reason == "":
		return b.Code
	default:
		return fmt.Sprintf("%s (%s)", b.Reason, b.Code)
	}
}

// busyFailurePrefix opens every busy verdict, so the message says the server
// answered before it says why.
const busyFailurePrefix = "server busy: "

// OpenSSH sends this pre-identification refusal when connection admission
// limits reject a client. Only a complete reply followed by EOF establishes
// that verdict; a timeout or arbitrary banner remains a transport error.
const sshAdmissionRefused = "Not allowed at this time"

func sshBannerBusy(raw []byte, err error) (Busy, bool) {
	if errors.Is(err, io.EOF) && strings.TrimRight(string(raw), protocolTrimCRLF) == sshAdmissionRefused {
		return Busy{Reason: sshAdmissionRefused}, true
	}
	return Busy{}, false
}

// Message is the verdict as every consumer reports it: Result.Failure, a sql
// or db_queries failure ("server busy: Too many connections (1040)").
func (b Busy) Message() string { return busyFailurePrefix + b.String() }

// Evidence is the verdict as result data: the server's message and, when it
// sent one, its code. Probes publish it in Extra and the sql and db_queries
// checks in Data, under the same keys.
func (b Busy) Evidence() map[string]string {
	evidence := map[string]string{ExtraKeyBusyReason: b.Reason}
	if b.Code != "" {
		evidence[ExtraKeyErrorCode] = b.Code
	}
	return evidence
}

// busyResult turns res into a busy verdict: a Failure (the peer answered and
// rejected) flagged Busy, with the server's code and message as evidence.
func busyResult(res Result, b Busy) Result {
	evidence := b.Evidence()
	extra := make(map[string]string, len(res.Extra)+len(evidence))
	maps.Copy(extra, res.Extra)
	maps.Copy(extra, evidence)
	res.Failure, res.Busy, res.Extra = b.Message(), true, extra
	return res
}

// replyError is a server's negative reply (Redis "-ERR …", POP "-ERR …"), kept
// typed so a handshake can tell the server's refusal from a transport error.
type replyError string

func (e replyError) Error() string { return string(e) }

// AsBusy reports whether err is a MySQL or PostgreSQL driver error whose code
// says the server is saturated or not ready yet. Any other error, including
// authentication and permission failures, is not busy.
func AsBusy(err error) (Busy, bool) {
	if err == nil {
		return Busy{}, false
	}
	if mysqlErr, ok := errors.AsType[*mysql.MySQLError](err); ok {
		return mysqlBusy(mysqlErr.Number, mysqlErr.Message)
	}
	if pgErr, ok := errors.AsType[*pgconn.PgError](err); ok && postgresBusyCode(pgErr.Code) {
		return Busy{Code: pgErr.Code, Reason: pgErr.Message}, true
	}
	return Busy{}, false
}

// MySQL and MariaDB error numbers a live server answers when it cannot take
// one more client. 1129 (host blocked) belongs here too: the server is alive
// and the fix is FLUSH HOSTS, not a restart. 1045 (access denied) and 1130
// (host not allowed) are configuration errors and stay ordinary failures.
const (
	mysqlErrConCount               uint16 = 1040 // ER_CON_COUNT_ERROR: Too many connections
	mysqlErrOutOfResources         uint16 = 1041 // ER_OUT_OF_RESOURCES
	mysqlErrHostIsBlocked          uint16 = 1129 // ER_HOST_IS_BLOCKED
	mysqlErrCantCreateThread       uint16 = 1135 // ER_CANT_CREATE_THREAD
	mysqlErrTooManyUserConnections uint16 = 1203 // ER_TOO_MANY_USER_CONNECTIONS
	mysqlErrUserLimitReached       uint16 = 1226 // ER_USER_LIMIT_REACHED
)

// mysqlBusy classifies a MySQL/MariaDB error number.
func mysqlBusy(number uint16, message string) (Busy, bool) {
	switch number {
	case mysqlErrConCount, mysqlErrOutOfResources, mysqlErrHostIsBlocked,
		mysqlErrCantCreateThread, mysqlErrTooManyUserConnections, mysqlErrUserLimitReached:
		return Busy{Code: strconv.Itoa(int(number)), Reason: strings.TrimSpace(message)}, true
	default:
		return Busy{}, false
	}
}

// PostgreSQL SQLSTATEs of a live server that cannot serve now: class 53
// (insufficient resources: too many clients, disk full, out of memory,
// configuration limit exceeded) and 57P03 (cannot connect now: starting up,
// in recovery, shutting down).
const (
	postgresClassInsufficientResources = "53"
	postgresCannotConnectNow           = "57P03"
)

// postgresBusyCode classifies a PostgreSQL SQLSTATE.
func postgresBusyCode(code string) bool {
	return strings.HasPrefix(code, postgresClassInsufficientResources) || code == postgresCannotConnectNow
}

// Redis, Valkey and KeyDB error prefixes of a live server that cannot serve
// now: loading its dataset, running a script past its time limit, a replica
// whose master is down, or refusing commands after a failed save (a full disk).
// "ERR max number of clients reached" is the maxclients refusal, sent before
// the server closes the connection.
const (
	redisErrorLoading    = "LOADING"
	redisErrorBusy       = "BUSY"
	redisErrorMasterDown = "MASTERDOWN"
	redisErrorMisconf    = "MISCONF"
	redisErrorGeneric    = "ERR"
	redisErrorMaxClients = "max number of clients reached"
)

// redisBusy classifies the payload of a Redis error reply ("LOADING Redis is
// loading the dataset in memory").
func redisBusy(reply string) (Busy, bool) {
	code, message, _ := strings.Cut(strings.TrimSpace(reply), " ")
	message = strings.TrimSpace(message)
	switch code {
	case redisErrorLoading, redisErrorBusy, redisErrorMasterDown, redisErrorMisconf:
		return Busy{Code: code, Reason: message}, true
	case redisErrorGeneric:
		if strings.HasPrefix(message, redisErrorMaxClients) {
			return Busy{Reason: message}, true
		}
	}
	return Busy{}, false
}

// memcachedErrorTooManyConnections is what memcached writes, prefixed by
// "ERROR ", to a client it accepted past its -c limit before closing it.
const memcachedErrorTooManyConnections = "Too many open connections"

// Text-protocol greeting codes of a live server that will not serve now: 421
// "service not available, closing channel" (SMTP, FTP: too many connections,
// shutting down) and FTP's 120 "service ready in nnn minutes".
const (
	replyCodeServiceNotAvailable = 421
	ftpReplyCodeReadyLater       = 120
)

// replyBye is the untagged BYE an IMAP or ManageSieve server sends instead of
// its greeting when it refuses the connection.
const replyBye = "BYE"

// temporaryRefusalMarkers are the response codes and phrases with which a
// mail server says its refusal is temporary: RFC 5530 [UNAVAILABLE] and
// [LIMIT] (IMAP), RFC 3206 [SYS/TEMP] and [IN-USE] (POP3), RFC 5804 TRYLATER
// (ManageSieve), and the words servers use for connection limits and
// shutdowns. They are matched in lower case.
var temporaryRefusalMarkers = []string{
	"[unavailable]", "[limit]", "[sys/temp]", "[in-use]", "trylater",
	"too many", "try again", "try later", "temporar", "shutting down",
	"maximum number", "busy", "overload",
}

// refusalBusy classifies a greeting refusal (an IMAP or Sieve BYE, a POP -ERR)
// by its text: busy when it says it is temporary, so a server that refuses for
// another reason — an internal error, a broken configuration — stays an
// ordinary failure and can still be restarted.
func refusalBusy(code, text string) (Busy, bool) {
	lower := strings.ToLower(text)
	if !slices.ContainsFunc(temporaryRefusalMarkers, func(marker string) bool { return strings.Contains(lower, marker) }) {
		return Busy{}, false
	}
	return Busy{Code: code, Reason: strings.TrimSpace(text)}, true
}

// greetingBusy classifies a text-protocol greeting code as busy.
func greetingBusy(code int, greeting string, codes ...int) (Busy, bool) {
	if slices.Contains(codes, code) {
		return Busy{Code: strconv.Itoa(code), Reason: strings.TrimSpace(greeting)}, true
	}
	return Busy{}, false
}
