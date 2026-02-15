#!/usr/bin/env python3

import argparse
import json
import math
import sys


def _round(value: float, precision: int) -> float:
    return float(f"{value:.{precision}f}")


def main() -> int:
    parser = argparse.ArgumentParser(description="Deterministic arithmetic calculator")
    parser.add_argument("--op", required=True, choices=["add", "sub", "mul", "div", "pow", "mod"])
    parser.add_argument("--a", required=True, type=float)
    parser.add_argument("--b", required=True, type=float)
    parser.add_argument("--precision", type=int, default=6)
    args = parser.parse_args()

    precision = max(0, min(args.precision, 12))
    a = args.a
    b = args.b

    try:
        if args.op == "add":
            result = a + b
        elif args.op == "sub":
            result = a - b
        elif args.op == "mul":
            result = a * b
        elif args.op == "div":
            if b == 0:
                raise ValueError("division by zero")
            result = a / b
        elif args.op == "pow":
            result = math.pow(a, b)
        elif args.op == "mod":
            if b == 0:
                raise ValueError("modulo by zero")
            result = math.fmod(a, b)
        else:
            raise ValueError(f"unsupported op: {args.op}")
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    payload = {
        "ok": True,
        "op": args.op,
        "a": _round(a, precision),
        "b": _round(b, precision),
        "result": _round(result, precision),
    }
    print(json.dumps(payload, ensure_ascii=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
