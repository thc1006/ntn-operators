#!/usr/bin/env python3
"""Validate ADR front matter and unique numbers.

Usage:
    python checks/check_adr_metadata.py docs/adr
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

ALLOWED_STATUS = {"proposed", "accepted", "deprecated", "superseded"}
REQUIRED = {
    "adr",
    "title",
    "status",
    "date",
    "last_verified",
    "deciders",
    "supersedes",
    "superseded_by",
    "implementation",
    "tracking",
}


def parse_front_matter(path: Path) -> dict[str, str]:
    text = path.read_text(encoding="utf-8")
    if not text.startswith("---\n"):
        raise ValueError("missing YAML front matter")
    end = text.find("\n---\n", 4)
    if end < 0:
        raise ValueError("unterminated YAML front matter")
    values: dict[str, str] = {}
    for raw in text[4:end].splitlines():
        if not raw or raw.startswith((" ", "\t", "#")):
            continue
        if ":" not in raw:
            raise ValueError(f"invalid front-matter line: {raw!r}")
        key, value = raw.split(":", 1)
        values[key.strip()] = value.strip()
    return values


def main() -> int:
    root = Path(sys.argv[1] if len(sys.argv) > 1 else "docs/adr")
    seen: dict[int, Path] = {}
    errors: list[str] = []

    for path in sorted(root.glob("[0-9][0-9][0-9][0-9]-*.md")):
        try:
            meta = parse_front_matter(path)
        except ValueError as exc:
            errors.append(f"{path}: {exc}")
            continue

        missing = REQUIRED - meta.keys()
        if missing:
            errors.append(f"{path}: missing keys {sorted(missing)}")
            continue

        try:
            number = int(meta["adr"])
        except ValueError:
            errors.append(f"{path}: adr must be an integer")
            continue

        filename_number = int(path.name[:4])
        if number != filename_number:
            errors.append(
                f"{path}: front-matter adr {number} != filename {filename_number}"
            )

        if number in seen:
            errors.append(f"{path}: duplicate ADR {number}; first seen at {seen[number]}")
        else:
            seen[number] = path

        status = meta["status"].strip("\"'").lower()
        if status not in ALLOWED_STATUS:
            errors.append(f"{path}: invalid status {status!r}")

        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", meta["date"]):
            errors.append(f"{path}: date must be YYYY-MM-DD")
        if not re.fullmatch(r"\d{4}-\d{2}-\d{2}", meta["last_verified"]):
            errors.append(f"{path}: last_verified must be YYYY-MM-DD")

    if errors:
        print("\n".join(errors), file=sys.stderr)
        return 1

    print(f"OK: {len(seen)} ADRs; all numbers unique and metadata valid")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
