#!/usr/bin/env python3
"""Open every Sermo dashboard in an isolated Chrome session."""

from __future__ import annotations

import argparse
import importlib.util
import json
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

DEFAULT_INVENTORY = Path("inventario-red-172.31.16.csv")
DEFAULT_PASSWORDS = Path(".env.pass")
DEFAULT_PORT = 9797
CHROME_CANDIDATES = ("google-chrome-stable", "google-chrome", "chromium", "chromium-browser")


def credentials_module():
    """Load the shared inventory and password parsers without a package install."""
    path = Path(__file__).with_name("remote-deploy") / "deploy_credentials.py"
    spec = importlib.util.spec_from_file_location("sermo_deploy_credentials", path)
    if spec is None or spec.loader is None:
        raise RuntimeError(f"cannot load {path}")
    module = importlib.util.module_from_spec(spec)
    sys.modules[spec.name] = module
    spec.loader.exec_module(module)
    return module


credentials = credentials_module()


def chrome_binary(requested: str | None) -> str:
    """Return the requested Chrome binary or the first available local browser."""
    if requested:
        resolved = shutil.which(requested)
        if resolved is None:
            raise ValueError(f"Chrome executable not found: {requested}")
        return resolved
    for candidate in CHROME_CANDIDATES:
        resolved = shutil.which(candidate)
        if resolved:
            return resolved
    raise ValueError("Chrome or Chromium is not installed")


def dashboard_urls(hosts: list[object], port: int) -> list[str]:
    """Build the non-secret dashboard URLs for all inventory hosts."""
    if not 1 <= port <= 65535:
        raise ValueError("--port must be in 1..65535")
    return [f"http://{host.ip}:{port}/" for host in hosts]


def write_extension(root: Path, urls: list[str], password: str) -> Path:
    """Create a private MV3 extension that answers only Sermo Basic challenges."""
    extension = root / "extension"
    extension.mkdir(mode=0o700)
    manifest = {
        "manifest_version": 3,
        "name": "Sermo dashboard session",
        "version": "1.0.0",
        "permissions": ["webRequest", "webRequestAuthProvider"],
        "host_permissions": [f"{url}*" for url in urls],
        "background": {"service_worker": "background.js"},
    }
    credentials_json = json.dumps({"username": "inode64", "password": password})
    urls_json = json.dumps([f"{url}*" for url in urls])
    background = (
        f"const credentials = {credentials_json};\n"
        "chrome.webRequest.onAuthRequired.addListener(\n"
        "  (_details, callback) => callback({authCredentials: credentials}),\n"
        f"  {{urls: {urls_json}}},\n"
        "  ['asyncBlocking'],\n"
        ");\n"
    )
    manifest_path = extension / "manifest.json"
    background_path = extension / "background.js"
    manifest_path.write_text(json.dumps(manifest, indent=2) + "\n", encoding="utf-8")
    background_path.write_text(background, encoding="utf-8")
    manifest_path.chmod(0o600)
    background_path.chmod(0o600)
    return extension


def chrome_command(chrome: str, profile: Path, extension: Path, urls: list[str]) -> list[str]:
    """Build a Chrome command whose arguments never contain the password."""
    return [
        chrome,
        "--new-window",
        *urls,
    ]


def arguments() -> argparse.Namespace:
    """Parse local-only browser-launch options."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--inventory", type=Path, default=DEFAULT_INVENTORY)
    parser.add_argument("--passwords", type=Path, default=DEFAULT_PASSWORDS)
    parser.add_argument("--port", type=int, default=DEFAULT_PORT)
    parser.add_argument("--chrome", help="Chrome or Chromium executable")
    parser.add_argument("--keep-profile", action="store_true", help="retain the private profile after Chrome exits")
    parser.add_argument("--dry-run", action="store_true", help="print URLs without launching Chrome")
    return parser.parse_args()


def main() -> int:
    """Create an isolated authenticated Chrome session for every dashboard."""
    args = arguments()
    hosts = credentials.parse_inventory(args.inventory)
    passwords = credentials.parse_passwords(args.passwords)
    password = passwords.get("inode64")
    if not password:
        raise ValueError("password source lacks inode64")
    urls = dashboard_urls(hosts, args.port)
    if args.dry_run:
        print("\n".join(urls))
        return 0
    chrome = chrome_binary(args.chrome)
    root = Path(tempfile.mkdtemp(prefix="sermo-chrome-"))
    root.chmod(0o700)
    profile = root / "profile"
    profile.mkdir(mode=0o700)
    try:
        extension = write_extension(root, urls, password)
        command = chrome_command(chrome, profile, extension, urls)
        print(f"Opening {len(urls)} Sermo dashboards in an isolated Chrome profile.")
        print("Close that Chrome window to remove its temporary profile and authentication extension.")
        process = subprocess.Popen(command)
        return process.wait()
    finally:
        if args.keep_profile:
            print(f"Private Chrome profile retained at {root}")
        else:
            shutil.rmtree(root, ignore_errors=True)


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except (OSError, RuntimeError, ValueError) as error:
        print(f"open_sermo_dashboards: {error}", file=sys.stderr)
        raise SystemExit(64) from error
