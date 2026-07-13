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
        options = generator.GenerationOptions(
            web_port=9797,
            web_password="test",
            storage_free_pct="5%",
            expand_by="5G",
            smart_interval="24h",
            hdparm_interval="6h",
            users_watch=False,
            active_services_only=True,
            catalog_services_dir=Path(__file__).parents[2] / "catalog/services",
        )
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

    def test_lvm_watches_include_lv_display_name(self):
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
        options = generator.GenerationOptions(
            web_port=9797,
            web_password="test",
            storage_free_pct="5%",
            expand_by="5G",
            smart_interval="24h",
            hdparm_interval="6h",
            users_watch=False,
            active_services_only=True,
            catalog_services_dir=Path(__file__).parents[2] / "catalog/services",
        )
        report = generator.generate_for_host("host", stage, root / "configs", options)
        lv_body = (root / "configs/host/root/etc/sermo/watches/lvm-vg0-root.yml").read_text(encoding="utf-8")
        vg_body = (root / "configs/host/root/etc/sermo/watches/lvm-vg0-capacity.yml").read_text(encoding="utf-8")

        self.assertIn('display_name: "LVM vg0/root"', lv_body)
        self.assertIn('display_name: "LVM vg0 capacity"', vg_body)
        self.assertEqual(
            report["lvm_volumes"],
            [{"volume_group": "vg0", "logical_volume": "root", "display_name": "LVM vg0/root"}],
        )

if __name__ == "__main__":
    unittest.main()
