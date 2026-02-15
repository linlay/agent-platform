---
name: "text_utils"
description: "Use when the task requires deterministic text metrics and normalized text output via Python script execution."
---

# Text Utils Skill

## Trigger
- Use this skill for deterministic text metrics.
- Prefer script execution when you need exact char/word/line counts.

## Script
- `scripts/text_metrics.py`

## Parameters
- `--text`: input text
- `--normalize-space`: optional flag; collapse internal whitespace and trim boundaries

## Example
- `_skill_script_run_` with:
  - `skill`: `text_utils`
  - `script`: `scripts/text_metrics.py`
  - `args`: `["--text","hello   world","--normalize-space"]`

## Output
- stdout JSON on success:
  - `{"ok":true,"chars":11,"words":2,"lines":1,"normalizedText":"hello world"}`
