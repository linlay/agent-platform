#!/usr/bin/env bash
set -euo pipefail

chats_dir="${1:-${CHATS_DIR:-}}"
config_root="${2:-${REGISTRIES_DIR:-}}"

if [[ -z "${chats_dir}" ]]; then
  echo "usage: $0 <CHATS_DIR> [CONFIG_ROOT]" >&2
  echo "or set CHATS_DIR and optionally REGISTRIES_DIR" >&2
  exit 2
fi

if [[ ! -d "${chats_dir}" ]]; then
  echo "CHATS_DIR does not exist: ${chats_dir}" >&2
  exit 2
fi

echo "== legacy chat files =="
find "${chats_dir}" -name events.jsonl -print
find "${chats_dir}" -name raw_messages.jsonl -print
find "${chats_dir}" -maxdepth 1 -name index.json -print

echo
echo "== legacy run ids =="
if command -v rg >/dev/null 2>&1; then
  rg -n '"runId"[[:space:]]*:[[:space:]]*"run_[0-9]{14}\.[0-9]+"' "${chats_dir}" || true
else
  grep -R -n -E '"runId"[[:space:]]*:[[:space:]]*"run_[0-9]{14}\.[0-9]+"' "${chats_dir}" || true
fi

echo
echo "== legacy sqlite columns =="
db_path="${chats_dir}/chats.db"
if [[ -f "${db_path}" ]] && command -v sqlite3 >/dev/null 2>&1; then
  sqlite3 "${db_path}" 'PRAGMA table_info(CHATS);' | grep -E 'PENDING_AWAITING_|READ_STATUS_' || true
elif [[ -f "${db_path}" ]]; then
  echo "sqlite3 not found; skipped ${db_path}"
else
  echo "no chats.db found"
fi

if [[ -n "${config_root}" ]]; then
  echo
  echo "== legacy config fields =="
  if [[ -d "${config_root}" ]]; then
    if command -v rg >/dev/null 2>&1; then
      rg -n 'sandboxConfig' "${config_root}" || true
    else
      grep -R -n -E 'sandboxConfig' "${config_root}" || true
    fi
  else
    echo "CONFIG_ROOT does not exist: ${config_root}" >&2
  fi
fi
