package conn

import "fmt"

// protocolRegistration is one built-in protocol and the aliases that resolve
// to it. Keeping every registration in one immutable table makes aliases and
// registry membership reviewable without relying on package init order.
type protocolRegistration struct {
	protocol      Protocol
	aliases       []string
	defaultSocket string
}

// builtinProtocolRegistrations is the complete connection-protocol catalog.
// Protocol implementations own their wire exchange and metadata methods; this
// table is the single source of truth for membership and aliases.
var builtinProtocolRegistrations = []protocolRegistration{
	{protocol: acpidProtocol, defaultSocket: DefaultACPIDSocket},
	{protocol: ajpProtocol{}},
	{protocol: amqpProtocol{}, aliases: []string{protocolAliasRabbitMQ}},
	{protocol: asteriskProtocol{}, aliases: []string{protocolAliasAMI}},
	{protocol: avahiProtocol{}, aliases: []string{protocolAliasAvahiDaemon}},
	{protocol: cephProtocol{}, aliases: []string{protocolAliasCephMon}},
	{protocol: chronyProtocol{}, aliases: []string{protocolAliasChronyd}},
	{protocol: clamdProtocol{}, aliases: []string{protocolAliasClamAV}},
	{protocol: cloudflaredProtocol{}, aliases: []string{protocolAliasCloudflareTunnel}},
	{protocol: dbusProtocol{}},
	{protocol: dhclientProtocol{}, aliases: []string{protocolAliasDHClient}},
	{protocol: dhcpProtocol{}, aliases: []string{protocolAliasDHCPD}},
	{protocol: dnsProtocol{}},
	{protocol: dockerProtocol{}, defaultSocket: DefaultDockerSocket},
	{protocol: fail2banProtocol, defaultSocket: DefaultFail2banSocket},
	{protocol: fpmProtocol{}, aliases: []string{protocolAliasPHPFPM}},
	{protocol: ftpProtocol{}},
	{protocol: glusterfsProtocol{}, aliases: []string{protocolAliasGlusterd, protocolAliasGluster}},
	{protocol: guacdProtocol{}, aliases: []string{protocolAliasGuacamole}},
	{protocol: imapProtocol{}},
	{protocol: influxdbProtocol{}, aliases: []string{protocolAliasInflux}},
	{protocol: ippProtocol{}, aliases: []string{protocolAliasCUPS}},
	{protocol: kafkaProtocol{}},
	{protocol: ldapProtocol{}},
	{protocol: libvirtProtocol{}, aliases: []string{protocolAliasLibvirtd}, defaultSocket: DefaultLibvirtSocket},
	{protocol: lvmpolldProtocol{}, defaultSocket: DefaultLVMPolldSocket},
	{protocol: memcachedProtocol{}, aliases: []string{protocolAliasMemcache}},
	{protocol: mongodbProtocol{}, aliases: []string{protocolAliasMongo}},
	{protocol: mountdProtocol{}, aliases: []string{protocolAliasRPCMountd, protocolAliasNFSMountd}},
	{protocol: mqttProtocol{}},
	{protocol: mysqlProtocol{}, aliases: []string{protocolAliasMariaDB}},
	{protocol: nebulaProtocol{}, aliases: []string{protocolAliasNebulaVPN}},
	{protocol: nfsProtocol{}, aliases: []string{protocolAliasNFSServer, protocolAliasNFSD}},
	{protocol: nntpProtocol{}, aliases: []string{protocolAliasNNTPs}},
	{protocol: ntpProtocol{}},
	{protocol: nutProtocol{}, aliases: []string{protocolAliasUPS, protocolAliasUPSD}},
	{protocol: openvpnProtocol{}, aliases: []string{protocolAliasOpenVPN}},
	{protocol: openvswitchProtocol{}, aliases: []string{protocolAliasOVS, protocolAliasOVSDB, protocolAliasOVSDBServer}},
	{protocol: popProtocol{}, aliases: []string{protocolAliasPOP3}},
	{protocol: postgresProtocol{}, aliases: []string{protocolAliasPostgreSQL}},
	{protocol: prometheusProtocol{}, aliases: []string{protocolAliasPrometheus}},
	{protocol: rdpProtocol{}, aliases: []string{protocolAliasMSWBTServer}},
	{protocol: redisProtocol{}, aliases: []string{protocolAliasValkey}},
	{protocol: rpcbindProtocol{}, aliases: []string{protocolAliasPortmap, protocolAliasPortmapper}},
	{protocol: rspamdProtocol{}},
	{protocol: rsyncProtocol{}, aliases: []string{protocolAliasRsyncd}},
	{protocol: sieveProtocol{}, aliases: []string{protocolAliasManageSieve}},
	{protocol: smbProtocol{}, aliases: []string{protocolAliasSamba, protocolAliasCIFS}},
	{protocol: smtpProtocol{}},
	{protocol: snmpProtocol{}},
	{protocol: spamdProtocol{}, aliases: []string{protocolAliasSpamAssassin}},
	{protocol: sshProtocol{}},
	{protocol: statdProtocol{}, aliases: []string{protocolAliasRPCStatd, protocolAliasNSM, protocolAliasNFSStatd}},
	{protocol: syncthingProtocol{}},
	{protocol: tftpProtocol{}},
	{protocol: unifiProtocol{}, aliases: []string{protocolAliasUniFiController, protocolAliasUniFiNetwork}},
	{protocol: varnishProtocol{}, aliases: []string{protocolAliasVarnishAdm}},
}

// registry is an immutable name-to-protocol index. Registrations are complete
// before the map is published, so lookups need no daemon-hot-path lock.
type registry struct {
	byName      map[string]Protocol
	byCanonical map[string]protocolRegistration
}

func newRegistry(registrations []protocolRegistration) (*registry, error) {
	byName := make(map[string]Protocol, len(registrations))
	byCanonical := make(map[string]protocolRegistration, len(registrations))
	for _, registration := range registrations {
		if registration.protocol == nil {
			return nil, fmt.Errorf("register connection protocol: nil implementation")
		}
		name := registration.protocol.Name()
		if name == "" {
			return nil, fmt.Errorf("register connection protocol: empty canonical name")
		}
		if err := registerProtocolName(byName, name, registration.protocol); err != nil {
			return nil, err
		}
		byCanonical[name] = registration
		for _, alias := range registration.aliases {
			if alias == "" {
				return nil, fmt.Errorf("register connection protocol %q: empty alias", name)
			}
			if err := registerProtocolName(byName, alias, registration.protocol); err != nil {
				return nil, err
			}
		}
	}
	return &registry{byName: byName, byCanonical: byCanonical}, nil
}

func registerProtocolName(byName map[string]Protocol, name string, protocol Protocol) error {
	if previous, exists := byName[name]; exists {
		return fmt.Errorf("register connection protocol name %q for %q: already owned by %q", name, protocol.Name(), previous.Name())
	}
	byName[name] = protocol
	return nil
}

func mustRegistry(registrations []protocolRegistration) *registry {
	registry, err := newRegistry(registrations)
	if err != nil {
		panic(err)
	}
	return registry
}

//nolint:ireturn // A registry lookup returns the protocol selected at runtime.
func (r *registry) lookup(name string) (Protocol, bool) {
	protocol, ok := r.byName[name]
	if !ok || protocol == nil {
		return nil, false
	}
	return protocol, true
}

// defaultRegistry holds the immutable protocols compiled into the binary.
var defaultRegistry = mustRegistry(builtinProtocolRegistrations)

// Lookup returns the protocol registered under name (canonical or alias).
//
//nolint:ireturn // The public registry API returns the protocol interface selected at runtime.
func Lookup(name string) (Protocol, bool) { return defaultRegistry.lookup(name) }

// Resolve applies one protocol's target defaults to cfg. An explicit socket,
// host or port always wins; when neither socket nor host was selected, a
// protocol with a well-known local socket prefers it over loopback TCP.
func Resolve(protocol Protocol, cfg Config) Config {
	if protocol == nil {
		return cfg
	}
	registration, registered := defaultRegistry.byCanonical[protocol.Name()]
	if cfg.Socket == "" && cfg.Host == "" && registered {
		cfg.Socket = registration.defaultSocket
	}
	if cfg.Host == "" {
		cfg.Host = DefaultHost
	}
	if cfg.Port == 0 {
		cfg.Port = protocol.DefaultPort()
	}
	return cfg
}

// DefaultPort returns the registered protocol's default port, or 0 when name is
// not registered.
func DefaultPort(name string) int {
	protocol, ok := Lookup(name)
	if !ok {
		return defaultPortNone
	}
	return protocol.DefaultPort()
}
