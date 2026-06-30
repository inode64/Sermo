package conn

import "testing"

// TestProbeMetadata consolidates the former per-probe Test*Registered tests:
// it asserts each probe (and its aliases) is registered with the expected
// default port and RequiresUser flag. Add a row here when adding a probe.
func TestProbeMetadata(t *testing.T) {
	cases := []struct {
		names        []string
		port         int
		requiresUser bool
	}{
		{[]string{"acpid"}, 0, false},
		{[]string{"ajp"}, 8009, false},
		{[]string{"amqp", "rabbitmq"}, 5672, false},
		{[]string{"asterisk", "ami"}, 5038, false},
		{[]string{"avahi", "avahi-daemon"}, 0, false},
		{[]string{"ceph", "ceph-mon"}, 3300, false},
		{[]string{"clamd", "clamav"}, 3310, false},
		{[]string{"cloudflared", "cloudflare-tunnel"}, 60123, false},
		{[]string{"dbus"}, 0, false},
		{[]string{"dhclient", "dhcp-client"}, 68, false},
		{[]string{"dhcp", "dhcpd"}, 67, false},
		{[]string{"dns"}, 53, false},
		{[]string{"docker"}, 2375, false},
		{[]string{"fail2ban"}, 0, false},
		{[]string{"fpm"}, 9000, false},
		{[]string{"ftp"}, 21, false},
		{[]string{"glusterfs", "glusterd", "gluster"}, 24007, false},
		{[]string{"guacd", "guacamole"}, 4822, false},
		{[]string{"imap"}, 143, false},
		{[]string{"influxdb", "influx"}, 8086, false},
		{[]string{"ipp", "cups"}, 631, false},
		{[]string{"kafka"}, 9092, false},
		{[]string{"ldap"}, 389, false},
		{[]string{"libvirt", "libvirtd"}, 16509, false},
		{[]string{"lvmpolld"}, 0, false},
		{[]string{"memcached", "memcache"}, 11211, false},
		{[]string{"mongodb", "mongo"}, 27017, false},
		{[]string{"mountd", "rpc.mountd", "nfs-mountd"}, 20048, false},
		{[]string{"mqtt"}, 1883, false},
		{[]string{"mysql", "mariadb"}, 3306, false},
		{[]string{"nebula", "nebula-vpn"}, 4242, false},
		{[]string{"nfs", "nfs-server", "nfsd"}, 2049, false},
		{[]string{"nntp", "nntps"}, 119, false},
		{[]string{"ntp"}, 123, false},
		{[]string{"nut", "ups", "upsd"}, 3493, false},
		{[]string{"openvpn", "ovpn"}, 1194, false},
		{[]string{"openvswitch", "ovs", "ovsdb", "ovsdb-server"}, 6640, false},
		{[]string{"pop", "pop3"}, 110, false},
		{[]string{"postgres", "postgresql"}, 5432, true},
		{[]string{"prometheus", "prom"}, 9090, false},
		{[]string{"rdp", "ms-wbt-server"}, 3389, false},
		{[]string{"redis"}, 6379, false},
		{[]string{"rpcbind", "portmap", "portmapper"}, 111, false},
		{[]string{"rspamd"}, 11334, false},
		{[]string{"rsync", "rsyncd"}, 873, false},
		{[]string{"sieve", "managesieve"}, 4190, false},
		{[]string{"smb", "samba", "cifs"}, 445, false},
		{[]string{"smtp"}, 25, false},
		{[]string{"snmp"}, 161, false},
		{[]string{"spamd", "spamassassin"}, 783, false},
		{[]string{"ssh"}, 22, false},
		{[]string{"statd", "rpc.statd", "nsm", "nfs-statd"}, 662, false},
		{[]string{"syncthing"}, 8384, false},
		{[]string{"tftp"}, 69, false},
		{[]string{"udisks2"}, 0, false},
		{[]string{"unifi", "unifi-controller", "unifi-network"}, 8443, false},
		{[]string{"varnish", "varnishadm"}, 6082, false},
	}
	for _, c := range cases {
		for _, name := range c.names {
			p, ok := Lookup(name)
			if !ok {
				t.Errorf("%s not registered", name)
				continue
			}
			if p.DefaultPort() != c.port {
				t.Errorf("%s default port = %d, want %d", name, p.DefaultPort(), c.port)
			}
			if p.RequiresUser() != c.requiresUser {
				t.Errorf("%s RequiresUser = %v, want %v", name, p.RequiresUser(), c.requiresUser)
			}
		}
	}
}
