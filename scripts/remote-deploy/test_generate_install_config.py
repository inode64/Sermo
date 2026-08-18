#!/usr/bin/env python3
"""Regression tests for endpoint-gated remote service generation."""

from __future__ import annotations

import dataclasses
import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path

import yaml


def generator_module():
    path = Path(__file__).with_name("generate_install_config.py")
    spec = importlib.util.spec_from_file_location("generate_install_config", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load generate_install_config.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


generator = generator_module()


def default_options():
    """The GenerationOptions every test in this file exercises."""
    return generator.GenerationOptions(
        web_port=9797,
        web_password="test",
        storage_free_pct="5%",
        expand_by="5G",
        smart_interval="24h",
        hdparm_interval="6h",
        active_services_only=True,
        catalog_services_dir=Path(__file__).parents[2] / "catalog/services",
    )


class EndpointGenerationTest(unittest.TestCase):
    def generate(self, hints: str):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("nginx.service\n", encoding="utf-8")
        (stage / "service_endpoint_hints").write_text(hints, encoding="utf-8")
        (stage / "services_json.out").write_text(
            json.dumps({"services": [{"name": "nginx", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )
        options = default_options()
        report = generator.generate_for_host("host", stage, root / "configs", options)
        body = (root / "configs/host/root/etc/sermo/services/nginx.yml").read_text(encoding="utf-8")
        return report, body

    def test_keeps_http_and_tcp_watches_with_associated_listener(self):
        report, body = self.generate('socket tcp LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("nginx",pid=1,fd=6))\n')
        self.assertNotIn("watches:", body)
        checks = report["services"]["enabled"][0]["endpoint_checks"]
        self.assertEqual([item["active"] for item in checks], [True, True])

    def test_disables_endpoint_watches_without_associated_listener(self):
        report, body = self.generate('socket tcp LISTEN 0 511 0.0.0.0:80 0.0.0.0:* users:(("other",pid=1,fd=6))\n')
        self.assertIn("watches:\n", body)
        self.assertIn("  port:\n    enabled: false", body)
        self.assertIn("  http:\n    enabled: false", body)
        checks = report["services"]["enabled"][0]["endpoint_checks"]
        self.assertEqual([item["active"] for item in checks], [False, False])

    def test_adds_discovered_terminal_namespaces_to_ssh_service(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("ssh.service\n", encoding="utf-8")
        (stage / "services_json.out").write_text(
            json.dumps({"services": [{"name": "ssh", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )
        (stage / "terminal_sessions.tsv").write_text(
            "tmux\troot\t/usr/bin/tmux\t/tmp/tmux-0/default\n"
            "tmux\troot\t/usr/bin/tmux\t/tmp/tmux-0/demo\n"
            "screen\tdeploy\t/usr/bin/screen\t\n",
            encoding="utf-8",
        )

        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        body = yaml.safe_load((root / "configs/host/root/etc/sermo/services/ssh.yml").read_text(encoding="utf-8"))
        terminal_checks = {
            name: watch["check"]
            for name, watch in body["watches"].items()
            if watch.get("check", {}).get("type") == "terminal_sessions"
        }

        self.assertEqual(len(terminal_checks), 3)
        self.assertEqual(
            terminal_checks["terminal-tmux-root-default"]["socket"],
            "/tmp/tmux-0/default",  # noqa: S108 -- fixture for tmux's real socket namespace.
        )
        self.assertEqual(terminal_checks["terminal-tmux-root-demo"]["reports"], "state")
        self.assertNotIn("socket", terminal_checks["terminal-screen-deploy-sessions"])
        self.assertEqual(len(report["terminal_sessions"]), 3)

    def test_ignores_terminal_inventory_without_a_safe_namespace(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("ssh.service\n", encoding="utf-8")
        (stage / "services_json.out").write_text(
            json.dumps({"services": [{"name": "ssh", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )
        (stage / "terminal_sessions.tsv").write_text(
            "tmux\troot\t/usr/bin/tmux\trelative.sock\n"
            "unknown\troot\t/usr/bin/unknown\t/tmp/unknown.sock\n",
            encoding="utf-8",
        )

        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        body = yaml.safe_load((root / "configs/host/root/etc/sermo/services/ssh.yml").read_text(encoding="utf-8"))

        self.assertEqual(report["terminal_sessions"], [])
        self.assertFalse(any(
            watch.get("check", {}).get("type") == "terminal_sessions"
            for watch in body.get("watches", {}).values()
        ))

    def test_active_service_filter_honors_os_selected_unit(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "os-release").write_text('ID="ubuntu"\n', encoding="utf-8")
        (stage / "active_units").write_text(
            "smartmontools.service loaded active running smartd\n",
            encoding="utf-8",
        )
        (stage / "services_all_json.out").write_text(
            json.dumps({"services": [{"name": "smartd", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )

        docs = generator.load_catalog_services(default_options().catalog_services_dir)
        active, failed, candidates, available = generator.active_service_filter(stage, docs)

        self.assertTrue(available)
        self.assertEqual(active, {"smartd"})
        self.assertEqual(failed, set())
        self.assertIn("smartmontools.service", candidates["smartd"])

    def test_accepts_dns_udp_and_explicit_ports_for_the_profile_process(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "service_endpoint_hints").write_text(
            'socket udp UNCONN 0 0 127.0.0.1:53 0.0.0.0:* users:(("dnsmasq",pid=1,fd=6))\n'
            'socket tcp LISTEN 0 511 127.0.0.1:8080 0.0.0.0:* users:(("dnsmasq",pid=1,fd=7))\n',
            encoding="utf-8",
        )
        doc = {
            "name": "dnsmasq",
            "watches": {
                "dns": {"check": {"type": "dns", "host": "127.0.0.1", "port": 53}},
                "ports": {"check": {"type": "ports", "host": "127.0.0.1", "ports": "8080"}},
            },
        }
        disabled, checks = generator.endpoint_watch_overrides(stage, doc, {})
        self.assertEqual(disabled, set())
        self.assertEqual([item["active"] for item in checks], [True, True])

    def test_lvm_space_watches_are_not_generated(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "lvs.json").write_text(
            json.dumps({
                "report": [{
                    "lv": [{
                        "vg_name": "vg0",
                        "lv_name": "root",
                        "data_percent": "-",
                        "metadata_percent": "-",
                    }],
                }],
            }),
            encoding="utf-8",
        )
        options = default_options()
        report = generator.generate_for_host("host", stage, root / "configs", options)
        self.assertFalse((root / "configs/host/root/etc/sermo/watches/lvm-vg0-root.yml").exists())
        self.assertFalse((root / "configs/host/root/etc/sermo/watches/lvm-vg0-capacity.yml").exists())
        self.assertEqual(report["lvm_volumes"], [])
        self.assertIn(
            {"kind": "lvm", "reason": "LVM space watches disabled by configuration"},
            report["skipped_watches"],
        )

    def test_network_block_devices_get_no_smart_watch(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "features").write_text("smartctl=1\n", encoding="utf-8")
        (stage / "lsblk.json").write_text(
            json.dumps({"blockdevices": [
                {"name": "sda", "path": "/dev/sda", "type": "disk", "ro": False, "tran": "sata"},
                {"name": "nbd0", "path": "/dev/nbd0", "type": "disk", "ro": False},
                {"name": "drbd0", "path": "/dev/drbd0", "type": "disk", "ro": False},
            ]}),
            encoding="utf-8",
        )
        options = default_options()

        report = generator.generate_for_host("host", stage, root / "configs", options)
        watches = root / "configs/host/root/etc/sermo/watches"

        self.assertTrue((watches / "smart-sda.yml").exists())
        self.assertFalse((watches / "smart-nbd0.yml").exists())
        self.assertFalse((watches / "smart-drbd0.yml").exists())
        # diskio still covers network block devices via /proc/diskstats.
        self.assertTrue((watches / "diskio-nbd0.yml").exists())
        self.assertTrue((watches / "diskio-drbd0.yml").exists())
        for name in ("smart-nbd0", "smart-drbd0"):
            self.assertIn(
                {"kind": name, "reason": "network block device without SMART data"},
                report["skipped_watches"],
            )

    def test_root_storage_watch_is_not_a_mount_unit(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "findmnt.json").write_text(
            json.dumps({"filesystems": [{"target": "/", "fstype": "ext4"}]}),
            encoding="utf-8",
        )
        (stage / "fstab").write_text("/dev/vda1 / ext4 defaults 0 1\n", encoding="utf-8")
        options = default_options()

        report = generator.generate_for_host("host", stage, root / "configs", options)
        storage_body = (root / "configs/host/root/etc/sermo/storages/storage-root.yml").read_text(encoding="utf-8")

        self.assertNotIn("mount:", storage_body)
        self.assertEqual(report["mount_units"], [])
        self.assertFalse((root / "configs/host/root/etc/sermo/watches/watch-users.yml").exists())

    def test_generates_safe_storage_checks_for_local_filesystem_types(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "findmnt.json").write_text(
            json.dumps({"filesystems": [
                {"target": "/", "fstype": "ext4"},
                {"target": "/srv/xfs", "fstype": "xfs"},
                {"target": "/media/fat", "fstype": "vfat"},
                {"target": "/data", "fstype": "btrfs"},
            ]}),
            encoding="utf-8",
        )
        options = default_options()

        report = generator.generate_for_host("host", stage, root / "configs", options)

        self.assertEqual(
            report["filesystems"],
            [
                {"name": "storage-root", "path": "/", "fstype": "ext4"},
                {"name": "storage-srv-xfs", "path": "/srv/xfs", "fstype": "xfs"},
                {"name": "storage-media-fat", "path": "/media/fat", "fstype": "vfat"},
                {"name": "storage-data", "path": "/data", "fstype": "btrfs"},
            ],
        )
        for name in ["storage-root", "storage-srv-xfs", "storage-media-fat", "storage-data"]:
            body = (root / f"configs/host/root/etc/sermo/storages/{name}.yml").read_text(encoding="utf-8")
            self.assertIn("type: storage", body)
            self.assertIn("mounted: true", body)

    def test_fuse_network_client_mount_is_mount_only(self):
        """findmnt spells a Gluster client mount `fuse.glusterfs` and fstab
        spells it `glusterfs`. Both are the same network client, so it gets the
        mount-only watch every network filesystem gets — never a capacity watch
        with an `expand` action that cannot grow a remote volume."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "findmnt.json").write_text(
            json.dumps({"filesystems": [
                {"target": "/", "fstype": "ext4"},
                {"target": "/var/lib/libvirt/images.cluster", "fstype": "fuse.glusterfs"},
            ]}),
            encoding="utf-8",
        )
        (stage / "fstab").write_text(
            "/dev/vda1 / ext4 defaults 0 1\n"
            "localhost:/images /var/lib/libvirt/images.cluster glusterfs rw,noatime 0 2\n",
            encoding="utf-8",
        )
        options = default_options()

        report = generator.generate_for_host("host", stage, root / "configs", options)

        generated = root / "configs/host/root/etc/sermo"
        self.assertFalse((generated / "storages/storage-var-lib-libvirt-images-cluster.yml").exists())
        mount_body = (generated / "mounts/mount-var-lib-libvirt-images-cluster.yml").read_text(encoding="utf-8")
        self.assertIn("mounted: true", mount_body)
        self.assertNotIn("expand:", mount_body)
        self.assertNotIn("free_pct", mount_body)
        self.assertEqual(report["filesystems"], [{"name": "storage-root", "path": "/", "fstype": "ext4"}])
        self.assertEqual(
            report["mount_units"],
            [{
                "name": "mount-var-lib-libvirt-images-cluster",
                "path": "/var/lib/libvirt/images.cluster",
                "source": "localhost:/images",
                "fstype": "glusterfs",
                "folder": "mounts",
            }],
        )

    def test_generates_nfs_endpoint_check_for_fstab_mount(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "findmnt.json").write_text(
            json.dumps({"filesystems": [{"target": "/mnt/portage", "fstype": "nfs4"}]}),
            encoding="utf-8",
        )
        (stage / "fstab").write_text(
            "k2keu3.intranet:/usr/portage /mnt/portage nfs4 defaults,_netdev 0 0\n",
            encoding="utf-8",
        )
        (stage / "nfs_routes").write_text("k2keu3.intranet\t172.31.28.4\tintranet\n", encoding="utf-8")
        options = default_options()

        report = generator.generate_for_host("host", stage, root / "configs", options)

        mount_body = (root / "configs/host/root/etc/sermo/mounts/mount-mnt-portage.yml").read_text(encoding="utf-8")
        endpoint_body = (root / "configs/host/root/etc/sermo/networks/nfs-k2keu3-intranet.yml").read_text(encoding="utf-8")
        self.assertIn("type: storage", mount_body)
        self.assertIn("type: nfs", endpoint_body)
        self.assertIn('host: "k2keu3.intranet"', endpoint_body)
        self.assertIn('interface: "intranet"', endpoint_body)
        self.assertEqual(
            report["nfs_endpoints"],
            [{
                "name": "nfs-k2keu3-intranet",
                "host": "k2keu3.intranet",
                "address": "172.31.28.4",
                "interface": "intranet",
                "paths": ["/mnt/portage"],
            }],
        )

    def test_parses_ipv6_nfs_fstab_source(self):
        self.assertEqual(generator.nfs_server_from_source("[fd00:41d0::4]:/srv/backup"), "fd00:41d0::4")
        self.assertEqual(generator.nfs_server_from_source("invalid-source"), "")

    def test_generates_nfs_endpoint_for_unmounted_fstab_entry(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "fstab").write_text(
            "k2keu3.intranet:/srv/backup /mnt/backup nfs defaults,_netdev 0 0\n",
            encoding="utf-8",
        )
        options = default_options()

        report = generator.generate_for_host("host", stage, root / "configs", options)

        endpoint_body = (root / "configs/host/root/etc/sermo/networks/nfs-k2keu3-intranet.yml").read_text(encoding="utf-8")
        mount_body = (root / "configs/host/root/etc/sermo/mounts/mount-mnt-backup.yml").read_text(encoding="utf-8")
        self.assertIn("type: nfs", endpoint_body)
        self.assertIn("type: storage", mount_body)
        self.assertIn("mounted: true", mount_body)
        self.assertEqual(report["nfs_endpoints"][0]["paths"], ["/mnt/backup"])

    def test_protocol_check_types_match_conn_registry(self):
        """The gate must cover every protocol probe. A new probe in
        internal/conn that is missing here would ship an ungated ${host} check
        to every host that runs the service."""
        import re

        conn = Path(__file__).parents[2] / "internal/conn"
        registry = set()
        for source in conn.glob("*.go"):
            registry.update(re.findall(r'ProtocolName\w+\s*=\s*"([^"]+)"', source.read_text(encoding="utf-8")))
        self.assertTrue(registry, "no protocol names found in internal/conn")
        # `dns` is gated by the older URL/port-aware path in ENDPOINT_CHECK_TYPES.
        self.assertEqual(generator.PROTOCOL_CHECK_TYPES, registry - {"dns"})
        self.assertFalse(generator.PROTOCOL_CHECK_TYPES & generator.ENDPOINT_CHECK_TYPES)

    def test_protocol_watch_disabled_without_listening_socket(self):
        """The ceph-mon case: the monitor binds its cluster IP, the profile
        probes ${host}, and the rule's action is `restart`. Without evidence the
        watch must be disabled rather than restart a healthy quorum member."""
        doc = {
            "watches": {
                "restart-if-messenger-failed": {"check": {"type": "ceph", "host": "${host}", "port": "${port}"}},
                "socket": {"check": {"type": "socket", "path": "/run/ceph/x.asok"}},
            }
        }
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "service_endpoint_hints").write_text("", encoding="utf-8")

        disabled, report = generator.endpoint_watch_overrides(stage, doc, {"host": "127.0.0.1", "port": "3300"})

        self.assertEqual(disabled, {"restart-if-messenger-failed"})
        self.assertEqual([item["watch"] for item in report], ["restart-if-messenger-failed"])

    def test_failed_units_are_monitorable(self):
        """A failed unit is installed, enabled and broken. Excluding it left the
        genuinely broken service (ceph-mon@radon) unmonitored while its healthy
        peers were watched."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        # systemd prints a status bullet for non-running units when --plain is absent.
        (stage / "failed_units").write_text(
            "● ceph-mon@radon.service loaded failed failed Ceph cluster monitor\n"
            "squid.service            loaded failed failed Squid proxy\n",
            encoding="utf-8",
        )

        failed = generator.parse_failed_units(stage)

        self.assertIn("ceph-mon@radon.service", failed)
        self.assertIn("ceph-mon@radon", failed)
        self.assertIn("squid.service", failed)
        self.assertNotIn("●", failed)

    def test_failed_sntp_unit_generates_a_repairable_service(self):
        """A one-shot unit that failed at boot must remain visible and operable."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "potasio" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "failed_units").write_text(
            "sntp.service loaded failed failed Set time via SNTP\n", encoding="utf-8"
        )
        (stage / "services_all_json.out").write_text(
            json.dumps(
                {
                    "services": [
                        {
                            "name": "sntp",
                            "installed": False,
                            "ok": False,
                            "status": "no binary configured",
                        }
                    ]
                }
            ),
            encoding="utf-8",
        )

        report = generator.generate_for_host("potasio", stage, root / "configs", default_options())

        service = root / "configs/potasio/root/etc/sermo/services/sntp.yml"
        self.assertTrue(service.exists())
        self.assertIn("uses: sntp", service.read_text(encoding="utf-8"))
        self.assertEqual(report["services"]["enabled"][0]["name"], "sntp")
        self.assertEqual(report["services"]["enabled"][0]["endpoint_checks"], [
            {"watch": "*", "active": True, "source": "unit failed; endpoint gating skipped"},
        ])

    def test_failed_epmd_owned_by_rabbitmq_is_not_a_separate_control_target(self):
        """RabbitMQ's EPMD must not be repaired through a stale OpenRC unit."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "fr1" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("openrc\n", encoding="utf-8")
        (stage / "openrc_status_all").write_text(" epmd    [  crashed  ]\n", encoding="utf-8")
        (stage / "service_endpoint_hints").write_text(
            "process epmd user rabbitmq exe /usr/lib64/erlang/erts/bin/epmd\n",
            encoding="utf-8",
        )
        (stage / "services_all_json.out").write_text(
            json.dumps({"services": [{"name": "epmd", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )

        report = generator.generate_for_host("fr1", stage, root / "configs", default_options())

        self.assertFalse((root / "configs/fr1/root/etc/sermo/services/epmd.yml").exists())
        self.assertEqual(report["services"]["skipped"], [
            {
                "name": "epmd",
                "status": "failed epmd unit is owned by RabbitMQ; monitor and repair rabbitmq instead",
                "installed": True,
                "ok": True,
            }
        ])

    def test_failed_epmd_with_its_own_owner_remains_repairable(self):
        """A genuinely failed EPMD unit remains visible to the operator."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "fr1" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("openrc\n", encoding="utf-8")
        (stage / "openrc_status_all").write_text(" epmd    [  crashed  ]\n", encoding="utf-8")
        (stage / "service_endpoint_hints").write_text(
            "process epmd user epmd exe /usr/lib64/erlang/erts/bin/epmd\n",
            encoding="utf-8",
        )
        (stage / "services_all_json.out").write_text(
            json.dumps({"services": [{"name": "epmd", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )

        generator.generate_for_host("fr1", stage, root / "configs", default_options())

        self.assertTrue((root / "configs/fr1/root/etc/sermo/services/epmd.yml").exists())

    def test_systemd_only_service_is_skipped_on_openrc(self):
        """lvm2-monitor and mdmonitor declare `service: {systemd: [...]}` only, but
        their app probe finds lvm/mdadm installed on OpenRC hosts too. Generating
        them there produced a service whose unit can never resolve, and every
        daemon start logged "service is not available on openrc" for it."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "kvm9" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("openrc\n", encoding="utf-8")
        (stage / "openrc_status_all").write_text(" acpid    [  started  ]\n", encoding="utf-8")
        (stage / "services_all_json.out").write_text(
            json.dumps(
                {
                    "services": [
                        {"name": "lvm2-monitor", "installed": True, "ok": True, "status": "ok"},
                        {"name": "acpid", "installed": True, "ok": True, "status": "ok"},
                    ]
                }
            ),
            encoding="utf-8",
        )

        report = generator.generate_for_host("kvm9", stage, root / "configs", default_options())

        services = root / "configs/kvm9/root/etc/sermo/services"
        self.assertFalse((services / "lvm2-monitor.yml").exists())
        skipped = {entry["name"]: entry["status"] for entry in report["services"]["skipped"]}
        self.assertEqual(skipped.get("lvm2-monitor"), "catalog profile declares no unit for openrc")
        # A backend-neutral profile is untouched by the filter.
        self.assertTrue((services / "acpid.yml").exists())

    def test_systemd_only_service_is_generated_on_systemd(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "fr5" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("lvm2-monitor.service\n", encoding="utf-8")
        (stage / "services_all_json.out").write_text(
            json.dumps({"services": [{"name": "lvm2-monitor", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )

        generator.generate_for_host("fr5", stage, root / "configs", default_options())

        self.assertTrue((root / "configs/fr5/root/etc/sermo/services/lvm2-monitor.yml").exists())

    def test_openrc_crashed_units_are_monitorable(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "init").write_text("openrc\n", encoding="utf-8")
        (stage / "openrc_status_all").write_text(
            " nginx    [  started  ]\n squid    [  crashed  ]\n", encoding="utf-8"
        )

        self.assertEqual(generator.parse_failed_units(stage), {"squid"})

    def test_profile_host_variable_wins_over_hostname_builtin(self):
        """docs/services.md: an explicit `host` variable always wins over the
        ${host} fallback. When the builtin won, endpoint evidence was looked up
        at the hostname instead of the pinned bind address, disabling watches
        for endpoints that were in fact being served."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "hostname").write_text("kvm5.example.com\n", encoding="utf-8")
        docs = [{"name": "mariadb", "variables": {"host": "127.0.0.1", "port": 3306}}]

        doc, values = generator.values_for_service("mariadb", stage, docs)
        variables = generator.effective_service_variables(doc, values, {})

        self.assertEqual(variables["host"], "127.0.0.1")
        # A profile that pins no host still gets the hostname fallback.
        bare = [{"name": "other", "variables": {"port": 1}}]
        _, bare_values = generator.values_for_service("other", stage, bare)
        self.assertEqual(bare_values["host"], "kvm5")

    def test_listener_without_process_attribution_counts_as_evidence(self):
        """Kernel sockets (nfsd) report no owning process. Treating that as
        'nothing is listening' disabled checks for served endpoints."""
        listeners = generator.socket_listeners("socket tcp LISTEN 0 4096 0.0.0.0:2049 0.0.0.0:*\n")

        self.assertEqual(len(listeners), 1)
        self.assertEqual(listeners[0]["processes"], set())
        self.assertTrue(generator.listener_serves_endpoint(({"tcp", "udp"}, "127.0.0.1", "2049"), listeners[0]))

    def test_protocol_watch_kept_when_port_unresolved(self):
        """A probe whose port cannot be resolved is not provable either way, so
        it stays enabled: disabling it would hide a real outage."""
        doc = {"watches": {"probe": {"check": {"type": "dbus", "host": "${host}"}}}}
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "service_endpoint_hints").write_text("", encoding="utf-8")

        disabled, report = generator.endpoint_watch_overrides(stage, doc, {"host": "127.0.0.1"})

        self.assertEqual(disabled, set())
        self.assertEqual(report, [])

    def test_skips_mount_watch_for_on_demand_fstab_entry(self):
        """A `noauto`/`x-systemd.automount` share is unmounted by design, so a
        `mounted: true` watch would alert on the operator's intent forever. The
        NFS endpoint check stays: server reachability is still meaningful."""
        for options_field in ("noatime,noauto", "defaults,x-systemd.automount"):
            with self.subTest(options=options_field):
                temp = tempfile.TemporaryDirectory()
                self.addCleanup(temp.cleanup)
                root = Path(temp.name)
                stage = root / "stage" / "host" / "out"
                stage.mkdir(parents=True)
                (stage / "init").write_text("systemd\n", encoding="utf-8")
                (stage / "active_units").write_text("", encoding="utf-8")
                (stage / "fstab").write_text(
                    f"k2keu3.intranet:/srv/backup /mnt/backup nfs {options_field} 0 0\n",
                    encoding="utf-8",
                )

                report = generator.generate_for_host("host", stage, root / "configs", default_options())

                self.assertFalse((root / "configs/host/root/etc/sermo/mounts/mount-mnt-backup.yml").exists())
                self.assertIn(
                    "type: nfs",
                    (root / "configs/host/root/etc/sermo/networks/nfs-k2keu3-intranet.yml").read_text(encoding="utf-8"),
                )
                reasons = [entry["reason"] for entry in report["skipped_watches"] if entry["kind"] == "mount"]
                self.assertTrue(any("/mnt/backup" in reason and "on demand" in reason for reason in reasons), reasons)

    def test_parse_fstab_records_options(self):
        entries = generator.parse_fstab("src /mnt/a nfs noatime,noauto 0 0\nsrc2 /mnt/b ext4 defaults 0 1\nsrc3 /mnt/c xfs\n")
        self.assertEqual([entry["options"] for entry in entries], ["noatime,noauto", "defaults", "defaults"])
        self.assertTrue(generator.fstab_is_on_demand(entries[0]))
        self.assertFalse(generator.fstab_is_on_demand(entries[1]))

    def test_skips_net_watch_for_carrierless_and_tap_interfaces(self):
        """`state: expect: down` is an alert condition, so a link that is
        already operationally down must not get a watch."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "ip_link").write_text(
            "1: lo: <LOOPBACK,UP,LOWER_UP> mtu 65536\n"
            "2: eth0: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500\n"
            "3: eth1: <NO-CARRIER,BROADCAST,MULTICAST,UP> mtu 1500\n"
            "4: vnet68: <NO-CARRIER,BROADCAST,MULTICAST,UP> mtu 1500\n",
            encoding="utf-8",
        )

        self.assertEqual(generator.parse_interfaces(stage), ["eth0"])

        generator.generate_for_host("host", stage, root / "configs", default_options())

        networks = root / "configs/host/root/etc/sermo/networks"
        self.assertTrue((networks / "net-eth0.yml").exists())
        self.assertFalse((networks / "net-eth1.yml").exists())
        self.assertFalse((networks / "net-vnet68.yml").exists())

    def test_hdparm_read_floor_follows_disk_medium(self):
        """One shared floor either alerts on every healthy HDD or never fires on
        an SSD, so the threshold follows the medium lsblk reports."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "features").write_text("hdparm=1\n", encoding="utf-8")
        (stage / "lsblk.json").write_text(
            json.dumps({
                "blockdevices": [
                    {"name": "sda", "type": "disk", "rota": True, "tran": "ata", "ro": False},
                    {"name": "sdb", "type": "disk", "rota": False, "tran": "ata", "ro": False},
                ]
            }),
            encoding="utf-8",
        )

        generator.generate_for_host("host", stage, root / "configs", default_options())

        watches = root / "configs/host/root/etc/sermo/watches"
        self.assertIn(
            f'read: {{ op: "<", value: {generator.HDPARM_READ_FLOOR_ROTATIONAL} }}',
            (watches / "hdparm-sda.yml").read_text(encoding="utf-8"),
        )
        self.assertIn(
            f'read: {{ op: "<", value: {generator.HDPARM_READ_FLOOR_SOLID_STATE} }}',
            (watches / "hdparm-sdb.yml").read_text(encoding="utf-8"),
        )

    def test_generates_geoip_summary_when_database_directory_exists(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "geoip_directory").write_text(f"{generator.GEOIP_DATABASE_DIRECTORY}\n", encoding="utf-8")
        options = default_options()

        generator.generate_for_host("host", stage, root / "configs", options)

        body = (root / "configs/host/root/etc/sermo/watches/geoip-database-freshness.yml").read_text(encoding="utf-8")
        self.assertIn('summary: "GeoIP ${value} is older than ${older_than} in ${number_files} files"', body)

class PostgresReplicationGenerationTest(unittest.TestCase):
    """The replication watches must reach only nodes whose cluster facts can
    make them fire. The scenarios mirror the measured fleet: a primary with two
    slots and two walsenders, and a standby with none."""

    def postgres_doc(self):
        docs = generator.load_catalog_services(default_options().catalog_services_dir)
        doc, _ = generator.catalog_doc_for_service("postgres-18", docs)
        self.assertIsNotNone(doc, "the postgres catalog service must resolve")
        return doc

    def overrides(self, clusters_evidence: str | None):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        if clusters_evidence is not None:
            (stage / "postgres_clusters").write_text(clusters_evidence, encoding="utf-8")
        return generator.replication_watch_overrides(stage, self.postgres_doc())

    def test_catalog_ships_every_gated_replication_watch(self):
        # Locks the gate table against the catalog: a renamed watch would
        # otherwise silently stop being gated and ship everywhere.
        watches = self.postgres_doc().get("watches", {})
        for watch_name in generator.POSTGRES_REPLICATION_WATCHES:
            self.assertIn(watch_name, watches)

    def test_primary_with_slots_and_walsenders_keeps_the_primary_side_watches(self):
        disabled, checks = self.overrides("/var/lib/postgresql/18/data\tprimary\t2\t2\n")
        self.assertEqual(disabled, {"alert-if-standby-replay-delay"})
        active = {item["watch"]: item["active"] for item in checks}
        self.assertTrue(active["alert-if-replication-slot-backlog"])
        self.assertTrue(active["alert-if-logical-slot-unconfirmed"])
        self.assertTrue(active["alert-if-replication-slot-inactive"])
        self.assertTrue(active["alert-if-replication-replay-lag"])
        self.assertFalse(active["alert-if-standby-replay-delay"])

    def test_standby_without_slots_keeps_only_the_standby_watch(self):
        disabled, checks = self.overrides("/var/lib/postgresql/18/data\tstandby\t0\t0\n")
        self.assertEqual(
            disabled,
            {
                "alert-if-replication-slot-backlog",
                "alert-if-logical-slot-unconfirmed",
                "alert-if-replication-slot-inactive",
                "alert-if-replication-replay-lag",
            },
        )
        active = {item["watch"]: item["active"] for item in checks}
        self.assertTrue(active["alert-if-standby-replay-delay"])

    def test_standalone_postgres_disables_every_replication_watch(self):
        disabled, checks = self.overrides("/var/lib/postgresql/18/data\tprimary\t0\t0\n")
        self.assertEqual(disabled, set(generator.POSTGRES_REPLICATION_WATCHES))
        reasons = {item["watch"]: item["reason"] for item in checks}
        self.assertEqual(reasons["alert-if-replication-slot-backlog"], "no replication slot present on this host")
        self.assertEqual(reasons["alert-if-replication-replay-lag"], "no walsender connected to this primary")
        self.assertEqual(reasons["alert-if-standby-replay-delay"], "host is not a standby")

    def test_missing_cluster_evidence_disables_with_its_own_reason(self):
        disabled, checks = self.overrides(None)
        self.assertEqual(disabled, set(generator.POSTGRES_REPLICATION_WATCHES))
        for item in checks:
            self.assertEqual(item["reason"], "no running PostgreSQL cluster discovered")

    def test_malformed_evidence_lines_are_ignored(self):
        disabled, _ = self.overrides(
            "not-enough-fields\n"
            "/data\tunknown-role\t2\t2\n"
            "/data\tprimary\tmany\t2\n"
            "/var/lib/postgresql/18/data\tstandby\t0\t0\n"
        )
        self.assertNotIn("alert-if-standby-replay-delay", disabled)
        self.assertIn("alert-if-replication-slot-backlog", disabled)

    def test_non_postgres_service_is_untouched(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "postgres_clusters").write_text("/data\tprimary\t2\t2\n", encoding="utf-8")
        disabled, checks = generator.replication_watch_overrides(stage, {"name": "nginx", "watches": {"http": {}}})
        self.assertEqual(disabled, set())
        self.assertEqual(checks, [])

    def test_generated_service_file_disables_the_watches_and_audits_them(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("postgresql-18.service\n", encoding="utf-8")
        (stage / "postgres_clusters").write_text("/var/lib/postgresql/18/data\tstandby\t0\t0\n", encoding="utf-8")
        (stage / "services_json.out").write_text(
            json.dumps({"services": [{"name": "postgres-18", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )
        options = default_options()

        report = generator.generate_for_host("host", stage, root / "configs", options)

        body = (root / "configs/host/root/etc/sermo/services/postgres-18.yml").read_text(encoding="utf-8")
        self.assertIn("  alert-if-replication-slot-backlog:\n    enabled: false", body)
        self.assertIn("  alert-if-replication-replay-lag:\n    enabled: false", body)
        self.assertNotIn("alert-if-standby-replay-delay:\n    enabled: false", body)
        entry = next(item for item in report["services"]["enabled"] if item["name"] == "postgres-18")
        self.assertEqual(
            {item["watch"] for item in entry["replication_checks"] if not item["active"]},
            {
                "alert-if-replication-slot-backlog",
                "alert-if-logical-slot-unconfirmed",
                "alert-if-replication-slot-inactive",
                "alert-if-replication-replay-lag",
            },
        )


class ProcessPolicyGenerationTest(unittest.TestCase):
    """Only reviewed PostgreSQL executables may enable a process policy."""

    def generate(self, evidence: str):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "process_policy.tsv").write_text(evidence, encoding="utf-8")
        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        policy_path = root / "configs/host/root/etc/sermo/watches/security-user-postgres.yml"
        return report, policy_path

    def test_generates_an_alert_only_policy_from_reviewed_postmaster_paths(self):
        report, policy_path = self.generate(
            "100\t110\tpostgres\tresolved\t/usr/lib/postgresql/18/bin/postgres\t\n"
            "101\t110\tpostgres\tdeleted\t\t/usr/lib/postgresql/18/bin/postgres\n"
            "102\t110\tpostgres\tresolved\t/tmp/postgres\t\n"
            "103\t110\tpostgres\tresolved\t/usr/bin/bash\t\n"
        )

        policy = yaml.safe_load(policy_path.read_text(encoding="utf-8"))
        self.assertEqual(policy["name"], "security-user-postgres")
        self.assertEqual(policy["category"], "security")
        self.assertTrue(policy["dry_run"])
        self.assertNotIn("then", policy)
        self.assertEqual(policy["check"]["type"], "process_policy")
        self.assertEqual(policy["check"]["user"], "postgres")
        self.assertEqual(
            policy["check"]["allow"],
            {"postgres-1": {"exe": "/usr/lib/postgresql/18/bin/postgres"}},
        )
        self.assertEqual(report["process_policies"][0]["processes"], 4)
        self.assertEqual(
            report["process_policies"][0]["deleted_executables"],
            ["/usr/lib/postgresql/18/bin/postgres"],
        )

    def test_unreviewed_postgres_processes_do_not_enable_a_policy(self):
        report, policy_path = self.generate(
            "100\t110\tpostgres\tresolved\t/tmp/postgres\t\n"
            "101\t110\tpostgres\tresolved\t/usr/bin/bash\t\n"
        )

        self.assertFalse(policy_path.exists())
        self.assertEqual(report["process_policies"], [])
        self.assertIn(
            {"kind": "process_policy:postgres", "reason": "postgres has no reviewed postmaster executable path"},
            report["skipped_watches"],
        )

    def test_collectors_keep_process_identity_evidence_credential_free(self):
        for filename in ("remote_collect_inventory.sh", "remote_stage.sh"):
            body = (Path(__file__).with_name(filename)).read_text(encoding="utf-8")
            policy_start = body.index("# Execution-policy evidence")
            policy_end = body.index("process_policy.tsv", policy_start)
            policy_block = body[policy_start:policy_end]
            self.assertIn('raw_exe="$(readlink "${pid}/exe"', policy_block)
            self.assertNotIn("${pid}/cmdline", policy_block)


class OomWatchGenerationTest(unittest.TestCase):
    """The OOM watch must fire on the first kill it sees."""

    def _generate_oom_watch(self) -> str:
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        generator.generate_for_host("host", stage, root / "configs", default_options())
        return (root / "configs/host/root/etc/sermo/watches/watch-oom.yml").read_text(encoding="utf-8")

    def test_oom_watch_has_no_multi_cycle_window(self):
        # oom_kill is a cumulative event counter and the check consumes the delta, so a
        # kill makes the condition true for exactly one cycle. Any `for:` window asking
        # for two or more consecutive cycles can never be satisfied, and the kill is
        # reported nowhere -- which is how a real OOM on fr3 went unnoticed.
        # TestOomConditionHoldsForOneCycleOnly pins the one-cycle property in Go.
        body = self._generate_oom_watch()
        self.assertIn("type: oom", body)
        self.assertNotIn("for:", body)

    def test_oom_watch_still_evaluates_and_reports(self):
        # dry_run keeps evaluation and firing events active (internal/app/watch.go), so
        # the kill still surfaces as an event and a failing watch state.
        body = self._generate_oom_watch()
        self.assertIn("monitor: enabled", body)
        self.assertIn("interval: 30s", body)

    def test_oom_watch_holds_the_alert_for_an_hour(self):
        # With the condition true for a single cycle, the recovery window is the alert's
        # whole visible life: without it the sensor would flash for one 30s cycle and go
        # green again. Spelled out rather than inherited from the global 5m default,
        # which is both invisible here and too short to be noticed.
        body = self._generate_oom_watch()
        self.assertIn("clear: { duration: 1h }", body)

    def test_level_watches_do_not_gain_an_explicit_clear(self):
        # The explicit recovery window is the edge sensor's exception, not a new rule for
        # every watch: a level check describes a state that persists, so its alert lasts
        # as long as the condition and the global default is the right hysteresis.
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        generator.generate_for_host("host", stage, root / "configs", default_options())
        body = (root / "configs/host/root/etc/sermo/watches/watch-memory.yml").read_text(encoding="utf-8")
        self.assertIn("for: { cycles: 10 }", body)
        self.assertNotIn("clear:", body)




class ClockAndDeadLetterGenerationTest(unittest.TestCase):
    """The three-tier drift policy and the dead.letter sensor must reach a host
    through the fleet deployment, not only through a hand-copied example. Tier 2
    carries a real clock step, so what it must NOT reach matters as much as what
    it must."""

    def generate(self, active_units: str):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text(active_units, encoding="utf-8")

        generator.generate_for_host("host", stage, root / "configs", options=default_options())
        return root / "configs/host/root/etc/sermo/watches"

    @staticmethod
    def unit_lines(*units: str) -> str:
        # `systemctl list-units` columns: UNIT LOAD ACTIVE SUB DESCRIPTION.
        return "".join(f"{unit} loaded active running {unit}\n" for unit in units)

    def test_tier_one_alerts_at_one_second_on_every_host(self):
        body = (self.generate("") / "watch-clock-drift.yml").read_text(encoding="utf-8")
        self.assertIn("max_offset: 1s", body)
        self.assertNotIn("max_offset: 3s", body)

    def test_tier_two_steps_the_clock_on_a_chrony_host(self):
        watches = self.generate(self.unit_lines("chronyd.service"))
        body = (watches / "watch-clock-step.yml").read_text(encoding="utf-8")
        self.assertIn("max_offset: 5s", body)
        self.assertIn("source: chrony", body)
        self.assertIn(f"socket: {generator.CHRONY_COMMAND_SOCKET}", body)
        self.assertIn("makestep:", body)
        # then.makestep is refused without a positive cooldown, and the fleet
        # posture must keep the step reported rather than performed.
        self.assertIn("cooldown: 30m", body)
        self.assertIn("dry_run: true", body)

    def test_tier_two_never_reaches_a_ceph_host(self):
        # The dangerous case is a ceph node that also runs chrony: everything
        # tier 2 needs is present, and stepping the clock there can cost the
        # monitor its quorum.
        watches = self.generate(self.unit_lines("chronyd.service", "ceph-mon@bk1.service"))
        self.assertFalse((watches / "watch-clock-step.yml").exists())
        # Tier 1 is read-only, so a ceph host still gets the alert.
        self.assertTrue((watches / "watch-clock-drift.yml").exists())

    def test_tier_two_is_skipped_without_chrony(self):
        # ntpd and systemd-timesyncd have no step command at all; their forced
        # correction is the catalog's restart-if-clock-drifting watch.
        for unit in ("ntpd.service", "systemd-timesyncd.service"):
            with self.subTest(unit=unit):
                watches = self.generate(self.unit_lines(unit))
                self.assertFalse((watches / "watch-clock-step.yml").exists())

    def test_dead_letter_watch_reaches_every_host(self):
        body = (self.generate("") / "watch-dead-letter.yml").read_text(encoding="utf-8")
        self.assertIn("type: file", body)
        self.assertIn(generator.DEAD_LETTER_PATH, body)
        self.assertIn('size: { op: ">", value: 0 }', body)


class SwapWatchGenerationTest(unittest.TestCase):
    """Swap usage is a capacity signal and swap io is the pressure one. Keeping
    both, and keeping usage high enough that cold pages do not flag a host, is a
    measured decision: across this fleet every host above the old 80% had
    pswpin/pswpout at zero."""

    def generate(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "proc_swaps").write_text(
            "Filename\t\t\t\tType\t\tSize\t\tUsed\t\tPriority\n"
            "/dev/sda2                               partition\t999420\t\t512\t\t-2\n",
            encoding="utf-8",
        )
        generator.generate_for_host("host", stage, root / "configs", options=default_options())
        return (root / "configs/host/root/etc/sermo/watches/watch-swap.yml").read_text(encoding="utf-8")

    def test_usage_threshold_is_ninety_five(self):
        body = self.generate()
        self.assertIn('used_pct: { op: ">", value: 95 }', body)
        self.assertNotIn("value: 80", body)

    def test_io_pressure_metric_is_kept(self):
        # Raising the usage threshold only makes sense while the metric that
        # detects real thrashing is still there.
        body = self.generate()
        self.assertIn("io:", body)
        self.assertIn('delta: { op: ">", value: 1000 }', body)


class FirewallWatchGenerationTest(unittest.TestCase):
    def generate(self, init: str, active_inventory: str, features: str = "nft=1\niptables=1\n"):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text(init + "\n", encoding="utf-8")
        inventory_name = "active_units" if init == "systemd" else "openrc_status_all"
        (stage / inventory_name).write_text(active_inventory, encoding="utf-8")
        (stage / "features").write_text(features, encoding="utf-8")
        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        return root / "configs/host/root/etc/sermo/watches", report

    def test_requires_an_active_supported_firewall_unit(self):
        cases = {
            "systemd-firewalld": ("systemd", "firewalld.service loaded active running firewalld\n", True),
            "systemd-nftables": ("systemd", "nftables.service loaded active running nftables\n", True),
            "openrc-firehol": ("openrc", " firehol    [  started  ]\n", True),
            "systemd-tools-only": ("systemd", "nginx.service loaded active running nginx\n", False),
            "openrc-tools-only": ("openrc", " nginx    [  started  ]\n", False),
        }
        for name, (init, inventory, want_watch) in cases.items():
            with self.subTest(name=name):
                watches, report = self.generate(init, inventory)
                watch = watches / "watch-firewall-rules.yml"
                self.assertEqual(watch.exists(), want_watch)
                if not want_watch:
                    self.assertIn(
                        {"kind": "firewall_rules", "reason": "no active supported firewall service"},
                        report["skipped_watches"],
                    )

class WebCredentialBlockTest(unittest.TestCase):
    """sermo.yml must not carry a second copy of a password the host already has."""

    def test_embeds_the_password_when_the_host_has_no_credentials_file(self):
        block = generator.web_credential_block(default_options())
        self.assertIn("password:", block)
        self.assertNotIn("password_file:", block)

    def test_references_the_credentials_file_when_one_exists(self):
        options = dataclasses.replace(
            default_options(), web_password_file="/etc/sermo/credentials.env"
        )
        block = generator.web_credential_block(options)
        self.assertIn("password_file:", block)
        self.assertIn("/etc/sermo/credentials.env", block)
        # The literal must not survive alongside the reference: two copies drift,
        # and this is the copy that lands in backups.
        self.assertNotIn(default_options().web_password, block)


class EximHintsGenerationTest(unittest.TestCase):
    """The tidy watches run a SQLite query, but Exim writes SQLite hints only
    when built for them. On the measured fleet 101 of 112 watch instances could
    never read their database — 43 absent, 33 with no tblblob table, 25 not a
    SQLite file at all — so only a host whose hints really are SQLite should
    carry them."""

    def exim_doc(self):
        docs = generator.load_catalog_services(default_options().catalog_services_dir)
        doc, _ = generator.catalog_doc_for_service(generator.EXIM_CATALOG_SERVICE, docs)
        self.assertIsNotNone(doc, "the exim catalog service must resolve")
        return doc

    def overrides(self, hints_evidence: str | None):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        if hints_evidence is not None:
            (stage / "exim_hints").write_text(hints_evidence, encoding="utf-8")
        return generator.exim_hints_watch_overrides(stage, self.exim_doc())

    def test_catalog_ships_every_gated_hints_watch(self):
        # Locks the gate table against the catalog: a renamed watch would
        # otherwise silently stop being gated and ship everywhere.
        watches = self.exim_doc().get("watches", {})
        for watch_name in generator.EXIM_HINTS_WATCHES:
            self.assertIn(watch_name, watches)

    def test_gate_probes_the_paths_the_catalog_actually_queries(self):
        # The hints paths live in three places — this table, the catalog watch
        # and the collectors' probe — and only the catalog can move them. Resolve
        # the watch's own ${db_dir} and require the table to agree, so a catalog
        # change cannot leave the gate probing a file nothing reads.
        doc = self.exim_doc()
        db_dir = str((doc.get("variables") or {})["db_dir"])
        for watch_name, gated_path in generator.EXIM_HINTS_WATCHES.items():
            check = doc["watches"][watch_name]["check"]
            self.assertEqual(check.get("engine"), "sqlite", watch_name)
            resolved = str(check["path"]).replace("${db_dir}", db_dir)
            self.assertEqual(resolved, gated_path, watch_name)

    def test_collectors_probe_every_gated_path(self):
        # Both collectors are uploaded standalone, so each carries its own copy
        # of the probe. A path added here must appear in both or the generator
        # silently sees no evidence and leaves a broken watch enabled.
        root = Path(__file__).resolve().parent
        for script in ("remote_collect_inventory.sh", "remote_stage.sh"):
            body = (root / script).read_text(encoding="utf-8")
            self.assertIn("exim_hints", body, script)
            for gated_path in generator.EXIM_HINTS_WATCHES.values():
                self.assertIn(gated_path, body, f"{script} does not probe {gated_path}")

    def test_sqlite_hints_keep_both_watches(self):
        disabled, checks = self.overrides(
            "/var/spool/exim/db/callout\tsqlite\n/var/spool/exim/db/retry\tsqlite\n"
        )
        self.assertEqual(disabled, set())
        self.assertTrue(all(item["active"] for item in checks))

    def test_berkeley_db_hints_disable_both_watches(self):
        disabled, checks = self.overrides(
            "/var/spool/exim/db/callout\tother\n/var/spool/exim/db/retry\tother\n"
        )
        self.assertEqual(disabled, set(generator.EXIM_HINTS_WATCHES))
        reasons = {item["watch"]: item["reason"] for item in checks}
        self.assertIn("is not a SQLite database", reasons["tidy-callout-db-if-large"])

    def test_absent_hints_file_disables_its_watch_with_that_reason(self):
        disabled, checks = self.overrides(
            "/var/spool/exim/db/callout\tabsent\n/var/spool/exim/db/retry\tsqlite\n"
        )
        self.assertEqual(disabled, {"tidy-callout-db-if-large"})
        reasons = {item["watch"]: item.get("reason") for item in checks}
        self.assertIn("does not exist", reasons["tidy-callout-db-if-large"])
        self.assertIsNone(reasons["tidy-retry-db-if-large"])

    def test_unreadable_hints_file_leaves_its_watch_enabled(self):
        # The collectors report "unknown" when the file could not be opened.
        # Disabling on that would silence a working watch over a read error.
        disabled, checks = self.overrides(
            "/var/spool/exim/db/callout\tunknown\n/var/spool/exim/db/retry\tother\n"
        )
        self.assertEqual(disabled, {"tidy-retry-db-if-large"})
        watched = {item["watch"] for item in checks}
        self.assertNotIn("tidy-callout-db-if-large", watched)

    def test_missing_evidence_leaves_the_watches_enabled(self):
        # Absence of proof is not proof of absence: a host staged before this
        # fact was collected must not have working watches switched off.
        disabled, checks = self.overrides(None)
        self.assertEqual(disabled, set())
        self.assertEqual(checks, [])

    def test_other_services_are_untouched(self):
        disabled, checks = generator.exim_hints_watch_overrides(
            Path("/nonexistent"), {"name": "nginx", "watches": {"http": {}}}
        )
        self.assertEqual(disabled, set())
        self.assertEqual(checks, [])

    def generate(self, hints_evidence: str):
        """Drive the whole generator for an exim host and return its report plus
        the emitted service YAML."""
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("exim.service\n", encoding="utf-8")
        (stage / "exim_hints").write_text(hints_evidence, encoding="utf-8")
        (stage / "services_json.out").write_text(
            json.dumps({"services": [{"name": "exim", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )
        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        body = (root / "configs/host/root/etc/sermo/services/exim.yml").read_text(encoding="utf-8")
        return report, body

    def test_generated_config_disables_the_watches_on_a_berkeley_db_host(self):
        # End to end: the emitted YAML must actually carry the disable, not just
        # the override helper's return value.
        report, body = self.generate(
            "/var/spool/exim/db/callout\tother\n/var/spool/exim/db/retry\tother\n"
        )
        self.assertIn("watches:", body)
        for watch_name in generator.EXIM_HINTS_WATCHES:
            self.assertIn(watch_name, body)
        self.assertIn("enabled: false", body)
        checks = report["services"]["enabled"][0]["exim_hints_checks"]
        self.assertEqual([item["active"] for item in checks], [False, False])

    def test_generated_config_keeps_the_watches_on_a_sqlite_host(self):
        report, body = self.generate(
            "/var/spool/exim/db/callout\tsqlite\n/var/spool/exim/db/retry\tsqlite\n"
        )
        for watch_name in generator.EXIM_HINTS_WATCHES:
            self.assertNotIn(watch_name, body)
        checks = report["services"]["enabled"][0]["exim_hints_checks"]
        self.assertEqual([item["active"] for item in checks], [True, True])


class LibvirtDomainStateTest(unittest.TestCase):
    """virsh translates its output. A Spanish host reported every running domain
    as "ejecutando", which the running-check read as stopped — regenerating that
    host's configuration would have deleted all nine of its VM watches."""

    def parse(self, tsv: str):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "libvirt_domains.tsv").write_text(tsv, encoding="utf-8")
        return generator.parse_libvirt_domains(stage)

    def test_running_domain_is_generated(self):
        domains, skipped = self.parse("/run/libvirt/virtqemud-sock\tqemu:///system\tkvm5\trunning\n")
        self.assertEqual([d["domain"] for d in domains], ["kvm5"])
        self.assertEqual(skipped, [])

    def test_genuinely_stopped_domain_is_skipped_as_such(self):
        domains, skipped = self.parse("/run/libvirt/virtqemud-sock\tqemu:///system\tkvm5\tshut off\n")
        self.assertEqual(domains, [])
        self.assertEqual(skipped[0]["reason"], "domain is not running")

    def test_localized_state_is_reported_as_a_parse_failure(self):
        # The exact string observed on 172.31.16.17.
        domains, skipped = self.parse("/run/libvirt/virtqemud-sock\tqemu:///system\tkvm5\tejecutando\n")
        self.assertEqual(domains, [])
        self.assertIn("unrecognized domain state", skipped[0]["reason"])
        self.assertIn("ejecutando", skipped[0]["reason"])

    def test_collectors_pin_the_locale(self):
        # The root cause: without LC_ALL=C the state string is translated.
        root = Path(__file__).resolve().parent
        for script in ("remote_collect_inventory.sh", "remote_stage.sh", "collect_runtime_targets.sh"):
            body = (root / script).read_text(encoding="utf-8")
            self.assertIn("export LC_ALL=C", body, f"{script} must pin the locale")


class FailedUnitsWatchGenerationTest(unittest.TestCase):
    """A failed unit with no catalog profile is invisible to service monitoring.
    Observed on k2keu2: `backup_kvm.service` had been failed for days, the host
    reported `degraded`, and Sermo said nothing about either."""

    def generate(self, init: str):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text(f"{init}\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        return root / "configs/host/root/etc/sermo", report

    def test_names_the_hosts_backend(self):
        for init in generator.INIT_BACKENDS:
            with self.subTest(init=init):
                generated, report = self.generate(init)
                body = yaml.safe_load((generated / "watches/watch-failed-units.yml").read_text(encoding="utf-8"))
                self.assertEqual(body["check"]["type"], "failed_units")
                # Explicit backend: `auto` would re-detect the init system every cycle.
                self.assertEqual(body["check"]["backend"], init)
                self.assertEqual(body["check"]["count"], {"op": ">", "value": 0})
                self.assertNotIn("then", body)
                self.assertNotIn(
                    {"kind": "failed_units", "reason": "no supported init backend detected"},
                    report["skipped_watches"],
                )

    def test_unknown_init_is_recorded_instead(self):
        generated, report = self.generate("unknown")

        self.assertFalse((generated / "watches/watch-failed-units.yml").exists())
        self.assertIn(
            {"kind": "failed_units", "reason": "no supported init backend detected"},
            report["skipped_watches"],
        )


class InotifyWatchGenerationTest(unittest.TestCase):
    """The per-user inotify limits are the exhaustion watch-fds cannot see: on
    bk1 uid 0 held all 1024 instances while watch-fds reported 0.0%, because
    fs.file-max is effectively unlimited there."""

    def generate(self, features: str):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("", encoding="utf-8")
        (stage / "features").write_text(features, encoding="utf-8")
        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        return root / "configs/host/root/etc/sermo", report

    def test_generated_when_the_sysctls_are_exposed(self):
        generated, report = self.generate("inotify=1\n")

        body = yaml.safe_load((generated / "watches/watch-inotify.yml").read_text(encoding="utf-8"))
        self.assertEqual(body["check"]["type"], "inotify")
        # One predicate on the worse of both limits: two prefixed predicates in a
        # single check are ANDed and would have stayed silent on bk1.
        self.assertEqual(body["check"]["used_pct"], {"op": ">=", "value": "80%"})
        self.assertEqual(body["interval"], "1m")
        self.assertEqual(body["for"], {"cycles": 3})
        self.assertNotIn("then", body)
        self.assertNotIn(
            {"kind": "inotify", "reason": "inotify sysctls not exposed"},
            report["skipped_watches"],
        )

    def test_skipped_without_the_sysctls(self):
        generated, report = self.generate("inotify=0\n")

        self.assertFalse((generated / "watches/watch-inotify.yml").exists())
        self.assertIn(
            {"kind": "inotify", "reason": "inotify sysctls not exposed"},
            report["skipped_watches"],
        )

    def test_collectors_probe_the_inotify_sysctls(self):
        root = Path(__file__).resolve().parent
        for script in ("remote_collect_inventory.sh", "remote_stage.sh"):
            body = (root / script).read_text(encoding="utf-8")
            self.assertIn("/proc/sys/fs/inotify/max_user_instances", body, script)
            self.assertIn('echo "inotify=1"', body, script)


class DockerStoppedContainerTest(unittest.TestCase):
    """A container Docker was asked to keep running and that exited non-zero is
    an outage, like a failed init unit. Observed on k2keu2: coreai-api-prod had
    been down three days with exit 137 and regenerating the host dropped it from
    the configuration, so nothing reported it any more."""

    def parse(self, containers: list[dict], stopped: str):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        stage = Path(temp.name)
        (stage / "docker_containers.json").write_text(json.dumps(containers), encoding="utf-8")
        (stage / "docker_stopped.tsv").write_text(stopped, encoding="utf-8")
        return generator.parse_docker_containers(stage)

    def test_running_container_needs_no_evidence(self):
        containers, skipped = self.parse([{"Names": ["/api"], "State": "running"}], "")
        self.assertEqual([c["container"] for c in containers], ["api"])
        self.assertEqual(skipped, [])

    def test_keep_alive_policy_with_failing_exit_is_generated(self):
        containers, skipped = self.parse(
            [{"Names": ["/coreai-api-prod"], "State": "exited", "Status": "Exited (137) 3 days ago"}],
            "/coreai-api-prod\texited\t137\ton-failure\n",
        )
        self.assertEqual([c["container"] for c in containers], ["coreai-api-prod"])
        self.assertEqual(containers[0]["source"], "restart policy on-failure and exit code 137")
        self.assertEqual(skipped, [])

    def test_one_off_container_that_failed_is_not_a_service(self):
        # `docker run` leftover: exit 127, restart policy no.
        containers, skipped = self.parse(
            [{"Names": ["/hungry_hoover"], "State": "exited"}],
            "/hungry_hoover\texited\t127\tno\n",
        )
        self.assertEqual(containers, [])
        self.assertEqual(skipped[0]["reason"], "container is not running (restart policy no)")

    def test_clean_exit_is_the_operators_intent(self):
        containers, skipped = self.parse(
            [{"Names": ["/blog-wordpress-1"], "State": "exited"}],
            "/blog-wordpress-1\texited\t0\tunless-stopped\n",
        )
        self.assertEqual(containers, [])
        self.assertEqual(skipped[0]["reason"], "container exited 0 (restart policy unless-stopped)")

    def test_missing_evidence_leaves_the_container_out(self):
        # A host staged before this fact was collected reports nothing, and the
        # exit code alone cannot tell a service outage from a one-off failure.
        containers, skipped = self.parse([{"Names": ["/api"], "State": "exited"}], "")
        self.assertEqual(containers, [])
        self.assertEqual(skipped[0]["reason"], "container is not running")

    def test_collectors_capture_stopped_container_evidence(self):
        root = Path(__file__).resolve().parent
        for script in ("remote_collect_inventory.sh", "remote_stage.sh", "collect_runtime_targets.sh"):
            body = (root / script).read_text(encoding="utf-8")
            self.assertIn("docker_stopped.tsv", body, script)
            self.assertIn("{{.HostConfig.RestartPolicy.Name}}", body, script)


class GlusterClusterGenerationTest(unittest.TestCase):
    def stage(self):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("glusterd.service\n", encoding="utf-8")
        (stage / "services_json.out").write_text(
            json.dumps({"services": [{"name": "glusterd", "installed": True, "ok": True, "status": "ok"}]}),
            encoding="utf-8",
        )
        return root, stage

    @staticmethod
    def write_xml(stage: Path, name: str, body: str):
        (stage / f"{name}.rc").write_text("0\n", encoding="utf-8")
        (stage / f"{name}.out").write_text(f"<cliOutput><opRet>0</opRet>{body}</cliOutput>", encoding="utf-8")

    def write_healthy_evidence(self, stage: Path):
        self.write_xml(
            stage,
            "gluster_peer_status",
            "<peerStatus><peer><hostname>zeus</hostname></peer><peer><hostname>apolo</hostname></peer></peerStatus>",
        )
        self.write_xml(
            stage,
            "gluster_volume_info",
            """<volInfo><volumes>
<volume><name>images</name><brickCount>3</brickCount><options><option><name>cluster.self-heal-daemon</name><value>on</value></option></options></volume>
<volume><name>images_raid5</name><brickCount>3</brickCount></volume>
</volumes></volInfo>""",
        )
        self.write_xml(
            stage,
            "gluster_volume_status",
            "<volStatus><volumes><volume><volName>images</volName></volume><volume><volName>images_raid5</volName><node><hostname>Self-heal Daemon</hostname></node></volume></volumes></volStatus>",
        )

    def test_generates_topology_check_from_successful_xml_evidence(self):
        root, stage = self.stage()
        self.write_healthy_evidence(stage)

        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        body = yaml.safe_load((root / "configs/host/root/etc/sermo/services/glusterd.yml").read_text(encoding="utf-8"))
        check = body["watches"]["cluster"]

        self.assertEqual(check["interval"], generator.GLUSTER_CLUSTER_INTERVAL)
        # Optional: a transient heal is not an outage, so a breach is a warning
        # rather than an unavailable data service. The limits stay strict at 0.
        self.assertTrue(check["check"]["optional"])
        self.assertEqual(check["check"]["type"], "gluster_cluster")
        self.assertEqual(check["check"]["peers"], ["apolo", "zeus"])
        self.assertEqual(check["check"]["volumes"]["images"]["bricks"], 3)
        self.assertTrue(check["check"]["volumes"]["images"]["self_heal"])
        self.assertTrue(check["check"]["volumes"]["images_raid5"]["self_heal"])
        self.assertEqual(check["check"]["volumes"]["images"]["max_heal_entries"], 0)
        self.assertEqual(check["check"]["volumes"]["images_raid5"]["max_split_brain_entries"], 0)
        entry = report["services"]["enabled"][0]["gluster_cluster"]
        self.assertTrue(entry["active"])
        self.assertEqual(entry["source"], "gluster CLI XML inventory")

    def test_omits_heal_limits_for_volumes_without_self_heal(self):
        """A distribute volume has no heal to report: `gluster volume heal` fails
        on it, and that failure would leave the whole check Unavailable."""
        root, stage = self.stage()
        self.write_xml(stage, "gluster_peer_status", "<peerStatus><peer><hostname>zeus</hostname></peer></peerStatus>")
        self.write_xml(
            stage,
            "gluster_volume_info",
            """<volInfo><volumes>
<volume><name>scratch</name><brickCount>2</brickCount></volume>
</volumes></volInfo>""",
        )
        self.write_xml(
            stage,
            "gluster_volume_status",
            "<volStatus><volumes><volume><volName>scratch</volName></volume></volumes></volStatus>",
        )

        generator.generate_for_host("host", stage, root / "configs", default_options())
        body = yaml.safe_load((root / "configs/host/root/etc/sermo/services/glusterd.yml").read_text(encoding="utf-8"))
        scratch = body["watches"]["cluster"]["check"]["volumes"]["scratch"]

        self.assertEqual(scratch["bricks"], 2)
        self.assertNotIn("self_heal", scratch)
        self.assertNotIn("max_heal_entries", scratch)
        self.assertNotIn("max_split_brain_entries", scratch)

    def test_keeps_only_local_liveness_when_xml_evidence_is_missing(self):
        root, stage = self.stage()

        report = generator.generate_for_host("host", stage, root / "configs", default_options())
        body = yaml.safe_load((root / "configs/host/root/etc/sermo/services/glusterd.yml").read_text(encoding="utf-8"))

        self.assertNotIn("cluster", body.get("watches", {}))
        entry = report["services"]["enabled"][0]["gluster_cluster"]
        self.assertFalse(entry["active"])
        self.assertIn("not collected", entry["reason"])

    def test_rejects_gluster_xml_with_entity_declarations(self):
        root, stage = self.stage()
        (stage / "gluster_peer_status.rc").write_text("0\n", encoding="utf-8")
        (stage / "gluster_peer_status.out").write_text(
            '<!DOCTYPE cliOutput [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>'
            "<cliOutput><opRet>0</opRet><peerStatus>&xxe;</peerStatus></cliOutput>",
            encoding="utf-8",
        )

        report = generator.generate_for_host("host", stage, root / "configs", default_options())

        entry = report["services"]["enabled"][0]["gluster_cluster"]
        self.assertFalse(entry["active"])
        self.assertIn("unsafe XML declarations", entry["reason"])

    def test_collectors_capture_gluster_xml(self):
        root = Path(__file__).resolve().parent
        for script in ("remote_collect_inventory.sh", "remote_stage.sh"):
            body = (root / script).read_text(encoding="utf-8")
            self.assertIn("gluster --mode=script --xml peer status", body, script)
            self.assertIn("gluster --mode=script --xml volume info", body, script)
            self.assertIn("gluster --mode=script --xml volume status", body, script)


class GlusterThinArbiterTest(unittest.TestCase):
    """The thin arbiter of a replica 2 volume is neither a brick nor a peer, so a
    volume whose arbiter was never started reports a perfectly healthy topology.
    Observed on k2kca2: the volume declared it, the unit was disabled and dead,
    and the clients logged `Failed to lookup/create thin-arbiter id file` every
    few hours while Sermo reported glusterd ok on all three nodes."""

    def stage(self, arbiters: str, addresses: str = "2: vrack6    inet 172.31.27.5/24 scope global vrack6\n"):
        temp = tempfile.TemporaryDirectory()
        self.addCleanup(temp.cleanup)
        root = Path(temp.name)
        stage = root / "stage" / "host" / "out"
        stage.mkdir(parents=True)
        (stage / "init").write_text("systemd\n", encoding="utf-8")
        (stage / "active_units").write_text("glusterd.service\n", encoding="utf-8")
        (stage / "services_json.out").write_text(
            json.dumps({"services": [
                {"name": "glusterd", "installed": True, "ok": True, "status": "ok"},
                {"name": "gluster-ta-volume", "installed": True, "ok": True, "status": "ok"},
            ]}),
            encoding="utf-8",
        )
        (stage / "ip_addr4").write_text(addresses, encoding="utf-8")
        (stage / "gluster_thin_arbiters").write_text(arbiters, encoding="utf-8")
        return root, stage

    def test_local_arbiter_generates_the_daemon_even_when_stopped(self):
        root, stage = self.stage("images\tk2kca2.vrack6\t/srv/gluster-images\t172.31.27.5\n")

        report = generator.generate_for_host("host", stage, root / "configs", default_options())

        body = yaml.safe_load(
            (root / "configs/host/root/etc/sermo/services/gluster-ta-volume.yml").read_text(encoding="utf-8")
        )
        self.assertEqual(body["uses"], generator.GLUSTER_TA_CATALOG_SERVICE)
        self.assertEqual(
            report["thin_arbiters"],
            [{
                "volume": "images",
                "host": "k2kca2.vrack6",
                "path": "/srv/gluster-images",
                "address": "172.31.27.5",
                "local": True,
                "source": "Gluster thin-arbiter declaration for this host",
            }],
        )

    def test_point_to_point_address_still_identifies_this_host(self):
        """`ip -o -4 addr` prints a VPN tun device as `inet <local> peer <remote>/32`,
        with no prefix glued to the host's own address. Reading those addresses with
        a CIDR parser skips the line outright, which classified an arbiter named by
        its tunnel address as remote and generated no daemon for it."""
        root, stage = self.stage(
            "images\tk2kca2.vrack6\t/srv/gluster-images\t172.31.25.123\n",
            addresses="38: tun1    inet 172.31.25.123 peer 172.31.25.124/32 scope global tun1\n",
        )

        report = generator.generate_for_host("host", stage, root / "configs", default_options())

        self.assertTrue((root / "configs/host/root/etc/sermo/services/gluster-ta-volume.yml").exists())
        self.assertTrue(report["thin_arbiters"][0]["local"])

    def test_remote_arbiter_is_recorded_but_not_generated(self):
        root, stage = self.stage("images\tk2kca2.vrack6\t/srv/gluster-images\t172.31.27.9\n")

        report = generator.generate_for_host("host", stage, root / "configs", default_options())

        self.assertFalse((root / "configs/host/root/etc/sermo/services/gluster-ta-volume.yml").exists())
        self.assertFalse(report["thin_arbiters"][0]["local"])
        self.assertEqual(report["thin_arbiters"][0]["reason"], "thin arbiter runs on k2kca2.vrack6")

    def test_unresolved_arbiter_host_is_reported_as_such(self):
        # Absence of proof is not proof: a host whose arbiter name does not resolve
        # is recorded, not silently treated as the arbiter itself.
        root, stage = self.stage("images\tk2kca2.vrack6\t/srv/gluster-images\t\n")

        report = generator.generate_for_host("host", stage, root / "configs", default_options())

        self.assertFalse((root / "configs/host/root/etc/sermo/services/gluster-ta-volume.yml").exists())
        self.assertFalse(report["thin_arbiters"][0]["local"])
        self.assertEqual(
            report["thin_arbiters"][0]["reason"],
            "thin arbiter host k2kca2.vrack6 did not resolve on the target",
        )

    def test_volume_without_a_thin_arbiter_requires_nothing(self):
        root, stage = self.stage("")

        report = generator.generate_for_host("host", stage, root / "configs", default_options())

        self.assertEqual(generator.thin_arbiter_report(stage), (False, []))
        self.assertEqual(report["thin_arbiters"], [])
        self.assertFalse((root / "configs/host/root/etc/sermo/services/gluster-ta-volume.yml").exists())

    def test_collectors_capture_the_thin_arbiter_declaration(self):
        root = Path(__file__).resolve().parent
        for script in ("remote_collect_inventory.sh", "remote_stage.sh"):
            body = (root / script).read_text(encoding="utf-8")
            self.assertIn("gluster --mode=script volume info", body, script)
            self.assertIn("Thin-arbiter-path", body, script)
            self.assertIn("gluster_thin_arbiters", body, script)


if __name__ == "__main__":
    unittest.main()
