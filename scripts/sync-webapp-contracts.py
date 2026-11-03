#!/usr/bin/env python3
"""Copy Desktop-owned WebApp contracts for embedding, or check for drift."""
import argparse
from pathlib import Path
import shutil

root = Path(__file__).resolve().parent.parent
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--desktop-root", type=Path, default=root.parent / "zenmind-desktop")
parser.add_argument("--check", action="store_true")
args = parser.parse_args()
for source, target in (
    ("contracts/webapp/webapp.schema.json", "internal/webapp/contract/webapp.schema.json"),
    ("contracts/webapp/fixtures/manifest-v2.json", "internal/webapp/testdata/manifest-v2.json"),
):
    source, target = args.desktop_root / source, root / target
    if args.check:
        if not target.exists() or source.read_bytes() != target.read_bytes():
            raise SystemExit(f"WebApp contract drift: {target.relative_to(root)}")
    else:
        target.parent.mkdir(parents=True, exist_ok=True)
        shutil.copyfile(source, target)
print("WebApp schema and shared fixture are synchronized")
