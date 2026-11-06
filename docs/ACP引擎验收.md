# ACP 引擎契约与验收

本文件随 Platform 提交，维护宿主责任、可执行测试入口与待实测门禁。工作区计划、评审答复和阶段编号仅作历史背景；独立检出不需要读取这些文件。当前预期以源码和专题设计为准，历史通过不代表当前候选通过。

## 契约与职责

| 位置 | 责任 |
| --- | --- |
| `internal/contracts/agentengine/` | 普通 Go 类型、显式校验与行为样例；不是安装授权或通用 Schema 系统 |
| `internal/agentengine/` | Desktop bootstrap / standalone 管理、可信交付、目录修订、快照、候选验证与租约 |
| `internal/runtime/acp/` | ACP v1 / JSON-RPC 2.0 / stdio、进程树、会话/连接复用、配置预览、认证、上下文、HITL 与事件转换 |
| `internal/runtime/query/` | 正式 Query 统一准入、后端分派、现有 Run 与收尾 |
| `internal/server/handler_agent_engines.go` | 已验证身份、HTTP DTO、目录/配置/认证接口与错误映射 |

宿主只识别受信 Manifest 的能力，不按 Codex/Claude/Kimi 名称分派。引擎全局身份是 pluginId/localEngineId；声明 `protocol=acp`、`transport=stdio`，Node/executable 使用既有包内入口与依赖校验。业务请求不得提供可执行命令、安装路径或管理凭据。新引擎原则上只新增插件；确需新 ACP 能力时更新通用协商及验收，不修改 AGW 来包装厂商私有字段。

Desktop 受信管理与普通应用身份分离；bootstrap/epoch 不进入日志、前端或插件。standalone 使用本机管理员 CLI，安装过程和命令见 [独立部署](ACP独立部署.md)。JSON 字段和错误码直接读本端 contracts 与 handler，不复制外部计划中的旧版本表。

## 必须保持的不变量

- 安装、启用意图、已同步目录、认证、会话准备及活动 Run 是不同状态。无信任或依赖不匹配时拒绝，不回退旧 HTTP Bridge。
- Chat 绑定、profile、上游 sessionId 与不可变程序快照分离；升级/卸载保留 Chat/profile，租约在所属进程树真实退出后释放。
- 同引擎/profile/规范 Workspace 且协商 close + resume/load 后才能复用连接；审批、配置和取消按 session 隔离，进程崩溃仍可能影响该连接所有会话。
- 空闲回收保留绑定，只回收已证明安全的会话；活动、审批、submitted 不确定状态及未知后台工具不能通过 end_turn 推断排空。安全恢复失败不得悄悄 new 丢失历史。
- 配置预览无 Chat 绑定、无 Prompt、不能驱逐用户会话；缓存不能授权执行。发送仍须真实准备和上游配置确认。
- prepare 成功不等于已认证；外部恢复凭据后可显式重试，无效凭据仍失败，不自动重放 Query。厂商本地认证来源由插件维护。
- ACP 事件进入现有 Delta/Run/AGW 管线；持久历史不重复注入；旧审批、迟到回调与跨 owner 请求拒绝。附件和工具能力按握手及入口授权校验，不以 Native 能力推断。
- 排队超时须区分 busy/not_started 与连接丢失；超过请求体额度须在写入前拒绝，保留可重试会话；原始附件限额不因 base64 传输额度放大而扩大。

详细设计见 [Runtime 边界](Runtime模块边界.md)、[API 与协议](API与协议.md)、[HITL](HITL协议.md)、[引用协议](引用协议.md)。

## 自动化入口

在本仓库使用已有 Go 工具链：

```bash
go test ./internal/contracts/agentengine ./internal/agentengine ./internal/runtime/acp
go test ./internal/runtime/query ./internal/server ./internal/interaction ./internal/connectorops
```

涉及并发/生命周期时针对变更用例运行 `-race`。`internal/runtime/acp/product_test.go` 是显式真实产品入口，依赖外部交付物和已授权认证；先阅读文件约定。常规测试或 fixture 通过不能证明真实模型、Desktop 壳、Windows 原生或已安装二进制通过。

## 必需手工门禁

| 场景 | 通过条件 |
| --- | --- |
| 安装与信任 | 未授权、旧 epoch、摘要/路径/平台/Node 不匹配拒绝；同步失败可按原修订对账，不发布半包 |
| 多会话容量 | 超过四个 Chat 的正常切换、配置/审批/取消隔离；安全回收后记忆恢复；全部受保护时明确 busy，不杀其他 Run |
| 活动生命周期 | 两插件同时活动时更新、禁用、撤销只影响目标准入；完成/取消后排空重试成功，旧快照无引用后回收 |
| 强杀及后台 | 在确认测试 PID 的 Platform/Adapter 强杀后，所属后代与租约清理；唯一终态、迟到提交拒绝；显式下一轮恢复随机记忆 |
| 配置、图片及连接器 | 预览无副作用，运行中只读配置；图片+技能按能力进入模型；挂载与凭据边界保持，引擎审批不重复；生成文件链接不越界 |
| 认证与跨版本 | 凭据失效/恢复、取消/超时可显式重试；固定版本对实际恢复原 session、模型配置及随机记忆，不能以新建空会话代替 |
| standalone / Linux / Windows | 分别记录真实系统、Node、插件、CLI 与认证组合；不能继承 Desktop/macOS 或交叉编译结果 |

截至 2026-11-06，历史 macOS 有限组合已有真实生命周期证据，后续共享连接、容量、预览、图片与链接修复的 fixture/race 通过不能替代重新安装候选后的三插件产品验证。真实跨 Node 应用覆盖升级、Windows 原生、第三引擎完整交付及最终市场发行仍需独立记录；Kimi 依赖本机 CLI 的候选不等于完整离线交付。

## 结果记录

每行一个用例×系统架构×插件/CLI/Node 版本组合，记录候选源码修订、包/二进制摘要、前置配置、步骤、预期、实际、状态和脱敏证据。状态用通过/失败/阻塞/不适用；未执行保持待实测，跳过不能写通过。私有 `build/qa` 日志不是长期凭证，提交可共享的摘要记录并标明可获取的证据位置，不提交 Token、OAuth code、个人配置、会话正文或凭据副本。

## 轻量 Adapter / 本机 CLI（2026-11-06）

Codex/Claude 0.1.1 不交付 CLI，使用用户在执行宿主上安装及认证的本机程序。Platform 继续精确校验宿主 Node；在受管运行时之后保留绝对用户 PATH，不继承相对目录、Token 或 NODE_OPTIONS 等变量。Adapter initialize 的 -32603/dependency_missing 映射为既有稳定错误码，不能误报为 auth_required；安装 CLI 后显式 Prepare 重试。CLI 配置/认证/会话由其本地实现管理，原插件 profile 仍保留但两插件不再用其认证投影。

自动化覆盖 PATH 边界及缺依赖后的重试；新版正式 Desktop/standalone、多轮/审批/恢复/强杀与 OS/CLI 版本矩阵须独立复验，旧随包 CLI 的证据不能替代。上游程序版本不受插件快照固定，用户升级 CLI 后可能改变协议能力。

安装预检补充（0.1.1）：只对标准 JSON-RPC 内明确的 -32603/dependency_missing 允许登记，CLI 后装；真实 Prepare 仍要求正确 ACP v1 握手。任意报错、退出、畸形数据或错误协议版本仍拒绝。该流程不伪造成功握手，缺 CLI 的用户可先安装插件。

## ACP 配置加载与切换延迟（2026-11-06）

现场证据：进程 4/4 时，新聊天预览反复 409；一次 Prepare 等待 15.3 秒失败，而驻留 Prepare 多为 0–1ms。对本机 Codex 0.164.0 的隔离测量只执行 initialize/session/new/session/close，无 Prompt：初始化约 371ms，同连接两次 new 约 25.2s/29.9s。逐请求测量 account/read 约 11.3s，thread/start 约 5.2s，model/list 约 5.0s；独立子进程使用 127.0.0.1:7890 后整次 new 约 14.8s，属于当时实测而非性能保证，系统/用户配置未修改。

修复沿用 Sessions/ConfigPreview：真实新会话成功后复用其初始默认选项，写入前复制，随后 Configure/恢复选择不得污染默认值；同引擎/Workspace/Agent 定义/目录代次/认证代次仍隔离。TTL 仅控制刷新，容量内保留旧值用于后台刷新期间展示，超过 128 项只逐出最旧项。首条消息确认现有凭据不清缓存，认证失败/显式登录、目录及定义变化仍使旧键不可用。UI 切换先读既有配置快照；Prepare/Query 的真实准入及容量/后台任务保护不放宽。

验收条件：满容量下已发现的默认选项无需新进程即可读取，其他 Chat 的 Model-B 不替代默认 Model-A；跨 30 秒并切换项目仍可读旧默认值；升级/凭据失效不能返回旧选项；发送始终校验真实准备/配置版本；无缓存且持续 busy 的前端停止无限重试。正式主壳及真实模型/多轮仍待实测，不能把本轮无模型握手当作完整产品验收。

本轮验证：ACP 全包通过，缓存/多会话/认证定向 race 通过；跨 30 秒公共接口回归、真实认证撤销后的缓存清空通过。上述 CLI 耗时来自实际已安装 Adapter；完整修复仍需更新 Platform/WebClient 后在正式主壳复验。
