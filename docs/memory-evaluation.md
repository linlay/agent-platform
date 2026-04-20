# Memory System Evaluation — A/B 日志对比方案

## 目标

量化记忆系统优化（三层渐进式披露、动态预算、有效重要度衰减/强化、反馈循环）相对于旧版本的效果。

## 前置条件

### 1. 确认 memory 日志已开启

默认已开启，日志路径 `runtime/logs/memory.log`。可通过环境变量覆盖：

```bash
LOGGING_AGENT_MEMORY_ENABLED=true
LOGGING_AGENT_MEMORY_FILE=runtime/logs/memory.log
```

### 2. 确认 auto-learn 已开启

反馈循环依赖 auto-learn 触发。确保 `.env` 中：

```bash
AGENT_MEMORY_AUTO_REMEMBER_ENABLED=true
```

### 3. 日志格式

`memory.log` 是 JSONL 格式，每行一个 JSON 对象，关键字段：

```json
{"ts":"2026-04-20T...", "category":"memory.operation", "operation":"...", ...}
```

## 关键事件

系统在运行时自动写入以下事件，无需额外埋点。

### `build_context_bundle`

每次 `/api/query` 构建记忆上下文时触发。

| 字段 | 类型 | 含义 |
|------|------|------|
| `agentKey` | string | 当前 agent |
| `chatId` | string | 当前会话 |
| `query` | string | 用户输入 |
| `totalCandidates` | int | 记忆库中的候选总数 |
| `stableFacts` | int | 选中的 stable fact 数 |
| `sessionItems` | int | 选中的 session observation 数 |
| `observations` | int | 选中的跨会话 observation 数 |
| `stableChars` | int | stable 层占用字符数 |
| `sessionChars` | int | session 层占用字符数 |
| `observationChars` | int | observation 层占用字符数 |
| `layers` | []string | 实际披露的层（如 `["stable","session","observation"]`） |
| `stopReason` | string | 停止原因（`no_memory` / `stable_only` / `session_added` / `observation_added`） |
| `hybrid` | bool | 是否使用了向量混合检索 |
| `maxChars` | int | 预算上限 |

### `disclosure_feedback`

每次 run 结束后，反馈循环评估记忆使用效果时触发。

| 字段 | 类型 | 含义 |
|------|------|------|
| `chatId` | string | 当前会话 |
| `agentKey` | string | 当前 agent |
| `disclosedTotal` | int | 本轮注入的记忆总数 |
| `referenced` | int | 被 assistant 回复引用的记忆数 |
| `unreferenced` | int | 注入但未被引用的记忆数 |
| `referenceRate` | float | 引用率 = referenced / disclosedTotal |
| `stableCount` | int | 本轮 stable 层记忆数 |
| `sessionCount` | int | 本轮 session 层记忆数 |
| `obsCount` | int | 本轮 observation 层记忆数 |
| `layers` | []string | 本轮披露的层 |
| `hybrid` | bool | 是否使用了向量混合检索 |

## 核心指标

### 1. 引用率（Reference Rate）

**定义**：注入 prompt 的记忆中，有多少被 LLM 实际引用了。

**公式**：`referenceRate = referenced / disclosedTotal`

**含义**：
- 高引用率 → 系统选的记忆确实有用，没有浪费 prompt 空间
- 低引用率 → 注入了很多记忆但 LLM 没用到，说明选取不够精准

**基线目标**：> 0.5 为良好，> 0.7 为优秀

**聚合命令**：

```bash
# 平均引用率
cat runtime/logs/memory.log \
  | jq -r 'select(.operation=="disclosure_feedback") | .referenceRate' \
  | awk '{s+=$1; n++} END {if(n>0) printf "avg_reference_rate: %.3f (samples: %d)\n", s/n, n; else print "no data"}'

# 引用率分布（分 10 个桶）
cat runtime/logs/memory.log \
  | jq -r 'select(.operation=="disclosure_feedback") | .referenceRate' \
  | awk '{bucket=int($1*10); counts[bucket]++; total++} END {for(i=0;i<=10;i++) printf "[%.1f-%.1f): %d (%.1f%%)\n", i/10, (i+1)/10, counts[i]+0, (counts[i]+0)/total*100}'
```

### 2. 选取率（Selection Rate）

**定义**：从所有候选记忆中选取了多少比例。

**公式**：`selectionRate = (stableFacts + sessionItems + observations) / totalCandidates`

**含义**：
- 低选取率 → 筛选精准，只挑了最相关的
- 高选取率 → 可能缺乏有效筛选

**聚合命令**：

```bash
cat runtime/logs/memory.log \
  | jq -r 'select(.operation=="build_context_bundle" and .totalCandidates > 0) | (.stableFacts + .sessionItems + .observations) / .totalCandidates' \
  | awk '{s+=$1; n++} END {if(n>0) printf "avg_selection_rate: %.3f (samples: %d)\n", s/n, n; else print "no data"}'
```

### 3. 层覆盖分布（Layer Coverage）

**定义**：三层（stable / session / observation）各自被使用的频率。

**含义**：
- 如果 session 层从未出现 → 要么没有同会话 observation，要么 auto-learn 没开
- 如果 observation 层从未出现 → 跨会话知识复用不足

**聚合命令**：

```bash
# 各层出现频率
cat runtime/logs/memory.log \
  | jq -r 'select(.operation=="build_context_bundle" and .layers != null) | .layers[]' \
  | sort | uniq -c | sort -rn

# 层组合分布
cat runtime/logs/memory.log \
  | jq -r 'select(.operation=="build_context_bundle" and .layers != null) | [.layers[]] | join("+")' \
  | sort | uniq -c | sort -rn
```

### 4. 预算利用率（Budget Utilization）

**定义**：实际使用的字符数占预算上限的比例。

**公式**：`utilization = (stableChars + sessionChars + observationChars) / maxChars`

**含义**：
- 60%~90% 为健康范围
- < 30% → 记忆库内容太少或筛选太严
- > 95% → 可能在截断有价值内容

**聚合命令**：

```bash
cat runtime/logs/memory.log \
  | jq -r 'select(.operation=="build_context_bundle" and .maxChars > 0) | (.stableChars + .sessionChars + .observationChars) / .maxChars' \
  | awk '{s+=$1; n++} END {if(n>0) printf "avg_budget_utilization: %.3f (samples: %d)\n", s/n, n; else print "no data"}'
```

### 5. 空披露率（Empty Disclosure Rate）

**定义**：没有任何记忆可注入的 query 占比。

**含义**：
- 高空披露率 → 记忆库内容太少，或 agent 没配置 memory 能力

**聚合命令**：

```bash
cat runtime/logs/memory.log \
  | jq -r 'select(.operation=="build_context_bundle") | .stopReason' \
  | sort | uniq -c | sort -rn
```

## 综合仪表盘命令

一次性输出所有核心指标：

```bash
#!/bin/bash
LOG=runtime/logs/memory.log

echo "=== Memory System Evaluation Report ==="
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
echo "--- 7. Per-Layer Reference Rate ---"
echo "  (from disclosure_feedback events)"
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
```

## 评估流程

### 第一阶段：采集基线（1-2 周）

1. 确认 `LOGGING_AGENT_MEMORY_ENABLED=true` 和 `AGENT_MEMORY_AUTO_REMEMBER_ENABLED=true`
2. 正常使用系统，不做任何调整
3. 运行综合仪表盘命令，记录基线数据

### 第二阶段：配置 embedding 后对比

1. 配置 `AGENT_MEMORY_EMBEDDING_PROVIDER_KEY` 启用语义检索
2. 运行一段时间后，再次执行综合仪表盘命令
3. 对比 `hybrid=true` vs `hybrid=false` 的引用率差异：

```bash
# 有 embedding 时的引用率
cat "$LOG" \
  | jq -r 'select(.operation=="disclosure_feedback" and .hybrid==true) | .referenceRate' \
  | awk '{s+=$1; n++} END {if(n>0) printf "hybrid_ref_rate: %.3f (n=%d)\n", s/n, n}'

# 无 embedding 时的引用率
cat "$LOG" \
  | jq -r 'select(.operation=="disclosure_feedback" and .hybrid==false) | .referenceRate' \
  | awk '{s+=$1; n++} END {if(n>0) printf "fallback_ref_rate: %.3f (n=%d)\n", s/n, n}'
```

### 第三阶段：持续监控

将综合仪表盘命令保存为 `scripts/memory-eval.sh`，定期运行，观察趋势：

- 引用率是否随时间上升（feedback 循环在起作用）
- session 层使用率是否稳定
- 空披露率是否下降（记忆库在积累）

## 指标参考范围

| 指标 | 差 | 一般 | 良好 | 优秀 |
|------|-----|------|------|------|
| 引用率 | < 0.2 | 0.2-0.4 | 0.5-0.7 | > 0.7 |
| 选取率 | > 0.5 | 0.3-0.5 | 0.1-0.3 | < 0.1 |
| 预算利用率 | < 0.2 或 > 0.95 | 0.2-0.4 | 0.4-0.6 | 0.6-0.9 |
| 空披露率 | > 0.5 | 0.3-0.5 | 0.1-0.3 | < 0.1 |

## 注意事项

1. **引用率检测基于关键词启发式**：`ComputeFeedback` 使用子串匹配判断 assistant 是否引用了记忆。这意味着如果 LLM 换了措辞引用记忆内容，会被判定为"未引用"，实际引用率可能略高于统计值。

2. **样本量**：至少需要 50+ 次 `disclosure_feedback` 事件才能得到有统计意义的引用率。

3. **agent 差异**：不同 agent 的记忆使用模式可能差异很大。按 `agentKey` 分组分析更有意义：

```bash
cat "$LOG" \
  | jq -r 'select(.operation=="disclosure_feedback") | "\(.agentKey) \(.referenceRate)"' \
  | awk '{sum[$1]+=$2; n[$1]++} END {for(k in sum) printf "  %s: avg_ref_rate=%.3f (n=%d)\n", k, sum[k]/n[k], n[k]}'
```

4. **冷启动期**：记忆库为空时所有指标都没有参考价值。至少需要 10+ 条 fact 和 20+ 条 observation 后再开始评估。
