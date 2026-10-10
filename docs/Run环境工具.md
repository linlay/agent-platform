# Run 环境工具

`run_env` 管理当前普通 native root Run 的动态环境覆盖层，固定 `{operation,params}`；operation 必填，params 省略等同空对象，未知字段、null 或错误类型拒绝。它不是 Secret 存储。全部参数（含 idempotencyKey、value、update.set）与 list 结果按普通工具数据可观测，不增加参数缓冲、脱敏或原始模型帧屏蔽。

## 操作

| operation | params | 结果 |
| --- | --- | --- |
| list | `{}` | variables、revision、usage、limits；只读动态层 |
| explain | `{key?}` | 归一化、key 格式/保护、优先级、继承范围，可校验 key，不读取 Host/静态值 |
| set | `{key,value,expectedRevision?,idempotencyKey?}` | key、changed、idempotent、revision |
| unset | `{key,expectedRevision?,idempotencyKey?}` | key、changed、idempotent、revision |
| update | `{set?:{K:"V"},unset?:[K],expectedRevision?,idempotencyKey?}` | changed、idempotent、revision |

成功数据直接作为工具结果返回，没有第二层 envelope revision。失败使用 error/message 和工具错误状态。update 至少有一个变更项，无 get/clear。

key 先去首尾空白、转大写，再匹配 `^[A-Z_][A-Z0-9_]{0,127}$`。Platform 保留变量、危险 shell/loader/interpreter 变量不可覆盖；deny-keys 仅追加。value 必须为无 NUL 的有效 UTF-8 字符串，允许空字符串和换行。

## 原子性与幂等

set/unset/update 在 Scope 同一把锁中校验与提交。update 按归一化 key 拒绝重复和 set/unset 交集；任意 unset 缺失整批失败；按最终状态校验限额，允许在键数已满时原子替换键。失败无部分写入，无变化 revision 不增加，有变化只增加一次。

`expectedRevision` 为可选非负整数。幂等摘要包含 operation、排序/归一化后的 set/unset 和 expectedRevision（区分缺省与 0），不含幂等键本身。与旧 set/unset 不同，同一幂等键改变 expectedRevision 现在报冲突。

先匹配成功幂等记录，再检查当前 revision：成功请求原样重试返回原结果的 revision/changed，并将 idempotent 标为 true，即使 Scope 当前 revision 已推进。失败不保存成功收据。

模型 idempotencyKey 为 1～128 UTF-8 字节；缺省使用当前 runId:toolId。收据只保存 SHA-256 请求摘要和结果，不保留历史 value。每个 Scope 最多 4096 条；达到上限后新 mutation 返回 run_env_idempotency_limit，已有收据仍可重试，不淘汰以避免旧调用重新执行。收据随 Run 终态释放，不跨重启。

## 调用范围与生命周期

Native GENERAL/CODER/KBASE/TEAM 通过全局/mode preset 或 Agent 自身声明获得 run_env；分发示例将其列入全局 preset-tools，可通过 excludeTools 排除。未配置或被排除时不创建 Scope。Scope 准入使用有效 Tools，不使用 DeclaredTools。平台管理连接器需要显式挂载，创建接口不隐含授予管理权限。

配置挂载不扩大执行范围：仅普通 native root Run 获得 Scope。list/explain 可用于该调用者的只读阶段，set/unset/update 仅在执行阶段可用并作为调度屏障，保持模型调用顺序、审批和启动时的环境快照一致。子任务模型列表隐藏 run_env，TEAM 总控按普通根 Run 使用，执行入口仍拒绝。chat_start 新 root 不继承父 Scope，符合准入时获得自己的空 Scope。

环境优先级：继承的 Host 环境 < Agent 环境 < 按顺序叠加的 Skill 环境 < 当前动态层 < 调用级环境 < Platform 保留上下文。unset 只移除动态层，后续新进程可能重新看到低层同名值。

Scope 仅影响后续适用的 Host 工具进程和 Container 新 command，不修改 Platform 进程环境；子 Agent、其他 Run、Terminal、MCP、ACP、Proxy、Channel、LSP、sidecar 和已启动进程不继承。终态销毁，重启不从历史工具调用恢复变量或收据。question/planning 恢复入口依当前有效挂载重新创建空 Scope，revision=0。

wait 参数、机制、检查点和 steer 不变：现有检查只看动态快照非空或读取失败。空 Scope 可恢复；set 后全部 unset 即使 revision>0 也可恢复，恢复后 revision=0；非空 Scope 的 wait 重启沿用不可恢复等待的失败终态。

## 配置与硬切发布

```yaml
run-env:
  max-dynamic-keys: 32
  max-value-bytes: 4096
  max-total-bytes: 32768
  deny-keys: []
```

没有 run-env.enabled。platform-control.enabled 不再影响动态环境。三个限额必须为正数；总字节用量只累计 value 的 UTF-8 字节，不含 key。list 在同锁下返回 variables/revision/usage/limits，usage.totalBytes 使用同一口径，同时报告收据条数及固定上限。

升级前将 tools.yml 的 platform-control.deny-keys/max-dynamic-keys/max-value-bytes/max-total-bytes 移到 run-env；旧键出现就启动失败，即使已同时配置新键。旧 platform-control.profiles/bindings 继续报错；runtimeConfig.runEnv 继续静默忽略。

platform_control 不再注册任何 run.env 操作，也不在 capabilities.list/runtime.status/结果 envelope 中返回环境字段；security.explain 不再接受环境 key。历史记录不改写。同步存量 Agent 和技能源文件，不修改 ru-agents/ru-connectors。

在线文档流程：create/upload 取得 documentId → run_env set DOCUMENT_HUB_DOCUMENT_ID → HTTPX session/edit/commit/download，不再调用 platform_control capabilities.list。切换文档后重建并验证对应 session/lease。

## 实现与错误

internal/runenv 保存 Scope，internal/runenvops 提供 handler，internal/toolpolicy 保存只读性/阶段/barrier 属性；配置与生命周期由 config、runtime/session、runtime/query、runtime/runstate 装配。Host/Container 原有快照消费者不变。

主要错误：run_env_invalid_operation、run_env_invalid_params、run_env_unavailable、run_env_stage_forbidden、run_env_closed、run_env_key_not_set、run_env_key_invalid、run_env_key_forbidden、run_env_value_invalid、run_env_limit_exceeded、run_env_revision_conflict、run_env_idempotency_conflict、run_env_idempotency_limit。规范化冲突等 Scope 请求错误返回 run_env_invalid_request。
