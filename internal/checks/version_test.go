package checks

import "testing"

// TestShortVersionRealData exercises ShortVersion against version strings
// captured live by running each app's configured version command and taking the first
// non-empty line, exactly as Inspect does. Each line must reduce to its numeric
// version and at most the patchlevel; a miss fails the test so the regex can be
// tightened against formats it does not yet cover.
func TestShortVersionRealData(t *testing.T) {
	cases := []struct {
		app, raw, want string
	}{
		{"acpid", "acpid-2.0.34", "2.0.34"},
		{"asterisk", "Asterisk 13.38.3", "13.38.3"},
		{"automount", "Linux automount version 5.1.9", "5.1.9"},
		{"bash", "GNU bash, versión 5.3.9(1)-release (x86_64-pc-linux-gnu)", "5.3.9"},
		{"certbot", "certbot 5.5.0", "5.5.0"},
		{"certbot", "certbot 4.0.0", "4.0.0"},
		{"clamd", "ClamAV 1.5.2/27673/Wed Jun 18 11:48:55 2025", "1.5.2"},
		{"clamd", "ClamAV 1.4.3", "1.4.3"},
		{"dbus-daemon", "D-Bus Message Bus Daemon 1.16.2", "1.16.2"},
		{"dhclient", "isc-dhclient-4.4.3-P1 Gentoo-r6", "4.4.3-P1"},
		{"dhcpd", "isc-dhcpd-4.4.3-P1 Gentoo-r6", "4.4.3-P1"},
		{"dmeventd", "dmeventd version: 1.02.213 (2026-03-13)", "1.02.213"},
		{"dovecot", "2.3.21.1 (d492236fa0)", "2.3.21"},
		{"exim", "Exim version 4.98.2 #2 built 25-Feb-2026 20:07:53", "4.98.2"},
		{"fail2ban-server", "Fail2Ban v1.1.0", "1.1.0"},
		{"fcron", "fcron 3.4.0 - periodic command scheduler", "3.4.0"},
		{"grafana", "grafana version 12.4.3+security-02", "12.4.3"},
		{"git", "git version 2.53.0", "2.53.0"},
		{"go", "go version go1.26.2-X:nodwarf5 linux/amd64", "1.26.2"},
		{"guacd", "Guacamole proxy daemon (guacd) version 1.6.0", "1.6.0"},
		{"in.tftpd", "tftp-hpa 5.2, with remap, without tcpwrappers", "5.2"},
		{"java", `openjdk version "21.0.11" 2026-04-21 LTS`, "21.0.11"},
		{"libvirtd", "/usr/bin/libvirtd (libvirt) 12.0.0", "12.0.0"},
		{"lvm", "  LVM version:     2.03.39(2) (2026-03-13)", "2.03.39"},
		{"lvmpolld", "lvmpolld version: 2.03.39(2) (2026-03-13)", "2.03.39"},
		{"mail", "s-nail v14.9.25, 2024-06-27 (built for Linux)", "14.9.25"},
		{"mariadb", "/usr/bin/mariadbd  Ver 11.8.6-MariaDB-log for Linux on x86_64 (Source distribution)", "11.8.6"},
		{"mariadb", "/usr/bin/mariadbd  Ver 11.4.7-MariaDB for Linux on x86_64 (Source distribution)", "11.4.7"},
		{"mdadm", "mdadm - v4.6 - 2026-03-16", "4.6"},
		{"mysql", "/usr/bin/mysqld  Ver 11.8.3-MariaDB for Linux on x86_64 (Source distribution)", "11.8.3"},
		{"named", "BIND 9.18.49 (Extended Support Version) <id:cd4a53b>", "9.18.49"},
		{"nginx", "nginx version: nginx/1.30.2", "1.30.2"},
		{"nmbd", "Version 4.22.3", "4.22.3"},
		{"node", "v24.14.0", "24.14.0"},
		{"numad", "/usr/bin/numad version: 20150602: compiled Sep 12 2024", "20150602"},
		{"ntpd", "ntpd 4.2.8p18", "4.2.8p18"},
		{"ntpd", "ntpd 4.2.8p18@1.4062-o Thu May 14 07:09:36 UTC 2026 (1)", "4.2.8p18"},
		{"openssl", "OpenSSL 3.5.6 7 Apr 2026 (Library: OpenSSL 3.5.6 7 Apr 2026)", "3.5.6"},
		{"openssh", "OpenSSH_9.9p2, OpenSSL 3.3.3 11 Feb 2025", "9.9p2"},
		{"openvpn", "OpenVPN 2.6.20 x86_64-pc-linux-gnu [SSL (OpenSSL)] [LZO] [LZ4] [EPOLL] [MH/PKTINFO] [AEAD]", "2.6.20"},
		{"ovs-vswitchd", "ovs-vswitchd (Open vSwitch) 3.7.1", "3.7.1"},
		{"ovsdb-client", "ovsdb-client (Open vSwitch) 3.7.1", "3.7.1"},
		{"perl", "This is perl 5, version 42, subversion 0 (v5.42.0) built for x86_64-linux-thread-multi", "5.42.0"},
		{"php", "PHP 8.2.31 (cli) (built: May 25 2026 20:34:19) (NTS)", "8.2.31"},
		{"php", "PHP 5.6.40-pl0-gentoo (cli) (built: Sep  5 2025 19:03:34) ", "5.6.40"},
		{"pkexec", "pkexec version 126", "126"},
		{"proftpd", "ProFTPD Version 1.3.9a", "1.3.9"},
		{"python", "Python 3.13.13", "3.13.13"},
		{"qemu-ga", "QEMU Guest Agent 9.2.0", "9.2.0"},
		{"redis", "Redis server v=8.2.6 sha=00000000:1 malloc=tcmalloc-2.15 bits=64 build=8c3fa9ca5bd0100b", "8.2.6"},
		{"rpc-mountd", "rpc.mountd version 2.8.5", "2.8.5"},
		{"rpc-statd", "rpc.statd version 2.8.5", "2.8.5"},
		{"rspamd", "Rspamd daemon version 4.0.1", "4.0.1"},
		{"rsync", "rsync  version 3.4.3  protocol version 32", "3.4.3"},
		{"ruby", "ruby 3.3.11 (2026-03-26 revision 1f2d15125a) [x86_64-linux]", "3.3.11"},
		{"salt", "salt 3007.14 (Chlorine)", "3007.14"},
		{"smartd", "smartd 7.5 2025-04-30 r5714 [x86_64-linux-6.12.74-gentoo] (local build)", "7.5"},
		{"smbd", "Version 4.22.3", "4.22.3"},
		{"snmpd", "NET-SNMP version:  5.9.5.2", "5.9.5.2"},
		{"snmpd", "NET-SNMP version:  5.9.4.pre", "5.9.4.pre"},
		{"snmpd", "NET-SNMP version:  5.9.4.pre2", "5.9.4.pre2"},
		{"spamassassin", "SpamAssassin version 4.0.1", "4.0.1"},
		{"sqlite", "3.51.3 2026-03-13 10:38:09 737ae4a34738ffa0c3ff7f9bb18df914dd1cad163f28fd6b6e114a344fe6alt1 (64-bit)", "3.51.3"},
		{"squid", "Squid Cache: Version 6.14", "6.14"},
		{"ssh", "OpenSSH_10.3p1, OpenSSL 3.5.6 7 Apr 2026", "10.3p1"},
		{"syslog-ng", "syslog-ng 4 (4.10.2)", "4.10.2"},
		{"systemd", "systemd 260 (260.1)", "260.1"},
		{"upsd", "Network UPS Tools upsd 2.8.2.1 (development iteration after 2.8.2)", "2.8.2.1"},
		{"upsd", "Network UPS Tools upsd 2.8.4.1-0+gc7198d501 (development iteration after 2.8.4)", "2.8.4.1"},
		{"upsmon", "Network UPS Tools upsmon 2.8.2.1 (development iteration after 2.8.2)", "2.8.2.1"},
		{"upsmon", "Network UPS Tools upsmon 2.8.4.1-0+gc7198d501 (development iteration after 2.8.4)", "2.8.4.1"},
		{"virtlockd", "/usr/bin/virtlockd (libvirt) 12.0.0", "12.0.0"},
		{"xinetd", "xinetd 2.3.15.4", "2.3.15.4"},
	}

	for _, c := range cases {
		got := ShortVersion(c.raw)
		if got == "" {
			t.Errorf("%s: ShortVersion(%q) found no version; adjust shortVersionRE to cover this format", c.app, c.raw)
			continue
		}
		if got != c.want {
			t.Errorf("%s: ShortVersion(%q) = %q, want %q", c.app, c.raw, got, c.want)
		}
	}
}

// TestShortVersionNoVersion documents lines that legitimately carry no version
// number: ShortVersion returns "" rather than inventing one. Such a line is what
// proftpd's old `-V` command emitted ("Compile-time Settings:"); its catalog
// entry now uses `--version`, which reports a parseable "ProFTPD Version 1.3.9a".
func TestShortVersionNoVersion(t *testing.T) {
	for _, raw := range []string{
		"",
		"Compile-time Settings:",
		"no version here",
		"built for x86_64-linux-thread-multi",
	} {
		if got := ShortVersion(raw); got != "" {
			t.Errorf("ShortVersion(%q) = %q, want empty", raw, got)
		}
	}
}

func TestVersionLevel(t *testing.T) {
	cases := []struct {
		name  string
		level int
		ok    bool
	}{
		{"major", 1, true},
		{"minor", 2, true},
		{"patch", 3, true},
		{"", 0, false},
		{"build", 0, false},
	}
	for _, c := range cases {
		got, ok := VersionLevel(c.name)
		if got != c.level || ok != c.ok {
			t.Errorf("VersionLevel(%q) = %d, %v; want %d, %v", c.name, got, ok, c.level, c.ok)
		}
	}
}

func TestTruncateVersion(t *testing.T) {
	cases := []struct {
		short string
		level int
		want  string
	}{
		{"1.4.2", 1, "1"},
		{"1.4.2", 2, "1.4"},
		{"1.4.2", 3, "1.4.2"},
		{"1.4.2", 5, "1.4.2"}, // level beyond components keeps them all
		{"1.4", 3, "1.4"},     // fewer components than level
		{"1.4.2", 0, "1.4.2"}, // level<=0 leaves input unchanged
		{"", 2, ""},           // empty input unchanged
	}
	for _, c := range cases {
		if got := TruncateVersion(c.short, c.level); got != c.want {
			t.Errorf("TruncateVersion(%q, %d) = %q, want %q", c.short, c.level, got, c.want)
		}
	}
}
