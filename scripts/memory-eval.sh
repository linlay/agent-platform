#!/bin/bash
# Memory System Evaluation Report
# Usage: ./scripts/memory-eval.sh [path-to-memory.log]
# Metrics follow build_context_bundle fields in internal/memory/sqlite_snapshot.go.

set -euo pipefail

LOG="${1:-runtime/memory/memory.log}"

if [ ! -f "$LOG" ]; then
  echo "memory.log not found: $LOG"
  echo "Usage: $0 [path-to-memory.log]"
  exit 1
fi

if ! command -v jq &>/dev/null; then
  echo "jq is required but not installed. Install with: brew install jq"
  exit 1
fi

echo "=== Memory System Evaluation Report ==="
echo "Source: $LOG"
echo "Lines: $(wc -l < "$LOG")"
echo ""

echo "--- 1. Context Bundles ---"
jq -r 'select(.operation=="build_context_bundle") | .agentKey' "$LOG" \
  | awk '{agents[$0]++; n++} END {printf "  bundles: %d  agents: %d\n", n, length(agents)}'

echo ""
echo "--- 2. Selection Rate ---"
jq -r 'select(.operation=="build_context_bundle" and .totalCandidates > 0) | (.stableFacts + .sessionItems + .observations) / .totalCandidates' "$LOG" \
  | awk '{s+=$1; n++} END {if(n>0) printf "  avg: %.3f  samples: %d\n", s/n, n; else print "  no bundle data yet"}'

echo ""
echo "--- 3. Layer Coverage ---"
jq -r 'select(.operation=="build_context_bundle" and .layers != null) | .layers[]' "$LOG" \
  | sort | uniq -c | sort -rn | awk '{printf "  %s: %d\n", $2, $1}'

echo ""
echo "--- 4. Budget Utilization ---"
jq -r 'select(.operation=="build_context_bundle" and .maxChars > 0) | (.stableChars + .sessionChars + .observationChars) / .maxChars' "$LOG" \
  | awk '{s+=$1; n++} END {if(n>0) printf "  avg: %.3f  samples: %d\n", s/n, n; else print "  no budget data yet"}'

echo ""
echo "--- 5. Stop Reason Distribution ---"
jq -r 'select(.operation=="build_context_bundle") | .stopReason' "$LOG" \
  | sort | uniq -c | sort -rn | awk '{printf "  %s: %d\n", $2, $1}'

echo ""
echo "--- 6. Per-Layer Avg Counts ---"
jq -r 'select(.operation=="build_context_bundle") | "\(.stableFacts) \(.sessionItems) \(.observations)"' "$LOG" \
  | awk '{
      stable+=$1; session+=$2; obs+=$3; n++
    } END {
      if(n>0) {
        printf "  stable_avg: %.1f  session_avg: %.1f  obs_avg: %.1f\n", stable/n, session/n, obs/n
        printf "  bundles: %d\n", n
      } else print "  no bundle data yet"
    }'
