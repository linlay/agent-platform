#!/usr/bin/env python3

import argparse
import json


def normalize_space(text: str) -> str:
    return " ".join(text.split())


def main() -> int:
    parser = argparse.ArgumentParser(description="Text metrics utility")
    parser.add_argument("--text", required=True)
    parser.add_argument("--normalize-space", action="store_true", dest="normalize_space")
    args = parser.parse_args()

    source = args.text
    normalized = normalize_space(source) if args.normalize_space else source

    if normalized == "":
        words = 0
        lines = 0
    else:
        words = len(normalized.split())
        lines = len(normalized.splitlines())

    payload = {
        "ok": True,
        "chars": len(normalized),
        "words": words,
        "lines": lines,
        "normalizedText": normalized,
    }
    print(json.dumps(payload, ensure_ascii=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
