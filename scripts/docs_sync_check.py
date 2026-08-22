#!/usr/bin/env python3
"""Check the documentation against the code it describes.

Every check here was written to catch a drift that actually reached the
repository, and that no other tool in the gate can see: a document naming a
source file that was deleted, a skill naming a Go identifier that does not
exist, AGENTS.md advertising half the linters .golangci.yml enables, a
validated configuration key documented nowhere, and the two language versions
disagreeing on a number.
"""

from __future__ import annotations

import re
import subprocess
import sys
from difflib import SequenceMatcher
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]

# Documents and skills that describe the code.
DOC_GLOBS = ("docs/*.md", "*.md", ".agents/skills/*/SKILL.md", ".agents/skills/*/references/*.md")

# A source path cited in prose, e.g. `internal/web/server.go`.
# The lookbehind anchors the root: without it "tests/" matched inside any longer
# path, so `.semgrep/tests/sermo.go` was checked as the non-existent
# "tests/sermo.go" — a citation could be validated against the wrong file.
CITED_PATH = re.compile(
    r"(?<![A-Za-z0-9_./-])"
    r"((?:\.semgrep|internal|cmd|scripts|packaging|catalog|templates|examples|tests|docs)"
    r"/[A-Za-z0-9_./-]+\.(?:go|json|jsonl|js|yml|yaml|md|css|html|sh|py|toml))\b"
)
# A backticked CamelCase identifier, the form skills use for Go symbols.
CITED_IDENT = re.compile(r"`([A-Z][A-Za-z0-9]{4,})`")
# A number with an optional unit, compared position by position across languages.
NUMBER = re.compile(r"\b(\d+(?:\.\d+)?(?:ms|s|m|h|d|%|KiB|MiB|GiB|KB|MB|GB)?)\b")

# Config keys the daemon injects itself; an operator cannot write them, so they
# are deliberately absent from the documentation.
INTERNAL_CONFIG_KEYS = {"change_level"}

LANG_PAIRS = [
    ("docs/architecture.md", "docs/architecture.es.md"),
    ("docs/cli.md", "docs/cli.es.md"),
    ("docs/configuration.md", "docs/configuration.es.md"),
    ("docs/rules.md", "docs/rules.es.md"),
    ("docs/safety.md", "docs/safety.es.md"),
    ("docs/services.md", "docs/services.es.md"),
    ("docs/webui-representation.md", "docs/webui-representation.es.md"),
    ("AGENTS.md", "AGENTS.es.md"),
    ("README.md", "README.es.md"),
    ("TODO.md", "TODO.es.md"),
]


def docs() -> list[Path]:
    found: list[Path] = []
    for pattern in DOC_GLOBS:
        found.extend(sorted(ROOT.glob(pattern)))
    return found


def read(path: Path | str) -> str:
    return (ROOT / path).read_text(encoding="utf-8")


def tracked(pattern: str) -> list[str]:
    out = subprocess.run(
        ["git", "ls-files", pattern], cwd=ROOT, capture_output=True, text=True, check=True
    )
    # git ls-files keeps paths deleted in the working tree until the deletion is
    # committed. The validation gate runs before commits, so only inspect source
    # files that still exist in the checkout being validated.
    return [path for path in out.stdout.split() if (ROOT / path).is_file()]


def check_cited_paths() -> list[str]:
    """Every source path a document names must exist."""
    problems = []
    for doc in docs():
        for lineno, line in enumerate(read(doc).split("\n"), 1):
            for match in CITED_PATH.finditer(line):
                if not (ROOT / match.group(1)).exists():
                    rel = doc.relative_to(ROOT)
                    problems.append(f"{rel}:{lineno}: cites missing path {match.group(1)}")
    return problems


def check_skill_identifiers() -> list[str]:
    """Every Go identifier a skill names must exist; skills steer agents to reuse."""
    code = "".join(read(f) for f in tracked("*.go")) + read("Makefile")
    problems = []
    for pattern in (".agents/skills/*/SKILL.md", ".agents/skills/*/references/*.md"):
        for skill in sorted(ROOT.glob(pattern)):
            for lineno, line in enumerate(read(skill).split("\n"), 1):
                for match in CITED_IDENT.finditer(line):
                    ident = match.group(1)
                    if not re.search(rf"\b{re.escape(ident)}\b", code):
                        rel = skill.relative_to(ROOT)
                        problems.append(f"{rel}:{lineno}: names unknown identifier {ident}")
    return problems


def check_linter_rollcall() -> list[str]:
    """AGENTS.md must name every linter .golangci.yml enables."""
    config = read(".golangci.yml").split("  settings:")[0]
    enabled = set(re.findall(r"^    - ([a-z0-9_]+)", config, re.M))
    problems = []
    for doc, following in (
        ("AGENTS.md", "- **`make npm-audit`**"),
        ("AGENTS.es.md", "- **`make npm-audit`**"),
    ):
        text = read(doc)
        section = text[text.index("- **`golangci-lint`**") : text.index(following)]
        named = set(re.findall(r"`([a-z0-9_]+)`", section))
        problems.extend(
            f"{doc}: does not name enabled linter {missing}"
            for missing in sorted(enabled - named)
        )
    return problems


def check_config_keys() -> list[str]:
    """Every operator-writable config key must appear in docs/."""
    key_decl = re.compile(
        r"(?:CheckKey|WatchKey|SectionKey|PolicyKey|StopPolicyKey|RuleField)"
        r"[A-Za-z0-9]+\s*=\s*\"([a-z0-9_]+)\""
    )
    keys: set[str] = set()
    for path in tracked("internal/*/[a-z]*.go"):
        if path.endswith("_test.go"):
            continue
        keys.update(key_decl.findall(read(path)))
    documented = "".join(read(f) for f in sorted(ROOT.glob("docs/*.md")))
    return [
        f"docs/: configuration key {key} is validated but undocumented"
        for key in sorted(keys - INTERNAL_CONFIG_KEYS)
        if not re.search(rf"\b{re.escape(key)}\b", documented)
    ]


def check_language_parity() -> list[str]:
    """The two language versions must agree on headings and on every number."""
    problems = []
    for en, es in LANG_PAIRS:
        english, spanish = read(en), read(es)
        heads_en = len(re.findall(r"^#", english, re.M))
        heads_es = len(re.findall(r"^#", spanish, re.M))
        if heads_en != heads_es:
            problems.append(f"{en}/{es}: {heads_en} vs {heads_es} headings")
        # A unit written apart ("360 s") is typography, not a different value.
        nums_en = [n.rstrip("smhd% ") for n in NUMBER.findall(english)]
        nums_es = [n.rstrip("smhd% ") for n in NUMBER.findall(spanish)]
        # Report where the sequences diverge, not which values are absent: the
        # same number usually appears in both files, just not in the same place.
        for tag, i1, i2, j1, j2 in SequenceMatcher(None, nums_en, nums_es).get_opcodes():
            if tag == "equal":
                continue
            problems.append(
                f"{en}/{es}: {tag} at position {i1} — EN has {nums_en[i1:i2][:4]}, "
                f"ES has {nums_es[j1:j2][:4]}"
            )
    return problems


CHECKS = (
    ("cited paths exist", check_cited_paths),
    ("skill identifiers exist", check_skill_identifiers),
    ("linter roll-call is complete", check_linter_rollcall),
    ("configuration keys are documented", check_config_keys),
    ("language versions agree", check_language_parity),
)


def main() -> int:
    failed = 0
    for label, check in CHECKS:
        problems = check()
        status = "ok" if not problems else f"{len(problems)} problem(s)"
        print(f"  {label}: {status}")
        for problem in problems:
            print(f"    {problem}")
        failed += len(problems)
    if failed:
        print(f"docs-sync: {failed} problem(s)", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
