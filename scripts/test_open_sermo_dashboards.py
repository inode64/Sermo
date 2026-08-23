#!/usr/bin/env python3
"""Tests for the local authenticated Chrome launcher."""

from __future__ import annotations

import importlib.util
import json
import sys
import tempfile
import unittest
from pathlib import Path


def browser_module():
    """Load the launcher script as a module under test."""
    path = Path(__file__).with_name("open_sermo_dashboards.py")
    spec = importlib.util.spec_from_file_location("open_sermo_dashboards", path)
    if spec is None or spec.loader is None:
        raise RuntimeError("cannot load open_sermo_dashboards.py")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


browser = browser_module()


class DashboardBrowserTest(unittest.TestCase):
    """Keep credentials out of URLs and Chrome command arguments."""

    def test_extension_is_limited_to_dashboard_origins(self):
        hosts = [browser.credentials.Host(client="inode64", ip="172.31.16.1")]
        urls = browser.dashboard_urls(hosts, 9797)
        with tempfile.TemporaryDirectory() as temporary_name:
            extension = browser.write_extension(Path(temporary_name), urls, "test-password")
            manifest = json.loads((extension / "manifest.json").read_text(encoding="utf-8"))
            background = (extension / "background.js").read_text(encoding="utf-8")

        self.assertEqual(manifest["host_permissions"], ["http://172.31.16.1:9797/*"])
        self.assertIn('"password": "test-password"', background)
        self.assertIn("webRequestAuthProvider", manifest["permissions"])

    def test_chrome_command_has_no_password_or_basic_auth_url(self):
        command = browser.chrome_command(
            "/usr/bin/google-chrome-stable",
            Path("test-profile"),
            Path("test-extension"),
            ["http://172.31.16.1:9797/"],
        )

        self.assertIn("--user-data-dir=test-profile", command)
        self.assertIn("--disable-extensions-except=test-extension", command)
        self.assertIn("--load-extension=test-extension", command)
        self.assertNotIn("test-password", command)
        self.assertNotIn("@", command[-1])
