#!/usr/bin/env python3
"""Regression tests for candidate-catalog fleet update payloads."""

from __future__ import annotations

import os
import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

SCRIPT_DIR = Path(__file__).resolve().parent
PREPARE = SCRIPT_DIR / "prepare_payload.sh"
REMOTE_UPDATE = SCRIPT_DIR / "remote_update_payload.sh"
REMOTE_STAGE = SCRIPT_DIR / "remote_stage.sh"
UPDATE_FLEET = SCRIPT_DIR / "update_fleet.sh"


class PreparePayloadTest(unittest.TestCase):
    def setUp(self) -> None:
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.run_root = self.root / "run"
        files = {
            "bin/sermoctl": "installed-cli\n",
            "bin/sermod": "daemon\n",
            "catalog/services/example.yml": "name: example\n",
            "templates/default-alert.yml": "notifiers: {}\n",
            "packaging/systemd/sermod.service": "[Service]\n",
            "packaging/systemd/sermo.conf": "d /run/sermo 0755 root root -\n",
            "packaging/openrc/sermod": "#!/bin/sh\n",
        }
        for name, content in files.items():
            path = self.repo / name
            path.parent.mkdir(parents=True, exist_ok=True)
            path.write_text(content, encoding="utf-8")
            if name in {"bin/sermoctl", "bin/sermod", "packaging/openrc/sermod"}:
                path.chmod(0o755)

    def prepare(self, candidate: Path | None = None) -> Path:
        args = [str(PREPARE), str(self.run_root), str(self.repo)]
        if candidate is not None:
            args.append(str(candidate))
        subprocess.run(args, check=True, capture_output=True, text=True)
        return self.run_root / "sermo-install-payload.tgz"

    def test_candidate_validator_is_staged_but_not_an_install_path(self) -> None:
        candidate = self.root / "sermoctl-candidate"
        candidate.write_text("candidate-cli\n", encoding="utf-8")
        candidate.chmod(0o755)

        payload = self.prepare(candidate)

        with tarfile.open(payload, "r:gz") as archive:
            names = set(archive.getnames())
            self.assertIn("candidate/sermoctl", names)
            self.assertIn("usr/bin/sermoctl", names)
            self.assertNotIn("usr/bin/sermoctl-candidate", names)
            member = archive.getmember("candidate/sermoctl")
            self.assertTrue(member.mode & 0o111)
            staged = archive.extractfile(member)
            self.assertIsNotNone(staged)
            self.assertEqual(staged.read(), b"candidate-cli\n")

    def test_install_payload_does_not_require_a_candidate(self) -> None:
        payload = self.prepare()

        with tarfile.open(payload, "r:gz") as archive:
            self.assertNotIn("candidate/sermoctl", archive.getnames())

    def test_remote_update_validates_with_staged_candidate(self) -> None:
        remote_update = REMOTE_UPDATE.read_text(encoding="utf-8")
        self.assertIn('"${stage}/candidate/sermoctl" --config', remote_update)
        self.assertNotIn('"${stage}/usr/bin/sermoctl" --config', remote_update)

        update_fleet = UPDATE_FLEET.read_text(encoding="utf-8")
        # The managed remote staging root is intentionally under /tmp.
        expected = '/tmp/sermo-update-${run_id}/stage/usr/share/sermo'  # noqa: S108
        self.assertIn(expected, update_fleet)
        self.assertIn("make build-candidate-sermoctl", update_fleet)

    def test_reinstall_preserves_web_credentials_from_backup(self) -> None:
        remote_stage = REMOTE_STAGE.read_text(encoding="utf-8")
        self.assertIn('"${backup}/credentials.env"', remote_stage)
        self.assertIn("/etc/sermo/credentials.env", remote_stage)
        self.assertIn("-m 0600", remote_stage)


if __name__ == "__main__":
    os.environ.setdefault("TZ", "UTC")
    unittest.main()
