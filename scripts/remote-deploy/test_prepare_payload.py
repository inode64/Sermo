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
REMOTE_APPLY = SCRIPT_DIR / "remote_apply.sh"
REMOTE_FINAL_CHECK = SCRIPT_DIR / "remote_final_check.sh"
REMOTE_UPDATE_BINARY_CATALOG = SCRIPT_DIR / "remote_update_binary_catalog.sh"
REMOTE_STAGE = SCRIPT_DIR / "remote_stage.sh"
REMOTE_COLLECT = SCRIPT_DIR / "remote_collect_inventory.sh"
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

    def test_update_fleet_exposes_inactive_service_generation(self) -> None:
        """An explicit fleet run can include installed inactive service profiles."""
        update_fleet = UPDATE_FLEET.read_text(encoding="utf-8")

        self.assertIn("--include-inactive-installed-services", update_fleet)
        self.assertIn("inactive_services_flag=(--include-inactive-installed-services)", update_fleet)
        self.assertIn('"${inactive_services_flag[@]}"', update_fleet)

    def test_update_fleet_passes_an_explicit_service_selector(self) -> None:
        """A scoped lifecycle run must not generate unrelated catalog services."""
        update_fleet = UPDATE_FLEET.read_text(encoding="utf-8")

        self.assertIn("--only-services", update_fleet)
        self.assertIn('only_services_flag=(--only-services "$only_services")', update_fleet)

    def test_update_fleet_bounds_each_remote_transfer_and_command(self) -> None:
        """A blocked collector or transfer must not hold the fleet run forever."""
        update_fleet = UPDATE_FLEET.read_text(encoding="utf-8")

        self.assertIn(
            'remote_command_timeout_seconds="${SERMO_REMOTE_COMMAND_TIMEOUT_SECONDS:-1500}"',
            update_fleet,
        )
        self.assertIn(
            'timeout --foreground "${remote_command_timeout_seconds}s" ssh',
            update_fleet,
        )
        self.assertIn(
            'timeout --foreground "${remote_command_timeout_seconds}s" scp',
            update_fleet,
        )
        self.assertIn('ready_wait_seconds="${SERMO_READY_WAIT_SECONDS:-600}"', update_fleet)

    def test_update_fleet_uses_only_the_remote_credentials_file(self) -> None:
        """A normal update must not put a Web password in SSH command arguments."""
        update_fleet = UPDATE_FLEET.read_text(encoding="utf-8")

        self.assertIn('credentials_file="/etc/sermo/credentials.env"', update_fleet)
        self.assertIn('cred_flag=(--web-password-file "$credentials_file")', update_fleet)
        self.assertNotIn("env SERMO_WEB_PASSWORD=", update_fleet)

    def test_remote_web_verifiers_bound_each_http_request(self) -> None:
        """A hung local Web UI must not make a deploy or final audit hang."""
        for script in [REMOTE_APPLY, REMOTE_FINAL_CHECK, REMOTE_UPDATE_BINARY_CATALOG]:
            body = script.read_text(encoding="utf-8")
            self.assertIn('http_timeout_seconds="${SERMO_HTTP_TIMEOUT_SECONDS:-5}"', body)
            self.assertIn('--connect-timeout "$http_timeout_seconds"', body)
            self.assertIn('--max-time "$http_timeout_seconds"', body)

    def test_remote_apply_waits_for_readiness_and_valid_web_apis(self) -> None:
        """Config apply succeeds only after readiness and supported APIs pass."""
        body = REMOTE_APPLY.read_text(encoding="utf-8")

        self.assertIn('livez_waited_seconds', body)
        self.assertIn('readyz_waited_seconds', body)
        self.assertIn('if [ "$livez_rc" -ne 0 ] || [ "$readyz_rc" -ne 0 ]', body)
        self.assertIn("for api in services watches mounts; do", body)
        self.assertIn("/api/${api}", body)
        self.assertNotIn("/api/status", body)

    def test_exim_binary_magic_probe_does_not_use_a_nul_command_substitution(self) -> None:
        """Non-SQLite database headers must not emit Bash's ignored-NUL warning."""
        for script in [REMOTE_COLLECT, REMOTE_STAGE]:
            body = script.read_text(encoding="utf-8")
            self.assertIn('od -An -N 15 -tx1 "$hints_db"', body)
            self.assertIn("53514c69746520666f726d61742033", body)
            self.assertNotIn('magic="$(dd if="$hints_db" bs=1 count=15', body)

    def test_reinstall_preserves_web_credentials_from_backup(self) -> None:
        remote_stage = REMOTE_STAGE.read_text(encoding="utf-8")
        self.assertIn('"${backup}/credentials.env"', remote_stage)
        self.assertIn("/etc/sermo/credentials.env", remote_stage)
        self.assertIn("-m 0600", remote_stage)

    def test_apply_never_deletes_the_per_host_override_layer(self) -> None:
        """`.local` carries the operator's tuning; a regeneration must keep it."""
        remote_apply = REMOTE_APPLY.read_text(encoding="utf-8")
        wipe = next(
            line for line in remote_apply.splitlines()
            if line.startswith("rm -rf /etc/sermo/")
        )
        self.assertNotIn(".local", wipe)
        for name in ["services", "apps", "notifiers", "watches", "networks", "storages", "mounts"]:
            self.assertIn(f"/etc/sermo/{name}.local", remote_apply)

    def test_reinstall_restores_the_per_host_override_layer(self) -> None:
        """remote_stage moves the whole tree aside, so it must restore it."""
        remote_stage = REMOTE_STAGE.read_text(encoding="utf-8")
        self.assertIn("local_overrides_preserved", remote_stage)
        self.assertIn('src="${backup}/$(basename "$dir")"', remote_stage)
        # templates are operator content too, and the same `mv` destroyed them.
        self.assertIn("/etc/sermo/templates.local /etc/sermo/templates", remote_stage)


if __name__ == "__main__":
    os.environ.setdefault("TZ", "UTC")
    unittest.main()
