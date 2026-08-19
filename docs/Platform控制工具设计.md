# Platform 控制工具设计与实现

> 状态：首期已实现。本文记录 `platform_control`、当前 run 动态环境、权限、恢复和执行通道的正式契约。`POST /api/query`、前端协议和 Chat API 均未增加字段。

## 1. 定位与结论

`platform_control` 是 Agent Platform 的 system control plane。首期提供能力发现、Catalog 默认值/候选校验、当前 run 环境变量、脱敏运行状态和安全解释，不提供进程全局环境修改、持久配置写入、重启、策略修改、secret reveal 或任意命令执行。

所有显式配置该工具的 Agent 看到同一份固定 JSON Schema，并可调用全部注册 operation。stage、root/child/team 形态和 run env 状态只决定 operation 当前是否可执行，不再存在按 Agent/profile 的二次 operation 授权。Skill 只负责说明操作方法；Skill 热重载和 `mustUseSkills` 都不能替 Agent 挂载 Tool，也不能授予环境变量 key、MCP 或文件访问权限。

旧 `platform_config` 已移除且没有模型可见 alias。Agent 配置引用旧名称会在 Catalog 校验时硬失败并提示改用 `platform_control`。历史 Chat 只回放，不迁移或重新执行历史 tool call。

### 1.1 命名选择

最终名称选择 `platform_control`，因为它表示 Platform 级控制面，并可用 `catalog.*`、`run.env.*`、`runtime.*`、`security.*` 稳定扩展能力分类。`run_env` 和 `runtime_env` 都过度限定为环境变量，无法容纳 Catalog/Runtime/Security；`system_env` 容易被误解为 Platform 进程全局环境；`run_context` 无法表达治理和风险操作；旧 `platform_config` 则错误暗示工具只读写持久配置。

## 2. 固定工具契约

内建定义位于 `internal/resources/tools/platform_control.yml`：

```yaml
name: platform_control
clientVisible: true
explicitOnly: true
readOnly: false
operationAware: true
inputSchema:
  type: object
  properties:
    operation:
      type: string
      enum:
        - capabilities.list
        - catalog.defaults.get
        - catalog.validate
        - run.env.bind
        - run.env.set
        - run.env.unset
        - run.env.get
        - run.env.list
        - run.env.bulk
        - runtime.status
        - security.explain
    params:
      type: object
      maxProperties: 32
      description: Operation-specific parameters; follow the active Skill.
  required: [operation]
  additionalProperties: false
```

`params` 有意保持普通 object，不使用 `oneOf/allOf/if/then`。`internal/platformcontrol` 中的 operation descriptor 和严格 validator 检查每个 operation 的必填字段、类型、未知字段及大小。descriptor 固定记录：

```text
Name, RiskClass, ReadOnly, Barrier, SensitivePaths,
AllowedStages
```

LLM planning 准入、并发调度、HITL 和参数脱敏读取 operation descriptor，不再仅按工具级 `readOnly:false` 判断。

结果统一包装为：

```json
{
  "operation": "run.env.bind",
  "status": "ok",
  "scope": "run",
  "revision": 1,
  "data": {}
}
```

业务失败仍使用 `ToolExecutionResult.Error`、`ExitCode=-1`，envelope 的 `status` 为 `error`。首期不把未来 operation 预放进 enum。

## 3. Operation 分类与参数

### 3.1 能力、Catalog、Runtime 与 Security

- `capabilities.list`：`params={}`。返回当前 run/stage 实际可执行的 operations、run env 限制和 revision；不再返回 profiles。
- `catalog.defaults.get`：`params.path` 只接受 `agents.creation.coder` 或 `agents.creation.kbase`，返回现有创建默认值，不回显凭据。
- `catalog.validate`：严格接收 `resourceType/resourceKey/content`；类型为 `agent/team/skill/mcp-server`，content 最大 1 MiB。只校验 candidate，不写文件或触发热重载。
- `runtime.status`：`params={}`。返回 Platform Control、Container Hub、Memory 和当前 run env 的脱敏摘要。
- `security.explain`：接收目标 `operation`，可附 `key/path`；解释 descriptor、stage/root/team/run-env 可用性与 runEnv key policy，不修改策略。

### 3.2 `run.env.*`

- `run.env.bind`：`key/value`，可选 `expectedRevision/idempotencyKey`。只用于 `mode: bind`；首次写入，相同值重试幂等成功，不同值返回 `run_env_already_bound`，不可 unset。
- `run.env.set`：参数同 bind，只用于 `mode: mutable`。
- `run.env.unset`：`key`，可选 revision/幂等键；只删除动态层，随后回退静态层。
- `run.env.get`：`key`；只返回 presence、mode、source、secret、targets 和 revision。
- `run.env.list`：无参数；只列当前 consumer policy 可见的 metadata。
- `run.env.bulk`：`changes` 最多 16 项，每项只允许 bind/set/unset；整批预校验、一次审批、一次 checkpoint、一次 revision 提升，失败零变更。

所有返回值都不含环境变量 value。所有 `params.value`、bulk value、`catalog.validate.params.content` 和 `idempotencyKey` 都属于敏感参数，不因 `secret:false` 取消脱敏。

常用错误码：

| 错误码 | 含义 |
| --- | --- |
| `platform_control_invalid_operation` | operation 不在固定 registry |
| `platform_control_invalid_params` | 字段缺失、类型错误、未知字段或字段数量超限 |
| `platform_control_disabled` | 全局关闭 |
| `platform_control_stage_forbidden` | planning/read-only stage 请求 mutation |
| `run_env_unavailable` | 当前 run 没有动态 State |
| `run_env_mutation_forbidden` | Team 或子 Agent 请求 mutation |
| `run_env_approval_required` | key 要求 each-change approval |
| `run_env_key_invalid` / `run_env_key_forbidden` | 名称非法、保留、deny 或 policy 未声明 |
| `run_env_operation_forbidden` | bind/mutable mode 与 operation 不匹配 |
| `run_env_value_invalid` / `run_env_limit_exceeded` | value 或容量限制失败 |
| `run_env_already_bound` | bind key 已绑定不同值 |
| `run_env_revision_conflict` | optimistic revision 冲突 |
| `run_env_idempotency_conflict` | 同一幂等键用于不同 payload |
| `run_env_checkpoint_failed` / `run_env_restore_failed` | 加密状态写入或恢复失败 |

## 4. 工具挂载与动态 key 事实源

可选的 `configs/tools.yml -> platform-control` 只用于覆盖全局开关、run-env 限额、追加 denylist 和 checkpoint key；普通部署省略整节并使用代码默认值：

```yaml
platform-control:
  enabled: true
  max-dynamic-keys: 32
  max-value-bytes: 4096
  max-total-bytes: 32768
  max-bulk-operations: 16
  deny-keys: []
```

Agent 必须在 `toolConfig.tools` 中显式配置 `platform_control`；`explicitOnly:true` 保证其他 Agent 不会偶然获得它。显式挂载就是完整 operation 授权，不再通过 `profiles/bindings` 按 Agent 二次分组。旧 `platform-control.profiles` 和 `platform-control.bindings` 会使配置加载硬失败，避免无效配置被静默忽略。

每个动态 key 还必须由 Agent 的 `runtimeConfig.runEnv` 声明：

```yaml
runtimeConfig:
  runEnv:
    DOCUMENT_HUB_DOCUMENT_ID:
      mode: bind
      secret: false
      pattern: '[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}'
      maxBytes: 36
      allowEmpty: false
      allowMultiline: false
      approval: none
      targets: [host]
```

普通 root Agent 可 mutation。子 Agent共享父 run State，但只能按父 policy 与自己的 `runEnv` policy 交集读取和消费，不能 mutation；无交集 key 不注入也不出现在 get/list。Team 首期不创建动态 State。`run_query` 启动的新 root run 不继承父 State。

## 5. 数据模型、校验与优先级

`internal/runenv` 提供 `Policy/KeyPolicy/State/Store`。`QuerySession` 和 `ExecutionContext` 持有：

```text
StaticRuntimeEnv  // admission 时冻结的 Agent + configured Skills 静态层
RunEnvironment   // runtime-only 共享指针
RunEnvPolicy     // 当前 Agent 的 frozen consumer policy
```

ExecutionContext clone 深拷贝静态 map，但共享同一个并发安全 State；不再复制动态 map，也不通过 batch merge 回写。State 以 `RWMutex` 建立 mutation/snapshot 的线性化点；每个 snapshot 返回独立 map 和 revision，只能看到完整旧 revision 或完整新 revision。

有效优先级从低到高：

```text
Host process base
< Agent runtimeConfig.env
< configured Skills .runtime-env.json
< current run dynamic env
< invocation env
< Platform reserved context
< Host Bash AP_ACCESS_TOKEN special injection
```

`mustUseSkills` 不合并额外 Skill 的 runtime env。`unset` 只删除动态层，不屏蔽静态或 Host process base。

key 必须匹配 `^[A-Z_][A-Z0-9_]{0,127}$`；Windows 按大小写不敏感合并环境。默认限制是 32 个动态 key、单值 4096 bytes、总量 32768 bytes、bulk 16 项。value 必须为合法 UTF-8、无 NUL，默认禁止空值和 CR/LF；`pattern` 始终按 full match 解释。

永久保留且大小写变体均禁止：

```text
AP_AGENT_CONFIG_HOME AP_WORKSPACE_DIR AP_CHAT_DIR AP_ACCESS_TOKEN
```

hard denylist 包括 PATH/PATHEXT/COMSPEC/SYSTEMROOT/WINDIR、shell init/prompt、动态加载器、NODE_OPTIONS、PYTHONPATH/PYTHONHOME、RUBYOPT、PERL5OPT、GIT_SSH_COMMAND、SSH_ASKPASS。`deny-keys` 只能追加，不能放宽 hard deny。

mutation 默认以 `runId + toolId` 幂等；显式 `idempotencyKey` 允许 1–128 字符。State 用 checkpoint key 做 HMAC，只持久化幂等 ID/参数指纹，不记录普通 value hash。

## 6. 并发、Barrier 与 HITL

只读 operation 可以按既有并行工具规则执行。所有 `run.env` mutation descriptor 都是 barrier；同一 assistant turn 只要有 mutation，整个 batch 按模型原始顺序串行：

```text
[run.env.bind, bash/httpx] -> 后者看到新 revision
[bash/httpx, run.env.bind] -> 前者看到旧 revision
```

`approval: each-change` 使用现有 approval HITL。展示内容只有 operation、key、value byte length、`source=run.dynamic` 和 targets，不含 value。进入等待时若未显式给 revision，Platform 注入当前 `expectedRevision`；批准后执行前重新走 operation/stage/policy/revision/value 全部校验。批准令牌按 tool ID 一次性消费。

等待中的 approval 不持久化明文参数。进程重启后沿用现有“approval 不可恢复”的终态对账；question/planning 可恢复路径见下一节。

## 7. Checkpoint、恢复与销毁

`Store` 默认使用：

```text
<AP_RUNTIME_DIR>/run-state/<sha256(runId)>.checkpoint
<AP_RUNTIME_DIR>/identity/run-env.key
```

可用 `runtime.yml -> paths.run-state-dir` 和 `tools.yml -> platform-control.checkpoint-key-file` 调整。密钥是首次使用时以 `0600` 独占创建的 32-byte key。checkpoint 使用 AES-256-GCM；AAD 绑定 runId、chatId、持久化 query subject、owner、agentKey 和 policy hash。mutation 在内存 publish 前先原子替换 checkpoint；Windows 使用 replace-existing rename 语义。

question/planning 同 RunID 跨进程恢复时，必须成功读取密钥、解密并验证 AAD/policy。文件缺失、篡改、错误密钥或 owner/policy 变化均以 `run_env_restore_failed` 终结受影响 run、清理 pending awaiting，不会使其他 run 或 Platform 启动失败，也禁止用空状态继续。planning 产生新的 ContinuationRunID 时创建空 State，不继承旧值。

正常完成、失败、interrupt、budget stop 和恢复终态最终都由 RunManager `Finish` 销毁 State：先禁止新 snapshot/mutation，尽力覆零内存 byte buffer，删除 checkpoint，再移除 Store/RunManager 索引。Admission 冲突等尚未注册的 State 也有独立清理路径。

## 8. 执行通道边界

| 通道 | 动态继承 | 边界 |
| --- | --- | --- |
| Host Bash / Windows PowerShell | 是 | 每次 `exec.Command` 前取 host snapshot；不调用 `os.Setenv` |
| Bash 内启动的 httpx/Node/Python | 是 | 普通子进程继承同一命令快照 |
| Platform 直接短进程（如 rg） | 是 | 复用统一 `mergeCommandEnv` |
| Container Hub 新 command | 是 | 每次 command 获取 container snapshot |
| Container session base/fingerprint | 否（只含静态 + reserved） | revision/value 不影响 session reuse key |
| 已启动/后台进程 | 不动态更新 | OS 环境是创建时副本 |
| Workspace Terminal | 否 | 无 run ExecutionContext，维持既有固定 context |
| MCP stdio / HTTP | 否 | registry 级或远端长生命周期会话 |
| ACP / Proxy / Channel / LSP | 否 | 不建立 run env State 或不接入该执行上下文 |
| KBASE sidecar / extractor、长期服务 | 否 | Platform 基础设施不接收 Agent 动态值 |

子进程内部的 `export`/`set` 只影响该进程树，不能反向修改 Platform State。代码路径严禁 `os.Setenv`。

## 9. online-docx 流程

1. `httpx create/upload` 返回真实 `documentId`。
2. Agent 调用：

```json
{"operation":"run.env.bind","params":{"key":"DOCUMENT_HUB_DOCUMENT_ID","value":"<documentId>"}}
```

3. Platform 提交 checkpoint 和 revision。
4. 后续 Host Bash/httpx 启动时获取 run snapshot。
5. online-docx TOML 使用：

```toml
path = { from = "env", key = "DOCUMENT_HUB_DOCUMENT_ID", trim = true, pattern = "^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$", output_template = "/api/v1/documents/{{value}}/ai/session" }
```

TOML 不使用 `from="shell"`、`/bin/sh` 或 `KEY=value httpx`。相邻 httpx builtin 已提供 `env + trim + full-match pattern + output_template`；pattern 先校验原值，之后才将原值字面替换到 `{{value}}` 占位符。

首期不提供显式 `document_id` action 参数。后续若扩展统一 coalesce，优先级为合法显式参数高于 run env；非法显式参数必须失败，不能回退。

## 10. 实现位置

| 位置 | 职责 |
| --- | --- |
| `internal/platformcontrol/operations.go` | operation registry、descriptor、approval 摘要与参数脱敏 |
| `internal/platformcontrol/tool_handler.go` | 严格参数校验、当前 run/stage 可用性、11 个 operation 和固定 envelope |
| `internal/runenv/policy.go` | Agent policy、name/value/target/full-match 校验与 hard deny |
| `internal/runenv/state.go` | RWMutex State、snapshot、bind/set/unset/bulk、revision 与 HMAC 幂等 |
| `internal/runenv/store.go` | AES-GCM checkpoint、恢复、原子替换和 Store 索引 |
| `internal/contracts/interfaces.go` | `StaticRuntimeEnv`、`RunEnvironment`、consumer policy 和一次性 approval |
| `internal/server/session_builder.go` | admission 时冻结静态 env/policy、root 创建/child 共享 State |
| `internal/contracts/run_control_manager.go` | State 索引和终态统一销毁 |
| `internal/server/recovered_awaiting_run.go` | question/planning fail-closed 恢复 |
| `internal/tools/tool_bash.go` | Host/Windows/直接子进程快照与 reserved/token 合并 |
| `internal/sandbox/service.go` | Container session base 与 command snapshot 拆分 |
| `internal/llm/run_stream_*` | operation-aware planning/barrier、HITL 与 clone 共享 |
| `internal/llm/delta_mapper.go`、`chat_trace.go` | 参数缓冲、SSE/回放/trace 脱敏 |
| `internal/catalog/agent_loader.go` | `runtimeConfig.runEnv` 解析与不支持 mode 拒绝 |
| `internal/resources/tools/platform_control.yml` | 固定模型可见 Schema |

## 11. 测试矩阵与发布门禁

Go 自动化覆盖显式 Tool 授予全部 operation、policy/value 限制、bind 幂等、mutable/unset 回退、bulk 原子性、revision、HMAC 幂等、并发 snapshot、checkpoint 恢复/篡改/cleanup、Skill 不挂载 Tool、child policy 交集、operation-aware planning/barrier、SSE 参数脱敏、Host 环境继承且父进程不变、Container reuse/command snapshot 和 Windows 大小写环境合并。

| 层级 | 必须覆盖 |
| --- | --- |
| Unit | 11 个 operation 的严格参数、显式 Tool/stage/root-child-team、name/value/limit、bind/set/unset/bulk、revision/幂等、脱敏 |
| Concurrency | bind→bash 与逆序、需审批 barrier 不重排、snapshot/mutation 线性化、两个 run 不串值、`go test -race ./...` |
| Lifecycle | question/planning 恢复、缺文件/篡改/错密钥/policy 变更 fail-closed、所有终态 cleanup |
| Execution | Linux/macOS Host、Windows PowerShell/CreateProcess、Container 复用 session 的新 command；Terminal/MCP/ACP/LSP/sidecar 负向用例 |
| Persistence | SSE、attach backlog、Chat JSONL、raw messages、trace/log、archive/export/search 均不得出现明文 value |
| Online office | create/upload → bind → session/edit/commit/download，TOML 无 shell，httpx 路径 full-match 校验 |

交付前运行：

```bash
go test ./...
go test -race ./...
make test
```

相邻 httpx 运行 `go test ./...`，并用 online-docx config 做 `inspect --reveal` URL 验证。真实 Windows CI、真实 Container Hub 和 document-hub/WorkPanel 端到端仍需在相应环境执行；本地通过不能替代这些发布门禁。

## 12. 分阶段交付

1. 控制面重构：不兼容删除 `platform_config`，引入固定 Schema、operation registry 与现有 Catalog 能力迁移；后续移除 operation profiles，显式挂载 Tool 即授予全部 operation。
2. Run env 核心：引入 policy/State/Store/checkpoint，冻结静态层，接入 Host/Container command snapshot 和统一终态销毁。
3. LLM/HITL/回放安全：按 descriptor 执行 planning/barrier/approval，对工具参数全链路缓冲和脱敏，恢复失败终结单个 run。
4. Online office 迁移：httpx 增加 env/trim/full-match/output_template，online-docx 切换为 bind + env，移除 shell 路径。
5. 发布验收：以全量 Go/make/race、真实 Windows、Container Hub 和 online-office E2E 为门禁；未通过外部环境门禁时不发布对应 builtin 或宣称端到端完成。
