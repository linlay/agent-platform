#!/usr/bin/env bash
set -euo pipefail

BASE_URL="${BASE_URL:-http://127.0.0.1:11949}"
AGENT_KEY="${AGENT_KEY:-jiraWeeklyReportAssistant}"
CHAT_ID="${CHAT_ID:-memory-verify-001}"
MEMORY_DB="${MEMORY_DB:-/Users/joe/gtja/linlay/zenmind-env/memory/memory.db}"
MEMORY_LOG="${MEMORY_LOG:-runtime/logs/memory.log}"
ROUND1_OUT="/tmp/memory_verify_round1.json"
ROUND2_OUT="/tmp/memory_verify_round2.json"
REQUEST_ID_1="${REQUEST_ID_1:-verify-memory-write-001}"
REQUEST_ID_2="${REQUEST_ID_2:-verify-memory-read-001}"

echo "== Round 1: create memory =="
curl -s "$BASE_URL/api/query" \
  -H 'Content-Type: application/json' \
  -d "{
    \"agentKey\": \"$AGENT_KEY\",
    \"requestId\": \"$REQUEST_ID_1\",
    \"chatId\": \"$CHAT_ID\",
    \"message\": \"记住：我每周都需要记录工时。\"
  }" >"$ROUND1_OUT"

cat "$ROUND1_OUT"
echo

sleep 3

echo "== Round 2: verify memory injection =="
curl -s "$BASE_URL/api/query" \
  -H 'Content-Type: application/json' \
  -d "{
    \"agentKey\": \"$AGENT_KEY\",
    \"requestId\": \"$REQUEST_ID_2\",
    \"chatId\": \"$CHAT_ID\",
    \"message\": \"帮我安排下周工时\"
  }" >"$ROUND2_OUT"

cat "$ROUND2_OUT"
echo

sleep 3

echo "== Memory log tail =="
tail -n 100 "$MEMORY_LOG" || true
echo

echo "== Filtered memory events =="
rg "\"chatId\":\"$CHAT_ID\"|\"requestId\":\"$REQUEST_ID_1\"|\"requestId\":\"$REQUEST_ID_2\"" "$MEMORY_LOG" | \
  rg '"operation":"write"|"operation":"tool_invocation"|"operation":"learn"|"operation":"auto_learn"|"operation":"llm_prompt_memory"' || true
echo

echo "== MEMORIES =="
MEMORIES_OUTPUT="$(sqlite3 -header -column "$MEMORY_DB" \
'select ID_, AGENT_KEY_, CHAT_ID_, CATEGORY_, SUMMARY_, UPDATED_AT_
 from MEMORIES
 where AGENT_KEY_ = '"'"$AGENT_KEY"'"' and CHAT_ID_ = '"'"$CHAT_ID"'"'
 order by UPDATED_AT_ desc limit 20;')"
printf '%s\n' "$MEMORIES_OUTPUT"
echo

echo "== MEMORY_FACTS =="
FACTS_OUTPUT="$(sqlite3 -header -column "$MEMORY_DB" \
'select mf.ID_, mf.AGENT_KEY_, mf.TITLE_, mf.CONTENT_, mf.STATUS_, mf.UPDATED_AT_
 from MEMORY_FACTS mf
 join MEMORIES m on m.ID_ = mf.ID_
 where mf.AGENT_KEY_ = '"'"$AGENT_KEY"'"' and m.CHAT_ID_ = '"'"$CHAT_ID"'"'
 order by mf.UPDATED_AT_ desc limit 20;')"
printf '%s\n' "$FACTS_OUTPUT"
echo

echo "== MEMORY_OBSERVATIONS =="
OBS_OUTPUT="$(sqlite3 -header -column "$MEMORY_DB" \
'select ID_, AGENT_KEY_, CHAT_ID_, TITLE_, SUMMARY_, STATUS_, UPDATED_AT_
 from MEMORY_OBSERVATIONS
 where AGENT_KEY_ = '"'"$AGENT_KEY"'"' and CHAT_ID_ = '"'"$CHAT_ID"'"'
 order by UPDATED_AT_ desc limit 20;')"
printf '%s\n' "$OBS_OUTPUT"
echo

echo "== Validation =="
PASS=1

if ! rg -q "\"operation\":\"write\".*\"chatId\":\"$CHAT_ID\"|\"chatId\":\"$CHAT_ID\".*\"operation\":\"write\"" "$MEMORY_LOG"; then
  echo "[FAIL] memory.log 缺少本次 chat 的 operation=write"
  PASS=0
else
  echo "[PASS] memory.log 包含本次 chat 的 operation=write"
fi

if ! rg -q "\"operation\":\"auto_learn\".*\"requestId\":\"$REQUEST_ID_1\"|\"requestId\":\"$REQUEST_ID_1\".*\"operation\":\"auto_learn\"" "$MEMORY_LOG"; then
  echo "[FAIL] memory.log 缺少本次首轮请求的 operation=auto_learn"
  PASS=0
else
  echo "[PASS] memory.log 包含本次首轮请求的 operation=auto_learn"
fi

if ! rg -q "\"operation\":\"llm_prompt_memory\".*\"requestId\":\"$REQUEST_ID_2\"|\"requestId\":\"$REQUEST_ID_2\".*\"operation\":\"llm_prompt_memory\"" "$MEMORY_LOG"; then
  echo "[FAIL] memory.log 缺少本次次轮请求的 operation=llm_prompt_memory"
  PASS=0
else
  echo "[PASS] memory.log 包含本次次轮请求的 operation=llm_prompt_memory"
fi

if ! grep -q '^mem_' <<<"$FACTS_OUTPUT" && ! grep -q '^mem_' <<<"$OBS_OUTPUT"; then
  echo "[FAIL] 本次 chat 的 MEMORY_FACTS 和 MEMORY_OBSERVATIONS 都为空"
  PASS=0
else
  echo "[PASS] 本次 chat 的 MEMORY_FACTS / MEMORY_OBSERVATIONS 至少有一类非空"
fi

if [ "$PASS" -eq 1 ]; then
  echo "== Result: PASS =="
  exit 0
fi

echo "== Result: FAIL =="
exit 1
