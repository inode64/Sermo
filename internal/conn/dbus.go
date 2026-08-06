package conn

import (
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"

	"sermo/internal/cfgval"
)

func init() { Register(dbusProtocol{}) }

const (
	// dbusDefaultAddress is the well-known system bus address.
	dbusDefaultAddress = "unix:path=/run/dbus/system_bus_socket"

	dbusReadOnlyCallFlags = dbus.FlagNoAutoStart
	dbusFirstNameIndex    = 0
	dbusMaxBusNameLen     = 255
	dbusMaxMemberLen      = 255
	dbusMinBusNameParts   = 2
	dbusGetID             = "org.freedesktop.DBus.GetId"
	dbusGetNameOwner      = "org.freedesktop.DBus.GetNameOwner"
	dbusIntrospect        = "org.freedesktop.DBus.Introspectable.Introspect"
	dbusPeerPing          = "org.freedesktop.DBus.Peer.Ping"
	dbusPropertiesGet     = "org.freedesktop.DBus.Properties.Get"
	dbusTCPPrefix         = "tcp:"
	dbusTCPHostKey        = "host"
	dbusTCPPortKey        = "port"
	dbusUnixPrefix        = "unix:"
	dbusUnixPathPrefix    = "unix:path="
)

// D-Bus probe modes. Peer is the default named-service liveness probe;
// introspect and property are the only additional calls because both are
// standardized read-only operations.
const (
	DBusProbePeer       = "peer"
	DBusProbeIntrospect = "introspect"
	DBusProbeProperty   = "property"
)

// DBusTarget is the optional named object a D-Bus check probes. An empty target
// means bus-only health. DBusInterface maps to dbus_interface in YAML; the
// generic Config.Interface field controls network egress instead.
type DBusTarget struct {
	BusName       string
	ObjectPath    string
	Probe         string
	DBusInterface string
	Property      string
}

func (target DBusTarget) probeMode() string {
	if target.Probe == "" {
		return DBusProbePeer
	}
	return target.Probe
}

type dbusIntrospection struct {
	Interfaces []struct {
		Name string `xml:"name,attr"`
	} `xml:"interface"`
}

type dbusMethodCaller interface {
	CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call
}

type dbusObjectFunc func(string, dbus.ObjectPath) dbusMethodCaller

// dbusProtocol probes a D-Bus daemon natively over its wire protocol using the
// pure-Go github.com/godbus/dbus/v5 client. Connecting performs the SASL auth
// and the org.freedesktop.DBus.Hello handshake, which alone proves the bus is
// up; it then calls org.freedesktop.DBus.GetId to read the bus UUID. No write
// operation is performed.
//
// The target defaults to the system bus (unix:path=/run/dbus/system_bus_socket).
// Set `socket` for a different socket path, or `query` for a full D-Bus address
// (e.g. unix:abstract=..., tcp:host=...,port=...). It is socket-based and has no
// standard TCP port (a tcp: address carries its own host/port). No user/password:
// access is governed by the socket's permissions.
type dbusProtocol struct{}

func (dbusProtocol) Name() string       { return ProtocolNameDBus }
func (dbusProtocol) DefaultPort() int   { return defaultPortNone }
func (dbusProtocol) RequiresUser() bool { return false }

func (dbusProtocol) Probe(ctx context.Context, cfg Config) (Result, error) {
	if err := ValidateDBusTarget(dbusTargetFromConfig(cfg)); err != nil {
		return Result{}, probeErr(ProtocolNameDBus, stepConfig, err)
	}
	return probeBusWithDeadline(ctx, cfg, dbusProbe)
}

// probeBusWithDeadline resolves the bus address from cfg and runs probe under
// the shared deadline backstop; the prologue every D-Bus-based probe repeats.
// buildConnCheck pre-resolves the address into Socket; the fallback keeps a
// direct Probe call (e.g. from a test) resolving query/default. godbus'
// connect/call are context-aware (WithContext / CallWithContext); the outer
// backstop covers a stuck handshake.
func probeBusWithDeadline(ctx context.Context, cfg Config, probe func(ctx context.Context, cfg Config, addr string) (Result, error)) (Result, error) {
	addr := cfg.Socket
	if addr == "" {
		addr = DBusAddress("", cfg.Query)
	}
	return probeWithDeadline(ctx, func(ctx context.Context) (Result, error) {
		return probe(ctx, cfg, addr)
	})
}

// dbusProbe connects to the bus (auth + Hello), reads the bus id and closes.
func dbusProbe(ctx context.Context, cfg Config, addr string) (Result, error) {
	conn, err := connectDBus(ctx, cfg, addr)
	if err != nil {
		return Result{}, probeErr(ProtocolNameDBus, stepConnect, err)
	}
	defer func() { _ = conn.Close() }()

	var busID string
	if err := conn.BusObject().CallWithContext(ctx, dbusGetID, dbusReadOnlyCallFlags).Store(&busID); err != nil {
		return Result{}, probeErr(ProtocolNameDBus, stepDBusGetID, err)
	}
	extra := map[string]string{extraAddress: addr}
	if busID != "" {
		extra[extraBusID] = busID
	}
	if names := conn.Names(); len(names) > 0 {
		extra[extraUniqueName] = names[dbusFirstNameIndex]
	}
	target := dbusTargetFromConfig(cfg)
	if target.BusName != "" {
		owner, observed, err := probeDBusService(ctx, conn.BusObject(), func(destination string, path dbus.ObjectPath) dbusMethodCaller {
			return conn.Object(destination, path)
		}, target)
		if err != nil {
			return Result{}, err
		}
		extra[ExtraKeyDBusBusName] = target.BusName
		extra[ExtraKeyDBusObjectPath] = target.ObjectPath
		extra[ExtraKeyDBusOwner] = owner
		extra[ExtraKeyFingerprint] = owner
		maps.Copy(extra, observed)
	}
	return Result{Extra: extra}, nil
}

func dbusTargetFromConfig(cfg Config) DBusTarget {
	return DBusTarget{
		BusName:       cfg.Params[ParamKeyDBusBusName],
		ObjectPath:    cfg.Params[ParamKeyDBusObjectPath],
		Probe:         cfg.Params[ParamKeyDBusProbe],
		DBusInterface: cfg.Params[ParamKeyDBusInterface],
		Property:      cfg.Params[ParamKeyDBusProperty],
	}
}

func probeDBusService(ctx context.Context, bus dbusMethodCaller, object dbusObjectFunc, target DBusTarget) (string, map[string]string, error) {
	owner, err := dbusNameOwner(ctx, bus, target.BusName)
	if err != nil {
		return "", nil, probeErr(ProtocolNameDBus, stepDBusGetNameOwner, err)
	}
	observed, step, err := probeDBusObject(ctx, object(owner, dbus.ObjectPath(target.ObjectPath)), target)
	if err != nil {
		return "", nil, probeErr(ProtocolNameDBus, step, err)
	}
	return owner, observed, nil
}

func probeDBusObject(ctx context.Context, object dbusMethodCaller, target DBusTarget) (map[string]string, string, error) {
	probe := target.probeMode()
	observed := map[string]string{ExtraKeyDBusProbe: probe}
	if target.DBusInterface != "" {
		observed[ExtraKeyDBusInterface] = target.DBusInterface
	}
	switch probe {
	case DBusProbePeer:
		if err := object.CallWithContext(ctx, dbusPeerPing, dbusReadOnlyCallFlags).Store(); err != nil {
			return nil, stepDBusPeerPing, fmt.Errorf("call %s: %w", dbusPeerPing, err)
		}
		return observed, stepDBusPeerPing, nil
	case DBusProbeIntrospect:
		var document string
		if err := object.CallWithContext(ctx, dbusIntrospect, dbusReadOnlyCallFlags).Store(&document); err != nil {
			return nil, stepDBusIntrospect, fmt.Errorf("call %s: %w", dbusIntrospect, err)
		}
		if err := requireDBusInterface(document, target.DBusInterface); err != nil {
			return nil, stepDBusIntrospect, err
		}
		return observed, stepDBusIntrospect, nil
	case DBusProbeProperty:
		var variant dbus.Variant
		if err := object.CallWithContext(ctx, dbusPropertiesGet, dbusReadOnlyCallFlags, target.DBusInterface, target.Property).Store(&variant); err != nil {
			return nil, stepDBusPropertiesGet, fmt.Errorf("call %s: %w", dbusPropertiesGet, err)
		}
		value, err := dbusScalarString(variant.Value())
		if err != nil {
			return nil, stepDBusPropertiesGet, fmt.Errorf("read property %s.%s: %w", target.DBusInterface, target.Property, err)
		}
		observed[ExtraKeyDBusProperty] = target.Property
		observed[ExtraKeyDBusPropertyValue] = value
		return observed, stepDBusPropertiesGet, nil
	default:
		return nil, stepConfig, fmt.Errorf("unsupported D-Bus probe %q", probe)
	}
}

func requireDBusInterface(document, interfaceName string) error {
	var node dbusIntrospection
	if err := xml.Unmarshal([]byte(document), &node); err != nil {
		return fmt.Errorf("parse D-Bus introspection XML: %w", err)
	}
	if interfaceName == "" {
		return nil
	}
	for _, iface := range node.Interfaces {
		if iface.Name == interfaceName {
			return nil
		}
	}
	return fmt.Errorf("D-Bus interface %q is not exported by the object", interfaceName)
}

func dbusScalarString(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case bool:
		return strconv.FormatBool(typed), nil
	case int:
		return strconv.Itoa(typed), nil
	case int8:
		return strconv.FormatInt(int64(typed), numericBaseDecimal), nil
	case int16:
		return strconv.FormatInt(int64(typed), numericBaseDecimal), nil
	case int32:
		return strconv.FormatInt(int64(typed), numericBaseDecimal), nil
	case int64:
		return strconv.FormatInt(typed, numericBaseDecimal), nil
	case uint:
		return strconv.FormatUint(uint64(typed), numericBaseDecimal), nil
	case uint8:
		return strconv.FormatUint(uint64(typed), numericBaseDecimal), nil
	case uint16:
		return strconv.FormatUint(uint64(typed), numericBaseDecimal), nil
	case uint32:
		return strconv.FormatUint(uint64(typed), numericBaseDecimal), nil
	case uint64:
		return strconv.FormatUint(typed, numericBaseDecimal), nil
	case float32:
		return strconv.FormatFloat(float64(typed), 'g', -1, 32), nil
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64), nil
	case dbus.ObjectPath:
		return string(typed), nil
	default:
		return "", fmt.Errorf("D-Bus property type %T is not a supported scalar", value)
	}
}

func dbusNameOwner(ctx context.Context, bus dbusMethodCaller, busName string) (string, error) {
	var owner string
	if err := bus.CallWithContext(ctx, dbusGetNameOwner, dbusReadOnlyCallFlags, busName).Store(&owner); err != nil {
		return "", fmt.Errorf("get owner of D-Bus name %q: %w", busName, err)
	}
	if owner == "" {
		return "", fmt.Errorf("D-Bus name %q has an empty owner", busName)
	}
	return owner, nil
}

// connectDBus establishes and completes the D-Bus auth/Hello handshake. The
// ordinary D-Bus client owns Unix-address dialing; TCP addresses with an egress
// interface are opened through probeTarget so SO_BINDTODEVICE is not bypassed.
func connectDBus(ctx context.Context, cfg Config, addr string) (*dbus.Conn, error) {
	conn, err := dialDBus(ctx, cfg, addr)
	if err != nil {
		return nil, err
	}
	if err := conn.Auth(nil); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("authenticate D-Bus connection: %w", err)
	}
	if err := conn.Hello(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("complete D-Bus Hello handshake: %w", err)
	}
	return conn, nil
}

// dialDBus creates a private D-Bus connection without the auth/Hello handshake.
// A local Unix address has no egress interface. For a TCP address, an interface
// is safety-significant: godbus dials internally, so a bound target is opened
// first and handed to dbus.NewConn instead.
func dialDBus(ctx context.Context, cfg Config, addr string) (*dbus.Conn, error) {
	tcpCfg, bindTCP, err := dbusBoundTCPConfig(cfg, addr)
	if err != nil {
		return nil, err
	}
	if !bindTCP {
		conn, err := dbus.Dial(addr, dbus.WithContext(ctx))
		if err != nil {
			return nil, fmt.Errorf("dial D-Bus address %q: %w", addr, err)
		}
		return conn, nil
	}
	c, err := newProbeTarget(tcpCfg, defaultPortNone).openTCP(ctx)
	if err != nil {
		return nil, err
	}
	conn, err := dbus.NewConn(c, dbus.WithContext(ctx))
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("create D-Bus connection: %w", err)
	}
	return conn, nil
}

// dbusBoundTCPConfig identifies the one address form Sermo can dial through a
// configured egress interface. Unix-only alternatives need no network binding;
// every address that could fall back to another network transport is rejected
// instead of letting godbus dial it outside SO_BINDTODEVICE.
func dbusBoundTCPConfig(cfg Config, addr string) (Config, bool, error) {
	if cfg.Interface == "" {
		return Config{}, false, nil
	}
	addresses := strings.Split(addr, ";")
	allUnix := true
	for _, candidate := range addresses {
		if !strings.HasPrefix(candidate, dbusUnixPrefix) {
			allUnix = false
			break
		}
	}
	if allUnix {
		return Config{}, false, nil
	}
	if len(addresses) != 1 {
		return Config{}, false, fmt.Errorf("D-Bus address alternatives cannot be used with interface %q", cfg.Interface)
	}
	if !strings.HasPrefix(addr, dbusTCPPrefix) {
		return Config{}, false, fmt.Errorf("D-Bus address transport cannot be bound to interface %q", cfg.Interface)
	}
	tcpCfg, err := dbusTCPConfig(cfg, addr)
	if err != nil {
		return Config{}, false, err
	}
	return tcpCfg, true, nil
}

// dbusTCPConfig parses the supported D-Bus TCP address form into a regular
// connection target. D-Bus options that alter transport semantics are rejected
// when interface binding is requested instead of silently dropping them.
func dbusTCPConfig(cfg Config, addr string) (Config, error) {
	if !strings.HasPrefix(addr, dbusTCPPrefix) {
		return Config{}, fmt.Errorf("D-Bus address %q is not TCP", addr)
	}
	var host string
	port := defaultPortNone
	for field := range strings.SplitSeq(strings.TrimPrefix(addr, dbusTCPPrefix), ",") {
		key, encodedValue, ok := strings.Cut(field, "=")
		if !ok || encodedValue == "" {
			return Config{}, fmt.Errorf("invalid D-Bus TCP address %q", addr)
		}
		value, err := dbus.UnescapeBusAddressValue(encodedValue)
		if err != nil {
			return Config{}, fmt.Errorf("invalid D-Bus TCP address value %q: %w", encodedValue, err)
		}
		if value == "" {
			return Config{}, fmt.Errorf("invalid D-Bus TCP address value %q", encodedValue)
		}
		switch key {
		case dbusTCPHostKey:
			if host != "" {
				return Config{}, fmt.Errorf("D-Bus TCP address %q has multiple hosts", addr)
			}
			host = value
		case dbusTCPPortKey:
			if port != defaultPortNone {
				return Config{}, fmt.Errorf("D-Bus TCP address %q has multiple ports", addr)
			}
			parsed, err := strconv.Atoi(value)
			if err != nil || !cfgval.ValidTCPPort(parsed) {
				return Config{}, fmt.Errorf("invalid D-Bus TCP port %q", value)
			}
			port = parsed
		default:
			return Config{}, fmt.Errorf("D-Bus TCP address option %q cannot be used with interface", key)
		}
	}
	if host == "" || port == defaultPortNone {
		return Config{}, fmt.Errorf("D-Bus TCP address %q requires host and port", addr)
	}
	return Config{Host: host, Port: port, Interface: cfg.Interface}, nil
}

// DBusAddress resolves the D-Bus address: a full address in query wins, then a
// socket path (wrapped as unix:path=), otherwise the system bus default. It is
// exported so the checks package can pre-resolve the address at build time.
func DBusAddress(socket, query string) string {
	if query != "" {
		return query
	}
	if socket != "" {
		return dbusUnixPathPrefix + dbus.EscapeBusAddressValue(socket)
	}
	return dbusDefaultAddress
}

// ValidateDBusTarget validates the optional named-service target of a D-Bus
// check. A bus-only check leaves the target empty; named targets use one of the
// constrained read-only probe modes.
func ValidateDBusTarget(target DBusTarget) error {
	busName, objectPath := target.BusName, target.ObjectPath
	switch {
	case busName == "" && objectPath == "" && target.Probe == "" && target.DBusInterface == "" && target.Property == "":
		return nil
	case busName == "":
		return errors.New("bus_name is required when object_path is set")
	case objectPath == "":
		return errors.New("object_path is required when bus_name is set")
	case !validDBusBusName(busName):
		return fmt.Errorf("bus_name %q is not a valid well-known D-Bus name", busName)
	case !dbus.ObjectPath(objectPath).IsValid():
		return fmt.Errorf("object_path %q is not a valid D-Bus object path", objectPath)
	}
	probe := target.probeMode()
	switch probe {
	case DBusProbePeer:
		if target.DBusInterface != "" {
			return errors.New("dbus_interface is not supported by the peer probe")
		}
		if target.Property != "" {
			return errors.New("property is only supported by the property probe")
		}
	case DBusProbeIntrospect:
		if target.Property != "" {
			return errors.New("property is only supported by the property probe")
		}
	case DBusProbeProperty:
		if target.DBusInterface == "" {
			return errors.New("dbus_interface is required by the property probe")
		}
		if target.Property == "" {
			return errors.New("property is required by the property probe")
		}
	default:
		return fmt.Errorf("probe must be %q, %q or %q", DBusProbePeer, DBusProbeIntrospect, DBusProbeProperty)
	}
	if target.DBusInterface != "" && !validDBusInterface(target.DBusInterface) {
		return fmt.Errorf("dbus_interface %q is not a valid D-Bus interface", target.DBusInterface)
	}
	if target.Property != "" && !validDBusMember(target.Property) {
		return fmt.Errorf("property %q is not a valid D-Bus property", target.Property)
	}
	return nil
}

func validDBusInterface(name string) bool {
	if name == "" || len(name) > dbusMaxBusNameLen {
		return false
	}
	elements := strings.Split(name, ".")
	if len(elements) < dbusMinBusNameParts {
		return false
	}
	for _, element := range elements {
		if !validDBusMember(element) {
			return false
		}
	}
	return true
}

func validDBusMember(name string) bool {
	if name == "" || len(name) > dbusMaxMemberLen || (!isASCIIAlpha(name[0]) && name[0] != '_') {
		return false
	}
	for i := 1; i < len(name); i++ {
		if c := name[i]; !isASCIIAlpha(c) && !isASCIIDigit(c) && c != '_' {
			return false
		}
	}
	return true
}

func validDBusBusName(busName string) bool {
	if busName == "" || len(busName) > dbusMaxBusNameLen || busName[0] == ':' {
		return false
	}
	elements := strings.Split(busName, ".")
	if len(elements) < dbusMinBusNameParts {
		return false
	}
	for _, element := range elements {
		if element == "" || isASCIIDigit(element[0]) {
			return false
		}
		for i := range len(element) {
			c := element[i]
			if !isASCIIAlpha(c) && !isASCIIDigit(c) && c != '_' && c != '-' {
				return false
			}
		}
	}
	return true
}

func isASCIIAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isASCIIDigit(c byte) bool { return c >= '0' && c <= '9' }
