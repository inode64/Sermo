// Package conn provides connection-protocol probes behind a small registry.
// Each protocol implements Protocol and
// registers itself; the checks package looks a protocol up by name to build a
// generic connection check. It is a leaf package: it depends on neither checks
// nor config, so both can import it without a cycle.
package conn

import (
	"bufio"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"io"
	"strings"
	"sync"

	"sermo/internal/dockerctl"
	"sermo/internal/httpx"
	"sermo/internal/netutil"
	"sermo/internal/units"
)

// DefaultHost is the loopback host protocol probes use when config omits host.
const DefaultHost = netutil.LoopbackIPv4

const (
	numericBaseDecimal = 10
	protocolLineBreak  = '\n'
	protocolTrimCRLF   = "\r\n"
	xid32Bytes         = 4
)

// ProtocolName* constants are canonical connection protocol names shared by the
// protocol registry and check builders.
const (
	ProtocolNameACPID       = "acpid"
	ProtocolNameAJP         = "ajp"
	ProtocolNameAMQP        = "amqp"
	ProtocolNameAsterisk    = "asterisk"
	ProtocolNameAvahi       = "avahi"
	ProtocolNameCeph        = "ceph"
	ProtocolNameClamd       = "clamd"
	ProtocolNameCloudflared = "cloudflared"
	ProtocolNameDBus        = "dbus"
	ProtocolNameDHClient    = "dhclient"
	ProtocolNameDHCP        = "dhcp"
	ProtocolNameDNS         = "dns"
	ProtocolNameDocker      = dockerctl.ControlType
	ProtocolNameFail2ban    = "fail2ban"
	ProtocolNameFPM         = "fpm"
	ProtocolNameFTP         = "ftp"
	ProtocolNameGlusterFS   = "glusterfs"
	ProtocolNameGuacd       = "guacd"
	ProtocolNameIMAP        = "imap"
	ProtocolNameInfluxDB    = "influxdb"
	ProtocolNameIPP         = "ipp"
	ProtocolNameKafka       = "kafka"
	ProtocolNameLDAP        = "ldap"
	ProtocolNameLibvirt     = "libvirt"
	ProtocolNameLVMPolld    = "lvmpolld"
	ProtocolNameMemcached   = "memcached"
	ProtocolNameMongoDB     = "mongodb"
	ProtocolNameMountd      = "mountd"
	ProtocolNameMQTT        = "mqtt"
	ProtocolNameMySQL       = "mysql"
	ProtocolNameNebula      = "nebula"
	ProtocolNameNFS         = "nfs"
	ProtocolNameNNTP        = "nntp"
	ProtocolNameNTP         = "ntp"
	ProtocolNameNUT         = "nut"
	ProtocolNameOpenVPN     = "openvpn"
	ProtocolNameOpenVSwitch = "openvswitch"
	ProtocolNamePOP         = "pop"
	ProtocolNamePostgres    = "postgres"
	ProtocolNamePrometheus  = "prometheus"
	ProtocolNameRDP         = "rdp"
	ProtocolNameRedis       = "redis"
	ProtocolNameRPCBind     = "rpcbind"
	ProtocolNameRsync       = "rsync"
	ProtocolNameRspamd      = "rspamd"
	ProtocolNameSieve       = "sieve"
	ProtocolNameSMB         = "smb"
	ProtocolNameSMTP        = "smtp"
	ProtocolNameSNMP        = "snmp"
	ProtocolNameSpamd       = "spamd"
	ProtocolNameSSH         = "ssh"
	ProtocolNameStatd       = "statd"
	ProtocolNameSyncthing   = "syncthing"
	ProtocolNameTFTP        = "tftp"
	ProtocolNameUDisks2     = "udisks2"
	ProtocolNameUniFi       = "unifi"
	ProtocolNameVarnish     = "varnish"
)

// Protocol aliases accepted by the registry in addition to canonical names.
const (
	protocolAliasAMI              = "ami"
	protocolAliasAvahiDaemon      = "avahi-daemon"
	protocolAliasCephMon          = "ceph-mon"
	protocolAliasCIFS             = "cifs"
	protocolAliasClamAV           = "clamav"
	protocolAliasCloudflareTunnel = "cloudflare-tunnel"
	protocolAliasCUPS             = "cups"
	protocolAliasDHClient         = "dhcp-client"
	protocolAliasDHCPD            = "dhcpd"
	protocolAliasGluster          = "gluster"
	protocolAliasGlusterd         = "glusterd"
	protocolAliasGuacamole        = "guacamole"
	protocolAliasInflux           = "influx"
	protocolAliasLibvirtd         = "libvirtd"
	protocolAliasMariaDB          = "mariadb"
	protocolAliasManageSieve      = "managesieve"
	protocolAliasMemcache         = "memcache"
	protocolAliasMongo            = "mongo"
	protocolAliasMSWBTServer      = "ms-wbt-server"
	protocolAliasNebulaVPN        = "nebula-vpn"
	protocolAliasNFSMountd        = "nfs-mountd"
	protocolAliasNFSServer        = "nfs-server"
	protocolAliasNFSD             = "nfsd"
	protocolAliasNFSStatd         = "nfs-statd"
	protocolAliasNNTPs            = "nntps"
	protocolAliasNSM              = "nsm"
	protocolAliasOpenVPN          = "ovpn"
	protocolAliasOVS              = "ovs"
	protocolAliasOVSDB            = "ovsdb"
	protocolAliasOVSDBServer      = "ovsdb-server"
	protocolAliasPHPFPM           = "php-fpm"
	protocolAliasPOP3             = "pop3"
	protocolAliasPortmap          = "portmap"
	protocolAliasPortmapper       = "portmapper"
	protocolAliasPostgreSQL       = "postgresql"
	protocolAliasPrometheus       = "prom"
	protocolAliasRabbitMQ         = "rabbitmq"
	protocolAliasRPCMountd        = "rpc.mountd"
	protocolAliasRPCStatd         = "rpc.statd"
	protocolAliasRsyncd           = "rsyncd"
	protocolAliasSamba            = "samba"
	protocolAliasSpamAssassin     = "spamassassin"
	protocolAliasUniFiController  = "unifi-controller"
	protocolAliasUniFiNetwork     = "unifi-network"
	protocolAliasUPS              = "ups"
	protocolAliasUPSD             = "upsd"
	protocolAliasValkey           = "valkey"
	protocolAliasVarnishAdm       = "varnishadm"
)

// Result.Extra keys shared with consumers that interpret protocol identity.
const (
	ExtraKeyFingerprint       = "fingerprint"
	ExtraKeyGreeting          = "greeting"
	ExtraKeyHostname          = "hostname"
	ExtraKeyRole              = "role"
	ExtraKeyServer            = "server"
	ExtraKeyStatus            = "status"
	ExtraKeyVersionString     = "version_string"
	ExtraKeyContainer         = "container"
	ExtraKeyContainerStatus   = "container.status"
	ExtraKeyContainerHealth   = "container.health"
	ExtraKeyContainerRunning  = "container.running"
	ExtraKeyContainerRestarts = "container.restartcount"
	ExtraKeyContainerExitCode = "container.exitcode"
	ExtraKeyDockerContainers  = "containers"
	ExtraKeyDockerRunning     = "containers.running"
	ExtraKeyDockerPaused      = "containers.paused"
	ExtraKeyDockerStopped     = "containers.stopped"
	ExtraKeyDockerImages      = "images"
	ExtraKeyDockerWarnings    = "warnings"
	ExtraKeyDNSQuery          = "query"
	ExtraKeyDNSRCode          = "rcode"
	ExtraKeyDNSAnswers        = "answers"
	ExtraKeyDNSAddresses      = "addresses"
	ExtraKeyDomain            = "domain"
	ExtraKeyDomainCount       = "domains"
	ExtraKeyDomainActive      = "domains.active"
	ExtraKeyDomainInactive    = "domains.inactive"
	ExtraKeyDomainState       = "domain.state"
	ExtraKeyDomainRunning     = "domain.running"
	ExtraKeyInode             = "inode"
	ExtraKeyKafkaAPICount     = "api_count"
	ExtraKeyKafkaErrorCode    = "error_code"
	ExtraKeyKafkaProduceAPI   = "produce_api"
	ExtraKeyKafkaVoteAPI      = "vote_api"
	ExtraKeyLocalAddress      = "local_address"
	ExtraKeyMongoReadOnly     = "read_only"
	ExtraKeyMongoSetName      = "set_name"
	ExtraKeyNodeCPUs          = "node.cpus"
	ExtraKeyNodeMemoryMB      = "node.memory_mb"
	ExtraKeyPort              = "port"
	ExtraKeyState             = "state"
)

// DNSRCodeNoErrorName is the DNS response code name for a successful response.
const DNSRCodeNoErrorName = "NOERROR"

// LibvirtDomainState* constants are stable lower-case state names emitted in
// ExtraKeyDomainState.
const (
	LibvirtDomainStateRunning     = "running"
	LibvirtDomainStateBlocked     = "blocked"
	LibvirtDomainStatePaused      = "paused"
	LibvirtDomainStateShutdown    = "shutdown"
	LibvirtDomainStateShutoff     = "shutoff"
	LibvirtDomainStateCrashed     = "crashed"
	LibvirtDomainStatePMSuspended = "pmsuspended"
	LibvirtDomainStateNoState     = "nostate"
)

// Config.Params keys consumed by protocol probes.
const (
	ParamKeyAuthSource = "auth_source"
	ParamKeyDomain     = "domain"
	ParamKeyMAC        = "mac"
	ParamKeyResolvconf = "resolvconf"
	ParamKeyTransport  = "transport"
)

// ParamValueTrue is the string form used for true boolean values in Config.Params.
const ParamValueTrue = "true"

// TLSModeSkipVerify is the tls mode that enables TLS without certificate
// verification.
const TLSModeSkipVerify = netutil.TLSModeSkipVerify

const (
	// TLSValueSummary is the user-facing list of accepted connection-check TLS values.
	TLSValueSummary = "boolean, " + TLSModeSkipVerify + ", or a valid sslmode"
	// TLSScalarSummary is the user-facing scalar form accepted in YAML.
	TLSScalarSummary = "true/false/" + TLSModeSkipVerify
)

// Transport names accepted by protocol params that expose a network transport.
const (
	TransportTCP = "tcp"
	TransportUDP = "udp"
	// TransportSummary is the user-facing list of network transport names.
	TransportSummary = TransportUDP + " or " + TransportTCP
)

// Default protocol ports used when a check omits an explicit port.
const (
	defaultPortNone        = 0
	defaultPortAJP         = 8009
	defaultPortAMQP        = 5672
	defaultPortAsterisk    = 5038
	defaultPortCeph        = 3300
	defaultPortClamd       = 3310
	defaultPortCloudflared = 60123
	defaultPortFPM         = 9000
	defaultPortFTP         = 21
	defaultPortGlusterFS   = 24007
	defaultPortGuacd       = 4822
	defaultPortIMAP        = 143
	defaultPortInfluxDB    = 8086
	defaultPortIPP         = 631
	defaultPortKafka       = 9092
	defaultPortLDAP        = 389
	defaultPortLibvirt     = 16509
	defaultPortMemcached   = 11211
	defaultPortMongoDB     = 27017
	defaultPortMountd      = 20048
	defaultPortMQTT        = 1883
	defaultPortMySQL       = 3306
	defaultPortNebula      = 4242
	defaultPortNFS         = 2049
	defaultPortNNTP        = 119
	defaultPortNTP         = 123
	defaultPortNUT         = 3493
	defaultPortOpenVPN     = 1194
	defaultPortOpenVSwitch = 6640
	defaultPortPOP         = 110
	defaultPortPostgres    = 5432
	defaultPortPrometheus  = 9090
	defaultPortRDP         = 3389
	defaultPortRedis       = 6379
	defaultPortRPCBind     = 111
	defaultPortRsync       = 873
	defaultPortRspamd      = 11334
	defaultPortSieve       = 4190
	defaultPortSMB         = 445
	defaultPortSMTP        = 25
	defaultPortSpamd       = 783
	defaultPortSSH         = 22
	defaultPortStatd       = 662
	defaultPortSyncthing   = 8384
	defaultPortTFTP        = 69
	defaultPortUniFi       = 8443
	defaultPortVarnish     = 6082
)

// DefaultPortLibvirt is libvirt's plaintext TCP port.
const DefaultPortLibvirt = defaultPortLibvirt

const (
	fallbackXID32 = 0x53524d4f // "SRMO"
	networkTCP    = TransportTCP
	networkUDP    = TransportUDP
	networkUnix   = netutil.NetworkUnix

	// HTTP probe body limits keep service probes bounded against unexpected peers.
	maxHTTPProbeBody      = 64 * units.BytesPerKiB
	maxHTTPProbeLargeBody = units.BytesPerMiB
	maxHTTPProbeShortBody = 4 * units.BytesPerKiB

	httpHeaderContentType   = httpx.HeaderContentType
	httpHeaderServer        = httpx.HeaderServer
	httpHeaderSyncthingAuth = "X-Api-Key"

	tlsSkipVerify = TLSModeSkipVerify
	tlsModeFalse  = "false"
	tlsModeYes    = "yes"
	tlsModeNo     = "no"
	tlsModeOn     = "on"
	tlsModeOff    = "off"
	tlsRequired   = "required"
	tlsDisable    = "disable"
	tlsRequire    = "require"
	tlsPrefer     = "prefer"
	tlsVerifyCA   = "verify-ca"
	tlsVerifyFull = "verify-full"
	// schemeHTTP and schemeHTTPS are the URL schemes an HTTP-based probe selects
	// by whether TLS is in use.
	schemeHTTP         = netutil.URLSchemeHTTP
	schemeHTTPS        = netutil.URLSchemeHTTPS
	urlSchemeSeparator = netutil.URLSchemeSeparator
	// extraGreeting is the Result.Extra key carrying a text-protocol server's
	// greeting/banner line (ftp, imap, pop, nntp, rsync, sieve, …).
	extraGreeting = ExtraKeyGreeting
	extraAddress  = "address"
	extraArch     = "arch"
	// extraAuthenticated marks protocols where optional auth was actually tested.
	extraAuthenticated = "authenticated"
	extraBanner        = "banner"
	extraBind          = "bind"
	extraBusID         = "bus_id"
	// extraCLIStatus is the Varnish CLI status code exposed by varnishadm.
	extraCLIStatus             = "cli_status"
	extraClientMAC             = "client_mac"
	extraConnack               = "connack"
	extraContentType           = "content_type"
	extraDatabases             = "databases"
	extraDHCPMessage           = "dhcp_message"
	extraEndpoint              = "endpoint"
	extraFixedAddress          = "fixed_address"
	extraHealth                = "health"
	extraImplementation        = "implementation"
	extraInterface             = "interface"
	extraLeaseExpires          = "lease_expires_at"
	extraLeaseFile             = "lease_file"
	extraLeaseSeconds          = "lease_seconds"
	extraLeaseSecondsRemaining = "lease_seconds_remaining"
	extraLibVersion            = "lib_version"
	extraLogin                 = "login"
	extraMessenger             = "messenger"
	extraMode                  = "mode"
	extraNegotiation           = "negotiation"
	extraOS                    = "os"
	extraOffsetSeconds         = "offset_seconds"
	extraOwner                 = "owner"
	extraOfferedIP             = "offered_ip"
	extraPool                  = "pool"
	extraProcessManager        = "process_manager"
	extraRC                    = "rc"
	extraResult                = "result"
	extraRevision              = "revision"
	extraRunning               = "running"
	extraSecurity              = "security"
	extraSelect                = "select"
	extraServerID              = "server_id"
	extraServerVer             = "server_version"
	extraSession               = "session_present"
	extraShareAccess           = "share_access"
	extraShares                = "shares"
	extraStratum               = "stratum"
	extraSubnetMask            = "subnet_mask"
	extraSysContact            = "sys_contact"
	extraSysLocation           = "sys_location"
	extraSysName               = "sys_name"
	extraSysUptimeSeconds      = "sys_uptime_seconds"
	extraTFTPError             = "tftp_error"
	extraTFTPErrorCode         = "tftp_error_code"
	extraUniqueName            = "unique_name"
	extraUPS                   = "ups"
	extraURI                   = "uri"
	extraUptime                = "uptime_seconds"
	extraUUID                  = "uuid"
	extraVersion               = "version"
	// extraProgram and extraRPCStatus are the Result.Extra keys of the
	// Sun-RPC probes (rpcbind, nfs, mountd, glusterfs): the queried program
	// number and the portmapper/RPC status.
	extraProgram   = "program"
	extraRPCStatus = "rpc_status"
	// extraPing is the Result.Extra key and respPong the expected reply body of
	// the ping/pong health probes (php-fpm, rspamd, spamd).
	extraPing = "ping"
	respPong  = "pong"
	// extraProtocol is the Result.Extra key carrying a negotiated protocol
	// version/dialect (ssh, smb, rsync, spamd, dhclient).
	extraProtocol = "protocol"
	// extraReply is the Result.Extra key carrying a probe's decoded reply token
	// (ajp cpong, openvpn/nebula reset, tftp op).
	extraReply = "reply"
	extraQuery = "query"
	// extraTransport is the Result.Extra key carrying the transport a probe used
	// (openvpn udp/tcp, libvirt connection mode).
	extraTransport = "transport"
	// extraSocket is the Result.Extra key carrying the unix socket path a probe
	// connected to (acpid, fail2ban, lvmpolld).
	extraSocket = "socket"
)

// ExtraKeySocket is the Result.Extra key carrying the Unix socket path a probe used.
const ExtraKeySocket = extraSocket

// Config is the connection target for a protocol probe. Fields that do not apply
// to a protocol are ignored by it.
type Config struct {
	Host     string
	Port     int
	Socket   string // Unix socket path; when set, protocols dial it instead of host:port
	User     string
	Password string
	Database string
	Query    string // protocol-specific lookup target (e.g. the DNS name to resolve)
	TLS      string // "" / "false" (plaintext), "true", "skip-verify"
	// Interface, when set, is the network interface the probe must egress through
	// (Linux SO_BINDTODEVICE) — for multi-homed hosts. Empty means default routing.
	Interface string
	Params    map[string]string
}

// Result is what a successful probe observed.
type Result struct {
	Version string
	Extra   map[string]string
}

// Protocol connects to a server over its wire protocol and verifies it responds.
//
// Every implementation must honor cfg.Interface (egress binding via
// SO_BINDTODEVICE) by dialing through BindDialer — directly or via
// probeBanner/dialDeadline/dialConn. When simplifying a probe with a Go module,
// preserve interface binding: a codec-only library is ideal (keep the existing
// dial, e.g. DNS with x/net/dnsmessage); a library that does its own I/O is only
// acceptable if it takes a custom dialer routed through BindDialer (e.g. NTP via
// beevik/ntp's Dialer callback). A library that dials internally with no such
// hook must not be adopted — keep the hand-rolled probe (e.g. DHCP). See
// AGENTS.md "Protocol probes: interface binding is mandatory".
type Protocol interface {
	Name() string     // canonical type token, e.g. "mysql"
	DefaultPort() int // used when the config omits a port
	// RequiresUser reports whether a user is mandatory. Some protocols can
	// prove liveness from an unauthenticated greeting; others need a user.
	RequiresUser() bool
	Probe(ctx context.Context, cfg Config) (Result, error)
}

// ValidTLSValue reports whether value is one of the connection-check TLS mode
// strings accepted by config validation.
func ValidTLSValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ParamValueTrue, tlsModeFalse, tlsModeYes, tlsModeNo, tlsModeOn, tlsModeOff,
		tlsRequired, tlsSkipVerify, tlsDisable, tlsRequire, tlsPrefer, tlsVerifyCA, tlsVerifyFull:
		return true
	default:
		return false
	}
}

// registry maps protocol names (canonical and aliases) to protocols.
type registry struct {
	mu     sync.RWMutex
	byName map[string]Protocol
}

func newRegistry() *registry {
	return &registry{byName: map[string]Protocol{}}
}

func (r *registry) register(p Protocol, aliases ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byName[p.Name()] = p
	for _, a := range aliases {
		r.byName[a] = p
	}
}

func (r *registry) lookup(name string) (Protocol, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byName[name]
	return p, ok
}

// defaultRegistry holds the protocols compiled into the binary.
var defaultRegistry = newRegistry()

// Register adds a protocol (and optional aliases) to the default registry.
func Register(p Protocol, aliases ...string) { defaultRegistry.register(p, aliases...) }

// Lookup returns the protocol registered under name (canonical or alias).
func Lookup(name string) (Protocol, bool) { return defaultRegistry.lookup(name) }

// DefaultPort returns the registered protocol's default port, or 0 when name is
// not registered.
func DefaultPort(name string) int {
	proto, ok := Lookup(name)
	if !ok {
		return defaultPortNone
	}
	return proto.DefaultPort()
}

// readCRLFLine reads one CRLF/LF-terminated line, trimmed — the line shape
// every text protocol probe (redis RESP, imap, nut, …) reads.
func readCRLFLine(br *bufio.Reader) (string, error) {
	s, err := br.ReadString(protocolLineBreak)
	return strings.TrimRight(s, protocolTrimCRLF), err
}

// readGreetingLine reads one CR/LF-terminated greeting line from a fresh reader
// over r, trimmed. It tolerates a read error as long as some data arrived — a
// server that sends its banner then closes without a final newline — returning
// the error only when nothing was read. For single-line greetings; a probe that
// reads more lines must keep its own bufio.Reader.
func readGreetingLine(r io.Reader) (string, error) {
	line, err := bufio.NewReader(r).ReadString(protocolLineBreak)
	if err != nil && line == "" {
		return "", err
	}
	return strings.TrimRight(line, protocolTrimCRLF), nil
}

// randXID32 returns a random 32-bit transaction id with a fixed fallback when
// the system RNG fails, shared by the rpcbind/nfs and dhcp probes.
func randXID32() uint32 {
	var b [xid32Bytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fallbackXID32
	}
	return binary.BigEndian.Uint32(b[:])
}

func hostPort(host string, port int) string {
	return netutil.JoinHostPort(host, port)
}

// pingAndVersion verifies a database/sql pool answers a ping and best-effort
// reads the server version with versionQuery — a successful ping already proves
// connect + auth. The probe tail shared by the SQL-backed protocols.
func pingAndVersion(ctx context.Context, db *sql.DB, versionQuery string) (Result, error) {
	if err := db.PingContext(ctx); err != nil {
		return Result{}, err
	}
	var version string
	_ = db.QueryRowContext(ctx, versionQuery).Scan(&version)
	return Result{Version: version}, nil
}

// hostPortDefaults returns cfg's host and port with the probe defaults applied:
// DefaultHost when the host is empty, defaultPort when the port is zero.
func (cfg Config) hostPortDefaults(defaultPort int) (string, int) {
	host := cfg.Host
	if host == "" {
		host = DefaultHost
	}
	port := cfg.Port
	if port == 0 {
		port = defaultPort
	}
	return host, port
}

// addrDefaults renders cfg's host:port address with the probe defaults applied.
func (cfg Config) addrDefaults(defaultPort int) string {
	return hostPort(cfg.hostPortDefaults(defaultPort))
}
