# Platform 控制工具设计与实现

## 目标与边界

`platform_control` 是显式挂载的 system tool。所有挂载它的 Agent 使用同一固定 Schema；Skill 只能说明调用方法，不能替 Agent 挂载 Tool。

动态 Run 环境是当前普通 native root run 的轻量覆盖层，只供后续 Tool 新启动的 Host/Container 子进程继承。它不会修改 Platform 进程环境，也不会注入子 Agent、Team、独立 root run、ACP、Proxy、Channel、Terminal、MCP、LSP、sidecar、长期服务或已经启动的进程。

Agent 配置不再声明动态 key。遗留 `runtimeConfig.runEnv` 会被静默忽略，不产生权限、校验规则或运行时状态。

## 固定操作

当前注册操作为：

- `capabilities.list`
- `catalog.defaults.get`
- `catalog.validate`
- `run.env.set`
- `run.env.unset`
- `runtime.status`
- `security.explain`

旧 `run.env.bind/get/list/bulk` 未注册，调用统一返回 `platform_control_invalid_operation`。

### `run.env.set`

参数：

```json
{
  "operation": "run.env.set",
  "params": {
    "key": "DOCUMENT_HUB_DOCUMENT_ID",
    "value": "<documentId>",
    "expectedRevision": 0,
    "idempotencyKey": "optional-retry-key"
  }
}
```

`key`、`value` 必填；`expectedRevision`、`idempotencyKey` 可选。set 创建或覆盖当前 run 的动态值，相同值不提升 revision。空字符串和多行值合法；非法 UTF-8、NUL、超限值、非法/保留/危险 key 会被拒绝。

### `run.env.unset`

参数：

```json
{
  "operation": "run.env.unset",
  "params": {
    "key": "DOCUMENT_HUB_DOCUMENT_ID",
    "expectedRevision": 1,
    "idempotencyKey": "optional-retry-key"
  }
}
```

unset 只能删除当前 run 成功 set 且仍存在的动态值。从未 set、已 unset 或只存在于 Host/Agent/Skill 静态层的 key 返回 `run_env_key_not_set`。相同 `idempotencyKey` 重试已成功的 unset，返回原成功结果。

set/unset 成功数据统一为：

```json
{
  "key": "DOCUMENT_HUB_DOCUMENT_ID",
  "changed": true,
  "idempotent": false,
  "revision": 1
}
```

成功结果不重复返回 value，但 `run.env.set.params.value` 是普通可观测 Tool 参数：它会原样进入 SSE、JSONL、raw messages、provider history、trace、archive、export 和 search，不得用于传递凭据或其他 Secret。`idempotencyKey` 和 `catalog.validate.params.content` 仍在这些边界前脱敏；未知 operation 的通用 `params.value` 仍 fail-closed。

## 校验与错误

平台统一保留：

- portable uppercase key 格式与保留/危险 key denylist；
- `platform-control.deny-keys` 追加 denylist；
- 单值字节、动态 key 数和总字节限制；
- optimistic `expectedRevision`；
- 当前 Scope 内的幂等请求记录；
- operation-aware scheduling barrier；
- `idempotencyKey` 与 Catalog candidate content 脱敏；run-env value 不脱敏。

主要错误码：

| 错误码 | 语义 |
| --- | --- |
| `platform_control_invalid_operation` | 操作未注册，包括旧 bind/get/list/bulk |
| `platform_control_invalid_params` | 参数缺失、类型错误或未知字段 |
| `run_env_unavailable` | 当前执行不是具有 scope 的普通 native root run |
| `run_env_mutation_forbidden` | 子任务或 Team 尝试修改 |
| `run_env_key_not_set` | unset 的 key 不属于当前 run 的现存动态层 |
| `run_env_key_invalid` / `run_env_key_forbidden` | key 格式非法或命中 denylist |
| `run_env_value_invalid` | value 含 NUL、非法 UTF-8或超过单值限制 |
| `run_env_limit_exceeded` | key 数或总量超限 |
| `run_env_revision_conflict` | expectedRevision 与当前 revision 不同 |
| `run_env_idempotency_conflict` | 同一幂等 key 被用于不同参数 |

Run env 不触发专用 HITL；文件与 Bash 仍各自遵守原有 AccessPolicy、bashsec 和 HITL。

## State、注入与环境优先级

显式挂载 `platform_control` 的普通 native root run 在 admission 时取得独立的进程内 `Scope`。Scope 直接维护 values、revision、limits 与幂等状态；set/unset 在同一把锁内完成校验和变更，不读取或写入任何运行状态文件。

环境优先级从低到高为：

```text
Host
  -> Agent/Skill 静态 env
  -> 当前 root run 动态 set
  -> 单次 Tool invocation env
  -> Platform 保留变量
```

unset 只移除第三层。因此 Host、Agent 或 Skill 存在同名低层值时，后续新子进程重新看到低层值。

Host Bash、httpx/dbx、Node、Python、rg/file_glob/file_grep 和 Container 新 command 在每次启动前获取独立 snapshot。Container session reuse fingerprint 只包含静态环境；动态 revision/value 不参与 session 身份。Platform 不调用 `os.Setenv`。

ExecutionContext 的并发 clone 共享同一个 root `Scope`；但构建子任务 session 时禁止从相同 RunID 的 RunManager 取回它。因此子 Agent 即使复用父 RunID 也没有动态 scope。

## 进程生命周期与重启

动态 run env 只属于当前 Platform 进程内的当前普通 root run，不写入 Chat、runtime 目录或其他持久化存储。run 正常完成、失败、interrupt 或 budget stop 后，RunManager 关闭 Scope 并清空内存；终态后的 Scope 拒绝继续访问。

Platform 重启后，question/planning 仍按 HITL 原协议恢复，但恢复的 run 获得 revision `0` 的全新空 Scope。历史 set/unset 不从 Chat 重放，旧进程中的动态值也不会继承。approval/form 的重启行为保持原协议不变。

## 配置

Agent 只需显式挂载 Tool：

```yaml
toolConfig:
  tools:
    - platform_control
```

`configs/tools.yml` 的可选平台配置只控制全局限额：

```yaml
platform-control:
  enabled: true
  max-dynamic-keys: 32
  max-value-bytes: 4096
  max-total-bytes: 32768
  deny-keys: []
```

旧 `platform_config` 与 `platform-control.profiles/bindings` 仍会硬失败。`runtimeConfig.runEnv` 则为了平滑读取遗留 Agent 文件而静默忽略。

## 在线文档流程

在线办公 Skill 的标准流程是：

```text
capabilities.list
  -> create/upload 获取 documentId
  -> run.env.set(DOCUMENT_HUB_DOCUMENT_ID)
  -> session/edit/commit/download
```

同一 run 切换文档时再次 set 新 documentId，并重新建立、验证对应 session/lease；不需要新 run。不要使用内联 `KEY=value httpx ...`。

## 实现位置

| 位置 | 职责 |
| --- | --- |
| `internal/runenv` | 进程内 Scope、limits、revision 与幂等状态 |
| `internal/platformcontrol` | 固定操作注册、参数校验、错误映射、脱敏 |
| `internal/server/session_builder.go` | root native scope admission 与子任务隔离 |
| `internal/contracts/run_control_manager.go` | scope 生命周期与 run 终态 cleanup |
| `internal/tools` / `internal/sandbox` | Host/Container 新 command snapshot |
