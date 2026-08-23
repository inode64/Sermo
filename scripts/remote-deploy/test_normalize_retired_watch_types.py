#!/usr/bin/env python3
"""Behavior tests for retired host-watch normalization."""

from __future__ import annotations

import subprocess
import tarfile
import tempfile
import unittest
from pathlib import Path

SCRIPT = Path(__file__).with_name("remote_normalize_retired_watch_types.sh")


class RetiredWatchNormalizationTest(unittest.TestCase):
    """Exercise the production deletion helper against a temporary /etc tree."""

    def test_retired_base_removes_partial_local_sibling(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_name:
            root = Path(temporary_name)
            config_root = root / "etc" / "sermo"
            base = config_root / "watches" / "entropy.yml"
            override = config_root / "watches.local" / "entropy.yml"
            keep = config_root / "watches.local" / "keep.yml"
            base.parent.mkdir(parents=True)
            override.parent.mkdir(parents=True)
            base.write_text("name: entropy\ncheck:\n  type: entropy\n", encoding="utf-8")
            override.write_text("check:\n  optional: true\n", encoding="utf-8")
            keep.write_text("name: keep\ncheck:\n  type: file_exists\n  path: /tmp\n", encoding="utf-8")

            backup, removed = self.run_helper(root, config_root)

            self.assertFalse(base.exists())
            self.assertFalse(override.exists())
            self.assertTrue(keep.exists())
            self.assertEqual(set(removed.read_text(encoding="utf-8").splitlines()), {str(base), str(override)})
            with tarfile.open(backup) as archive:
                self.assertIn("etc/sermo/watches/entropy.yml", archive.getnames())
                self.assertIn("etc/sermo/watches.local/entropy.yml", archive.getnames())

    def test_orphan_retired_local_document_is_removed(self) -> None:
        with tempfile.TemporaryDirectory() as temporary_name:
            root = Path(temporary_name)
            config_root = root / "etc" / "sermo"
            retired = config_root / "networks.local" / "legacy.yaml"
            retired.parent.mkdir(parents=True)
            retired.write_text("name: legacy\ncheck:\n  type: autofs\n", encoding="utf-8")

            _, removed = self.run_helper(root, config_root)

            self.assertFalse(retired.exists())
            self.assertEqual(removed.read_text(encoding="utf-8").splitlines(), [str(retired)])

    def run_helper(self, root: Path, config_root: Path) -> tuple[Path, Path]:
        backup = root / "backup.tgz"
        removed = root / "removed_files"
        subprocess.run(
            [
                "bash",
                "-c",
                'source "$1"; remove_retired_watch_files "$2" "$3" "$4" etc/sermo "$5"',
                "test-normalize-retired-watch-types",
                str(SCRIPT),
                str(config_root),
                str(backup),
                str(root),
                str(removed),
            ],
            check=True,
        )
        return backup, removed


if __name__ == "__main__":
    unittest.main()
