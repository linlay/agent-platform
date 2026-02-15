#!/usr/bin/env python3

import argparse
import json
import statistics
import sys
from typing import List


ALLOWED_METRICS = {"summary", "count", "sum", "min", "max", "mean", "median", "mode", "stdev"}


def _round(value: float, precision: int) -> float:
    return float(f"{value:.{precision}f}")


def _parse_values(raw: str) -> List[float]:
    if raw is None or raw.strip() == "":
        raise ValueError("values must not be empty")
    parts = [item.strip() for item in raw.split(",") if item.strip() != ""]
    if not parts:
        raise ValueError("values must not be empty")
    try:
        return [float(item) for item in parts]
    except ValueError as exc:
        raise ValueError("values must be numeric") from exc


def _summary(values: List[float], precision: int) -> dict:
    data = {
        "count": len(values),
        "sum": _round(sum(values), precision),
        "min": _round(min(values), precision),
        "max": _round(max(values), precision),
        "mean": _round(statistics.fmean(values), precision),
        "median": _round(statistics.median(values), precision),
    }
    try:
        data["mode"] = _round(statistics.mode(values), precision)
    except statistics.StatisticsError:
        data["mode"] = None
    if len(values) >= 2:
        data["stdev"] = _round(statistics.stdev(values), precision)
    else:
        data["stdev"] = None
    return data


def main() -> int:
    parser = argparse.ArgumentParser(description="Deterministic statistics calculator")
    parser.add_argument("--values", required=True)
    parser.add_argument("--metric", default="summary")
    parser.add_argument("--precision", type=int, default=6)
    args = parser.parse_args()

    precision = max(0, min(args.precision, 12))
    metric = args.metric.strip().lower()
    if metric not in ALLOWED_METRICS:
        print(f"unsupported metric: {metric}", file=sys.stderr)
        return 2

    try:
        values = _parse_values(args.values)
    except ValueError as exc:
        print(str(exc), file=sys.stderr)
        return 2

    data = _summary(values, precision)
    if metric == "summary":
        payload = {"ok": True, "metric": metric, **data}
    else:
        payload = {
            "ok": True,
            "metric": metric,
            metric: data[metric],
        }

    print(json.dumps(payload, ensure_ascii=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
