---
name: "math_basic"
description: "Use when the task requires deterministic arithmetic operations via Python script execution."
---

# Math Basic Skill

## Trigger
- Use this skill for deterministic arithmetic calculations.
- Prefer script execution when exact numeric results are required.

## Script
- `scripts/calc.py`

## Parameters
- `--op`: `add|sub|mul|div|pow|mod`
- `--a`: numeric operand
- `--b`: numeric operand
- `--precision`: optional integer, default `6`

## Example
- `_skill_script_run_` with:
  - `skill`: `math_basic`
  - `script`: `scripts/calc.py`
  - `args`: `["--op","mul","--a","2","--b","3"]`

## Output
- stdout JSON on success:
  - `{"ok":true,"op":"mul","a":2.0,"b":3.0,"result":6.0}`
