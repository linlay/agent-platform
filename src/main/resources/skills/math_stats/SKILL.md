---
name: "math_stats"
description: "Use when the task requires deterministic statistics over a numeric list via Python script execution."
---

# Math Stats Skill

## Trigger
- Use this skill for summary statistics on numeric sequences.
- Prefer script execution for reproducible mean/median/mode/stdev results.

## Script
- `scripts/stats.py`

## Parameters
- `--values`: comma-separated numeric values, for example `1,2,3,4`
- `--metric`: `summary|count|sum|min|max|mean|median|mode|stdev` (default `summary`)
- `--precision`: optional integer, default `6`

## Example
- `_skill_run_script_` with:
  - `skill`: `math_stats`
  - `script`: `scripts/stats.py`
  - `args`: `["--values","1,2,3,4","--metric","summary"]`

## Output
- stdout JSON on success with `ok=true`.
- `summary` returns multiple statistics in one object.
