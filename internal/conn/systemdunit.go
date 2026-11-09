package conn

import (
	"context"
	"fmt"

	"github.com/godbus/dbus/v5"
)

const (
	systemdBusName     = "org.freedesktop.systemd1"
	systemdManagerPath = dbus.ObjectPath("/org/freedesktop/systemd1")
	systemdGetUnit     = "org.freedesktop.systemd1.Manager.GetUnit"
)

// SystemdUnitProperty reads one property of a loaded systemd unit over the
// system bus. Manager.GetUnit, unlike LoadUnit, fails for a unit systemd has not
// loaded instead of loading it, so a host check only reads state; no call may
// auto-start a service. The connection follows ctx and is closed on return.
func SystemdUnitProperty(ctx context.Context, unit, iface, property string) (any, error) {
	conn, err := connectDBus(ctx, Config{}, dbusDefaultAddress)
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close() }()
	return systemdUnitProperty(ctx, conn.Object(systemdBusName, systemdManagerPath), func(path dbus.ObjectPath) dbusMethodCaller {
		return conn.Object(systemdBusName, path)
	}, unit, iface, property)
}

func systemdUnitProperty(ctx context.Context, manager dbusMethodCaller, unitObject func(dbus.ObjectPath) dbusMethodCaller, unit, iface, property string) (any, error) {
	var path dbus.ObjectPath
	if err := manager.CallWithContext(ctx, systemdGetUnit, dbusReadOnlyCallFlags, unit).Store(&path); err != nil {
		return nil, fmt.Errorf("find systemd unit %s: %w", unit, err)
	}
	var variant dbus.Variant
	if err := unitObject(path).CallWithContext(ctx, dbusPropertiesGet, dbusReadOnlyCallFlags, iface, property).Store(&variant); err != nil {
		return nil, fmt.Errorf("read %s.%s of %s: %w", iface, property, unit, err)
	}
	return variant.Value(), nil
}
