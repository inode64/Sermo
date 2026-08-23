#!/usr/bin/env python3
"""Tests for inventory and credential-selection helpers."""

from __future__ import annotations

import importlib.util
import subprocess
import sys
import tempfile
import unittest
from pathlib import Path


def deploy_module():
    """Load the deployment script as a module under test."""
    path = Path(__file__).with_name("deploy_credentials.py")
    spec = importlib.util.spec_from_file_location("deploy_credentials", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load deploy_credentials.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


deploy = deploy_module()


class CredentialDeploymentTest(unittest.TestCase):
    """Keep mapping rules independent from remote SSH execution."""

    def write(self, body: str) -> Path:
        """Create a disposable fixture file."""
        with tempfile.NamedTemporaryFile("w", delete=False, encoding="utf-8") as temporary:
            temporary.write(body)
            path = Path(temporary.name)
        self.addCleanup(path.unlink, missing_ok=True)
        return path

    def test_inventory_normalizes_punctuation_and_deduplicates_same_host(self):
        inventory = self.write(
            "nombre,cliente,ip_vpn\n"
            "one,amizalsa,'172.31.16.31\n"
            "one-copy,amizalsa,172.31.16.31,\n"
            "two,euromeca,172.31.16.55\n",
        )

        self.assertEqual(
            deploy.parse_inventory(inventory),
            [deploy.Host(client="amizalsa", ip="172.31.16.31"), deploy.Host(client="euromeca", ip="172.31.16.55")],
        )

    def test_inventory_accepts_latin1_headers_not_used_for_mapping(self):
        with tempfile.NamedTemporaryFile("wb", delete=False) as temporary:
            temporary.write(b"nombre,cliente,compilaci\xf3n,ip_vpn\none,amizalsa,,172.31.16.31\n")
            inventory = Path(temporary.name)
        self.addCleanup(inventory.unlink, missing_ok=True)

        self.assertEqual(deploy.parse_inventory(inventory), [deploy.Host(client="amizalsa", ip="172.31.16.31")])

    def test_special_clients_get_optiza_and_inode64(self):
        self.assertEqual(deploy.credential_clients("bertolin"), ("bertolin", "inode64", "optiza"))
        self.assertEqual(deploy.credential_clients("inode64"), ("inode64",))
        self.assertEqual(deploy.credential_clients("promopublic"), ("promopublic", "inode64"))

    def test_password_source_requires_each_required_client(self):
        passwords = self.write("amizalsa one\ninode64 two\n")
        parsed = deploy.parse_passwords(passwords)

        with self.assertRaisesRegex(ValueError, "optiza"):
            deploy.validate_credentials([deploy.Host(client="amizalsa", ip="172.31.16.31")], parsed)

    def test_rejects_conflicting_client_for_one_ip(self):
        inventory = self.write("nombre,cliente,ip_vpn\none,amizalsa,172.31.16.31\ntwo,bertolin,172.31.16.31\n")

        with self.assertRaisesRegex(ValueError, "more than one client"):
            deploy.parse_inventory(inventory)

    def test_selects_only_requested_inventory_hosts(self):
        hosts = [
            deploy.Host(client="amizalsa", ip="172.31.16.31"),
            deploy.Host(client="euromeca", ip="172.31.16.55"),
        ]

        self.assertEqual(deploy.select_hosts(hosts, ["172.31.16.55"]), [hosts[1]])
        self.assertEqual(deploy.select_hosts(hosts, [], ["172.31.16.31"]), [hosts[1]])
        with self.assertRaisesRegex(ValueError, "absent from the inventory"):
            deploy.select_hosts(hosts, ["172.31.16.99"])

    def test_success_detail_is_one_report_line(self):
        process = subprocess.CompletedProcess(["test"], 0, stdout="OK\n * Starting sermod ... [ ok ]\n", stderr="")

        self.assertEqual(deploy.success_detail(process), "OK * Starting sermod ... [ ok ]")
