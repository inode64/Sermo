#!/usr/bin/env python3
"""Regression tests for endpoint-gated remote service generation."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


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

if __name__ == "__main__":
    unittest.main()
