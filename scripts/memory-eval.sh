#!/bin/bash
# Memory System Evaluation Report
# Usage: ./scripts/memory-eval.sh [path-to-memory.log]

LOG="${1:-runtime/logs/memory.log}"

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

echo "--- 1. Reference Rate ---"
cat "$LOG" \
  | jq -r 'select(.operation=="disclosure_feedback") | .referenceRate' \
  | awk '{s+=$1; n++} END {if(n>0) printf "  avg: %.3f  samples: %d\n", s/n, n; else print "  no feedback data yet"}'

echo ""
echo "--- 2. Selection Rate ---"
cat "$LOG" \
  | jq -r 'select(.operation=="build_context_bundle" and .totalCandidates > 0) | (.stableFacts + .sessionItems + .observations) / .totalCandidates' \
  | awk '{s+=$1; n++} END {if(n>0) printf "  avg: %.3f  samples: %d\n", s/n, n; else print "  no bundle data yet"}'

echo ""
echo "--- 3. Layer Coverage ---"
cat "$LOG" \
  | jq -r 'select(.operation=="build_context_bundle" and .layers != null) | .layers[]' \
  | sort | uniq -c | sort -rn | awk '{printf "  %s: %d\n", $2, $1}'

echo ""
echo "--- 4. Budget Utilization ---"
cat "$LOG" \
  | jq -r 'select(.operation=="build_context_bundle" and .maxChars > 0) | (.stableChars + .sessionChars + .observationChars) / .maxChars' \
  | awk '{s+=$1; n++} END {if(n>0) printf "  avg: %.3f  samples: %d\n", s/n, n; else print "  no budget data yet"}'

echo ""
echo "--- 5. Stop Reason Distribution ---"
cat "$LOG" \
  | jq -r 'select(.operation=="build_context_bundle") | .stopReason' \
  | sort | uniq -c | sort -rn | awk '{printf "  %s: %d\n", $2, $1}'

echo ""
echo "--- 6. Hybrid Search Usage ---"
cat "$LOG" \
  | jq -r 'select(.operation=="build_context_bundle") | .hybrid' \
  | sort | uniq -c | sort -rn | awk '{printf "  hybrid=%s: %d\n", $2, $1}'

echo ""
echo "--- 7. Per-Agent Reference Rate ---"
cat "$LOG" \
  | jq -r 'select(.operation=="disclosure_feedback") | "\(.agentKey) \(.referenceRate)"' \
  | awk '{sum[$1]+=$2; n[$1]++} END {for(k in sum) printf "  %s: avg_ref_rate=%.3f (n=%d)\n", k, sum[k]/n[k], n[k]}'

echo ""
echo "--- 8. Per-Layer Avg Counts ---"
cat "$LOG" \
  | jq -r 'select(.operation=="disclosure_feedback") | "\(.stableCount) \(.sessionCount) \(.obsCount) \(.referenced) \(.disclosedTotal)"' \
  | awk '{
      stable+=$1; session+=$2; obs+=$3; ref+=$4; total+=$5; n++
    } END {
      if(n>0) {
        printf "  stable_avg: %.1f  session_avg: %.1f  obs_avg: %.1f\n", stable/n, session/n, obs/n
        printf "  overall_ref_rate: %.3f  total_runs: %d\n", (total>0?ref/total:0), n
      } else print "  no data"
    }'
