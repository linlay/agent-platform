#!/usr/bin/env python3
"""统计项目代码行数：源代码 vs 注释 vs 空白"""

import os
import sys
import re

ROOT = "/Users/linlay/Project/zenmind/agent-platform"

EXCLUDE_DIRS = {".git", ".zenmind", "vendor", "node_modules", "target", "__pycache__"}

def count_go(filepath):
    """统计 Go 文件的代码行、注释行、空白行"""
    with open(filepath, "r", encoding="utf-8", errors="replace") as f:
        lines = f.readlines()

    code, comment, blank = 0, 0, 0
    in_block = False

    for line in lines:
        stripped = line.strip()
        if stripped == "":
            blank += 1
            continue

        if in_block:
            comment += 1
            if "*/" in stripped:
                in_block = False
            continue

        # 检查块注释开始
        if stripped.startswith("/*") or stripped.startswith("//*"):
            comment += 1
            if "*/" not in stripped:
                in_block = True
            continue

        if stripped.startswith("//"):
            comment += 1
            continue

        # 检查行内 /* */
        if "/*" in stripped:
            comment += 1
            if "*/" not in stripped:
                in_block = True
            continue

        code += 1

    return code, comment, blank

def count_rust(filepath):
    """统计 Rust 文件的代码行、注释行、空白行（同 Go）"""
    return count_go(filepath)

def count_yaml(filepath):
    """统计 YAML 文件的代码行、注释行、空白行"""
    with open(filepath, "r", encoding="utf-8", errors="replace") as f:
        lines = f.readlines()

    code, comment, blank = 0, 0, 0

    for line in lines:
        stripped = line.strip()
        if stripped == "":
            blank += 1
        elif stripped.startswith("#"):
            comment += 1
        else:
            code += 1

    return code, comment, blank

def count_markdown(filepath):
    """统计 Markdown（全算文档/注释，不算代码）"""
    with open(filepath, "r", encoding="utf-8", errors="replace") as f:
        lines = f.readlines()

    code, comment, blank = 0, 0, 0

    for line in lines:
        stripped = line.strip()
        if stripped == "":
            blank += 1
        else:
            comment += 1  # Markdown 全部算文档

    return code, comment, blank

def main():
    counters = {"go": count_go, "rs": count_rust, "yml": count_yaml, "yaml": count_yaml, "md": count_markdown}

    totals = {"code": 0, "comment": 0, "blank": 0, "files": 0}
    by_ext = {}

    for root, dirs, files in os.walk(ROOT):
        dirs[:] = [d for d in dirs if d not in EXCLUDE_DIRS and not d.startswith(".")]
        for fname in files:
            ext = fname.rsplit(".", 1)[-1] if "." in fname else ""
            if ext not in counters:
                continue
            filepath = os.path.join(root, fname)
            try:
                code, comment, blank = counters[ext](filepath)
            except Exception:
                continue
            totals["code"] += code
            totals["comment"] += comment
            totals["blank"] += blank
            totals["files"] += 1

            if ext not in by_ext:
                by_ext[ext] = {"code": 0, "comment": 0, "blank": 0, "files": 0}
            by_ext[ext]["code"] += code
            by_ext[ext]["comment"] += comment
            by_ext[ext]["blank"] += blank
            by_ext[ext]["files"] += 1

    total_all = totals["code"] + totals["comment"] + totals["blank"]

    print("=" * 60)
    print("项目代码统计")
    print("=" * 60)
    print(f"{'类型':<10} {'文件数':>6} {'代码行':>8} {'注释行':>8} {'空白行':>8} {'总行数':>8}")
    print("-" * 60)

    for ext in sorted(by_ext.keys()):
        s = by_ext[ext]
        t = s["code"] + s["comment"] + s["blank"]
        print(f"{ext:<10} {s['files']:>6} {s['code']:>8} {s['comment']:>8} {s['blank']:>8} {t:>8}")

    print("-" * 60)
    print(f"{'合计':<10} {totals['files']:>6} {totals['code']:>8} {totals['comment']:>8} {totals['blank']:>8} {total_all:>8}")
    print("=" * 60)

if __name__ == "__main__":
    main()
