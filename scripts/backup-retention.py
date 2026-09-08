#!/usr/bin/env python3
"""Retain completed local snapshots; caller holds .backup.lock exclusively."""
import argparse
import json
from pathlib import Path
import re
import shutil


def prune(root: Path, keep: int):
    root = root.resolve(strict=True)
    if keep < 2:
        raise ValueError("Keep at least two completed backups")
    latest = Path((root / "latest").read_text().strip()).resolve(strict=True)
    if latest.parent != root:
        raise ValueError("Latest backup escapes the backup root")
    valid = []
    for entry in root.iterdir():
        if entry.is_symlink() or not entry.is_dir() or not re.fullmatch(r"qiyun-\d{8}-\d{6}-\d+", entry.name):
            continue
        try:
            manifest = json.loads((entry / "manifest.json").read_text())
            if manifest.get("formatVersion") != 2 or not (entry / "SHA256SUMS").is_file():
                continue
        except (OSError, ValueError):
            continue
        valid.append(entry)
    protected = set(sorted(valid, key=lambda p: p.name, reverse=True)[:keep]) | {latest}
    removed = []
    for entry in valid:
        if entry in protected:
            continue
        resolved = entry.resolve(strict=True)
        if resolved.parent != root or entry.is_symlink():
            raise ValueError("Retention target changed")
        shutil.rmtree(resolved)
        removed.append(entry.name)
    return removed


if __name__ == "__main__":
    parser = argparse.ArgumentParser()
    parser.add_argument("root", type=Path)
    parser.add_argument("--keep", type=int, default=7)
    args = parser.parse_args()
    print(json.dumps({"removed": prune(args.root, args.keep)}))
