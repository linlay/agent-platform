# Runtime 模块边界

## 定位

`internal/runtime` 是 transport-neutral 的应用运行时入口。HTTP、WebSocket、automation 和 run tools 可以使用不同的适配方式，但必须共享同一套 Query/Run 生命周期语义。

```text
HTTP / WebSocket ─┐
automation ───────┼─> runtime.Service ─> query ─> runstate
run tools ────────┘                    ├> runexec
                                      ├> orchestration
                                      └> proxy
```

`internal/server` 只负责外部协议解码、认证身份映射、HTTP/WS 错误响应、SSE heartbeat/flush 和事件本地化。`app.New` 负责创建 Runtime、RunState、Proxy、Terminal、Conversation、AdminSource、ChatResource 与 Project 服务并完成依赖注入。

## 子包职责

- `runtime/types`：`Caller`、`QueryCommand`、`RunRef`、`RunHandle`、控制命令、查询结果和订阅等内部值对象；不携带 `http.Request`、`ws.Conn` 或 `api.*` DTO。
- `runtime/query`：普通 Query、旁聊和独立根 Run 的准入、Agent/Team 快照租约、注册、Native 启动与阻塞执行、控制命令、continuation 仲裁和重启 awaiting 对账；持有这些业务实现，不回调 Server 的 Native 方法。
- `runtime/session`：构造执行 Session，冻结模型、工具、权限、技能与连接器、路径、环境、历史及 system-init；由 App 创建，Native 根 Run、子 Agent/Team 与 compact 共用。
- `runtime/catalogview`：Agent/Team 租约及 Team 可执行快照解析；`runtime/reference`：输入引用校验与物化，远端文件请求限于该非核心 I/O 包。
- `runtime/runstate`：活动 Run 注册、Chat 独占、observer/backlog、attach/detach、控制状态、compact barrier、Run 环境销毁、快照查询与恢复等待项的内存登记。
- `runtime/runexec`：Native Query 共用执行核心 `Execute` 及 `StartNative` / `ExecuteNative` 生命周期驱动，负责模型流、编排、事件消费、StepLine、模型轮次提交/丢弃、usage/cost、完成落盘、freeze/交付确认与 continuation；共用 fullText 汇总和时间契约错误识别。`ProxyExecutor` 单独负责受管根 Proxy 的启动、完成落盘、freeze/交付确认及结束通知；Proxy recorder 与 usage tracker 同归此包，Native 执行路径不变。
- `runtime/orchestration`：实际执行 `agent_invoke` 与 Team 调度，构造子调用、继承上下文、合并 HITL、路由事件并回注结果；Session/system-init 端口在 Native 驱动中绑定到 `runtime/session.Builder`，不绑定 Server。
- `runtime/proxy`：上游 HTTP/WS 协议值对象、Reference 物化规则、事件/usage 映射、活动 Proxy route、子任务 SSE 驱动和 submit/steer/interrupt 控制客户端；`Driver` 承接受管根 Proxy 的上游 SSE、独立 WS 与 inbound channel 驱动。channel 仅通过窄接口借用既有连接，单次 Run 清理请求订阅，不关闭共享连接。独立 WS 随 Run context 取消关闭，以解除静默上游的阻塞读取。
- `runtime/acp`（ACP-005）：`Supervisor` 从受信目录取得快照租约，使用受管 Node、固定入口/argv 和冻结 cwd 启动持续 stdio 进程。App 通过 ACP-006 的 `Sessions` 负责创建、关闭；Run 分派已由 ACP-007 接入，外部权限裁决仍归 ACP-008。复用 ACP Go SDK v0.13.5 的请求关联和通知顺序，仅在传输边界保留原始反向 request ID/params、连接代次并限制输入。

## 调用与依赖规则

ACP-001 已新增叶子包 `contracts/agentengine`，提供普通 Go struct、显式边界校验及共享测试样例，不内嵌专属 Schema 或引入代码生成；它不依赖 Server/Runtime，不负责 HTTP 认证或执行准入。开放上游参数和联合类型保留 raw JSON，避免丢失外部 session/tool 关联。精确契约见本仓库 `internal/contracts/agentengine/` 的类型、显式校验和测试；验收门禁见 [ACP 引擎验收](ACP引擎验收.md)。

ACP-003 已新增 `internal/agentengine`，负责受信 bootstrap、动态目录、批次修订及快照引用/候选租约；它不依赖 HTTP/WS、Server 或具体 Agent mode，不创建 ACP 会话。`internal/server/handler_agent_engines.go` 保留专用管理 HTTP 鉴权与响应映射，`app.New` 装配并通过已有 NotificationSink 通知目录失效。`internal/runtimefs` 从 connector 提取摘要、树复制、原子发布和 OS 锁/回收原语；连接器和引擎的业务记录与锁命名空间独立，没有通用插件注册框架。

目录提交与缓存是不同边界：启动不加载旧快照授权；第一次完整同步前拒绝候选执行。导入和回收由 assembly 锁协调，当前条目和候选执行各持共享版本锁；撤销/更新只切换目录，不覆盖旧版本。新 Runtime 执行器必须使用 Acquire 返回的候选，并在整棵进程树结束后 Release，不能由 Chat 永久持有。ACP-005 已实现进程监督与整树释放；会话准备/恢复已由 ACP-006 接入，ACP-014 已通过租约退休通知与会话操作锁实现更新/禁用/撤销排空；历史 Bridge 路径在 ACP-015 清除，不能作为新目录失败时的回退。



ACP driver、目录、快照、启动授权及后端统一分派已接入当前链路。依赖方向是 Server 解码鉴权 → Runtime 准入/驱动 → contracts；程序路径不能来自普通业务 DTO，取消/已有等待项按冻结 Run 归属分派，不借用新的执行准入状态阻断收尾。

1. `runtime/**` 不得 import `internal/server`。
2. `runtime/types`、`query`、`session`、`reference`、`runexec`、`orchestration`、`proxy` 不得 import `internal/api`。公共输入叶子值归入 `contracts/queryinput`，API 保留类型别名与原 JSON 契约。
3. `query`、`session`、`runexec`、`orchestration` 不得 import `net/http` 或 `internal/ws`。
4. Server 不得直接 import `internal/llm`、`internal/tools` 或具体 Agent mode 包。
5. Server 通过 `QueryRuntime` 接口进入应用运行时，Session 由 App 注入；生产 Runtime 的创建和绑定只发生在 `app.New`。测试 fixture 显式组装同一套服务，没有 Server fallback Runtime。
6. automation 与 `runops` 直接调用 Runtime，不允许借用 Server 业务门面。

上述规则由 `internal/architecture/boundaries_test.go` 使用标准库 AST 检查；另检查 Server 不得重新声明 Native 准入、Session、执行和恢复业务方法，query.Dependencies 不得恢复为 Server 函数回调表。

## 生命周期时序

`runtime/runstate.Manager` 是生产与集成测试唯一的 RunManager 实现，统一由 `runstate.NewManager()` 创建；同一运行环境的 Query、控制接口与客户端目标绑定共享该实例。`contracts` 保留 `RunManager` 等接口、`RunControl`、错误与不透明 `CompactControlHandle`，依赖方向固定为 `runstate -> contracts`，不提供旧 Manager 的别名或转发构造函数。

Manager 行为测试位于 `runtime/runstate` 同包，回收测试可以直接调整私有时间状态并调用回收逻辑，不为测试扩展生产接口。`contracts` 中的纯 RunControl 和 owner 契约测试继续保留。Server fixture 固定持有 `*runstate.Manager`；模拟重启时显式注入新 Manager，幂等取消持久化未完成 Run 并保留原始开始时间，不注册历史 awaiting，也不复用旧实例中的 Run、claim 或 compact 状态。

等待项协调统一使用 `runstate.NewDeferredAwaitingStore()` 创建的内存存储。`app.New` 创建后注入 Query 服务。Server 测试 fixture 也注入同一存储。存储持有等待记录、supervisor 取消函数和按等待项串行的 resolution coordinator；Chat 持久化读取、当前进程 continuation 仲裁与重启终态补写全部由 `runtime/query` 负责，App 绑定 Runtime 后调用 `Reconcile`。

异步 Query 的稳定时序是：

```text
decode/auth -> Runtime.StartQuery -> Runtime.AttachRun -> transport forward
            -> executor persist terminal -> EventBus freeze
            -> subscription acknowledges delivery -> RunState finish
```

客户端断开只关闭本订阅，不中断 Run。Proxy 上游序号可不从 1 开始：其 EventBus 显式允许在尚未淘汰任何事件时从 0 回放，后接入者不必赶在首事件前订阅；发生实际淘汰后仍严格返回 replay window exceeded，Native 与恢复 Run 不启用此例外。EventBus freeze 时订阅必须确认 delivery done，否则终态注销、Chat admission 和下一轮 Query 都可能被阻塞。SSE 与 WS 现在复用 Runtime 的相同订阅语义。

Native LLM stream 在工具执行中收到取消后，先完成工具结果收尾，再交付终态或 context 错误；运行中的异步工具共享 2 秒收尾期限，结果不可确认时保留明确的未知副作用失败记录。`runexec` 继续通过 Mapper/Processor/StepWriter 持久化这些普通工具结果，终态前 flush；worker 不直接操作 Chat store 或 EventBus。持久层失败、进程崩溃或强制终止不在此内存收尾保证内，历史读取仍对无结果的有效调用 fail closed。

订阅关闭必须同时执行 detach 和 `Observer.MarkDone()`：freeze 已从 EventBus 移除 live observer 后，单独 detach 无法再找到它并确认交付完成。Manager 的 `Finish` 不接管 EventBus 的冻结等待，执行端仍先完成终态持久化与交付确认，再释放 Run 的活动状态。注册后、执行器接管前的失败由 `query` 注册边界冻结并排空订阅，清理 connector pin/snapshot 后释放 Run，避免已附着的观察者永久等待。

## 同步与异步 Query 的共用执行核心

HTTP 默认 SSE、WebSocket 启动的 Native Run、HTTP `stream:false`、Runtime 进程内阻塞调用和旧内部 Query 接口都调用 `runexec.Execute`。同步入口不再维护另一份 `Next/Map` 循环；子 Agent、Team 调度、阶段标记、awaiting、usage 聚合和 continuation 使用同一路径。子 Session 构建由共享 Session Builder 提供，Proxy 子任务 SSE 驱动位于 `runtime/proxy`。

正文、usage 和 finishReason 以执行器生成并交给持久化的 `RunCompletion` 为唯一结果来源。内部 `QueryResult.Persisted` 直接透传执行器是否成功保存完成记录；Completion 在保存失败时仍可用于诊断，语音必须同时验证 Persisted 与 complete，不新增公开完成事件。Native 与 Proxy 非流式响应都不再用 `queryEventCollector` 的计算结果补写它们；旧 collector 仅保留为测试兼容。`fullText` 单独观察经过 Processor 的 normalized 内部事件，保留重试丢弃信号，不从公共 EventBus 反推模型轮次。StepWriter 在最终完成记录之前 flush，以保留最后一个阶段的模型信息。

等待方式由入口决定：SSE/WS 断线只解除订阅，Run 可继续并通过 attach 重新订阅；Native 阻塞调用保留原有执行与 RunControl context。Proxy 阻塞调用直接等待完成结果，不创建前台 observer，调用方 context 取消不终止已注册 Run；显式 interrupt、服务关闭和 Run lifetime 仍可终止执行。执行结束后先冻结并排空事件，再注销 Run、释放准入；只有成功持久化完成的 Run 才尝试启动 continuation。

Runtime 的 Native 与 Proxy 阻塞调用直接取得执行结果，不再模拟 HTTP 请求或解析自身输出的 SSE。旧内部 Query 的 status/body 与 SSE 回调编码器只保留在 Server 测试文件中，测试经实际 Runtime 启动/订阅或阻塞入口执行。Proxy 的异步启动、HTTP JSON 和 automation 阻塞调用使用同一个 `ProxyExecutor`，后台持续接收上游 SSE/WS 并记账；不会等待前台 observer 才开始接收。完成落盘后直接交付阻塞结果，再按既有规则 freeze/确认订阅交付和释放 Run。Proxy 不合并进 Native 模型执行核心。此次调整不改变 JSONL、数据库 schema、assembler/mapper 渲染与缓冲规则、事件字段或序列。

## 相邻应用服务

- `voice.Service` 只持有当前媒体轮、一次 Runtime 调用及最多一份等待旧 Run 释放的定稿；复用 ExecuteQueryWithHooks/AttachRun/Interrupt 和完整 Completion。语音 WS 断开不取消 Run；应用关闭才通过 Runtime 中断本服务受理的 Run 并等待自身 goroutine 退出。新 Chat 的主要聊天设置沿普通 Primary:true 路径；内部 PrimaryIfMissing 复用 BackfillPrimaryChat 的 CAS，保护 hello 后并发发布的 primary。

- `conversation.Service` 承担批量 Archive/Restore、active-run gate、Archive list/search/load/delete；`chat` 仍是持久化事实源。
- `adminsource.Service` 承担 source mutation 锁以及 Agent ZIP 的 staging、reload 校验、commit、rollback 和恢复 reload 事务。
- `chatresource.Service` 承担资源解析、图片 commit 的 Chat owner 校验与文档持久化。
- `project.Service` 由 app 创建，承担 Workspace tree/changes/diff；Server 只映射 HTTP 参数和错误。
- `modelclient` 承担 Provider HTTP、首响应超时、响应生命周期和 provider 错误分类。

## 保留的协议与旧契约适配

Native admission/session、根 Run 执行/恢复及子 Agent/Team 编排由 Runtime 负责，`app.New` 不再把 `srv.StartQueryRuntime`、`srv.ExecuteQuery`、`srv.SubmitRuntime` 等回注到 Query。

两处适配仍明确保留：

- `runtime/adapter` 只转换旧 `contracts.AgentEngine` / system-init / catalog 的 DTO。Core 接受 `types.QueryCommand`；旧执行器仍接受 `api.QueryRequest`，转换集中在适配包，不持有准入、恢复或生命周期。
- 根 Proxy 仍经 `query.ProxyPort` 接入；受管 Run 的上游 SSE/WS/channel 驱动已移到 `runtime/proxy.Driver`，公共收尾及 recorder/usage 已移到 `runtime/runexec`。Server 保留通用 HTTP/SSE 响应、channel socket 适配、路由配置和控制转发；所有根 Proxy 调用均通过 Runtime prepared registration，旧未注册阻塞 SSE 入口与模拟 HTTP 结果捕获已删除。路由/控制适配仍保留，不能据此宣称 ProxyPort 已完全移除；Native 路径不进入 ProxyPort。

HTTP/WS 保留外部请求解码、认证、来源/transport/device/lane 校验与错误编码。Runtime 保留 Agent/Team owner、输入能力、等待项身份和权限级别校验。Submit 仍允许跨设备及 HTTP/WS；其他控制仍校验进程内 control scope。语音创建的 Run 冻结事实上的 voice transport 与主体；相同 subject 的普通 HTTP/main WS 界面可观察和控制，BTW/explain 与其他 subject 拒绝。HTTP/WS 原有 transport/device/lane 边界不放宽，Submit 继续原本的跨 transport/设备规则。未改变外部路由、SSE/WS 字段、JSONL/SQLite schema、模型协议或工具取消收尾策略。

## 集成注意事项

- 原 `server/session_builder.go`、Session 上下文 helper 和 Server fixture 已迁移到 `runtime/session` 及显式组装的测试适配，不恢复旧 Server 业务文件。
- Query/Session 的重新装配不改变 Memory 服务边界。
- 旧 `contextConfig.agents` 与 agents 标签只由 Catalog 生成非阻断弃用诊断，Runtime 不再构建候选。历史 Chat 读取不依赖当前 Agent 可执行；Query 准入仍区分 `422 agent_configuration_invalid` 与 `404 agent_not_found`。
- Windows 验证使用 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...`，不等同于 Windows 原生运行测试。

WebApp SDK 作为受信宿主适配器调用同一 Runtime，不把应用身份或管理存储加入核心模型。`ExecuteCommand.OnPrepared` 是内部准入钩子：准备完成后、注册和执行前同步预留新 Run/Chat 的资源归属；钩子失败释放准备租约并拒绝执行，公共请求不能指定该钩子。WebApp 的授权、限额、事件投影及持久化归属由 `internal/webappbridge` 维护。其授权运行使用 owner 的 HTTP 控制身份，不伪造设备或 WebSocket lane；普通 owner 控制入口仍遵循既有 Run 规则。

## ACP stdio 监督器阶段边界

当前默认上限为 4 个进程、2 个活动 prompt、8 个排队 prompt（等待 5 秒）、每 Chat 1 个 prompt；每连接最多 8 个在途调用、8 个反向请求，调用期限 10 分钟。入站帧与非 prompt 出站帧 1 MiB；`session/prompt` 出站帧 64 MiB（覆盖既有 10 MiB 附件的 base64/JSON 转义及消息、技能正文），超过额度在写入前返回既有 `resource_reference_too_large` / `not_started`，不关闭共享进程、不消耗会话上下文。通知队列最多 32 项且合计 4 MiB，stderr 私有尾部最多 64 KiB，对外仅暴露字节计数。它们是受控测试下的保守值，发布容量预算仍需实测；当前安全空闲/LRU 回收按下述会话与连接复用规则执行。

排队拒绝/超时标记 `not_started`；已发请求失败保守标记 `unknown`，不重放。`session/cancel` 通知可越过活动 prompt 准入，轮次取消先发 session/cancel，等待真实 prompt 响应，2 秒未收尾则回收进程树。超时/坏帧/EOF/关闭会取消当前连接和回调并回收进程树；新连接有新代次。处理器必须响应 context 取消，不能将旧代次交互写入新会话。

`processgroup.Start/Tree.Wait` 管理 macOS 进程组及 Windows Job Object；仅确认整个树退出后释放版本租约与容量，无法确认则保留并报错。这是受信程序的生命周期管理，不是防止恶意进程逃逸的安全沙箱。macOS guardian 使用既有 x/sys 的内核进程信息，持续按 PID+启动时间记录可观察的后代，覆盖 CLI 为后台 Shell 创建的独立进程组；收尾前冻结已知父进程并重新收集子进程，逐个复核身份后终止，确认退出才释放租约，检查失败保留锁。它不依赖进程名或命令行匹配，不承诺捕获尚未观察就完成双重派生/脱离父树的进程；后台任务仍需按固定 CLI 组合实测。Windows 继续使用 Job Object，其他 Unix 保留进程组边界。后台、强杀和跨平台的验收条件见 [ACP 引擎验收](ACP引擎验收.md)；每个固定 CLI/系统组合分别记录，不将旧 HTTP Bridge 接入此路径。

受信引擎交付要求 Platform Agent Engine API 2：`environment` 只创建稳定的私有 profile 根并通过 `ZENMIND_AGENT_PROFILE` 传给插件，逐层拒绝链接及非私有目录，不再创建产品子目录或注入厂商变量。插件入口负责自己的配置映射；新旧宿主/包由既有 requires 校验拒绝混用，防止旧入口回退个人配置。HOME 不改，profile 身份不随版本或 Chat 改变。受信 Manifest 的 launch.runtime 支持 node/executable：前者使用声明版本的受管 Node，后者直接执行包内程序（macOS 执行位、Windows .exe），共用 Supervisor、认证、进程树与版本租约。宿主不读取 npm/JS SDK 元数据；bundled 文件由受信摘要固定，adapter 仅为可选来源说明。入口和参数只能来自受信 Manifest，不接受普通请求指定系统程序；不新增语言解释器发现或 shell 执行框架。

## ACP 会话准备与恢复（ACP-006）

`runtime/acp.Sessions` 以真实 Chat 为单位串行准备、配置和 prompt，生命周期复用 Supervisor。Server 只解码、校验现有 Chat 所有权并映射响应；Runtime 根据 Chat 的 Agent 解析 engineId 和既有 Workspace，不接受客户端提供命令、路径或外部 session ID。读取目录/Inspect 不启动进程，Prepare 返回 connection generation 对应的 preparationId 及 configRevision。配置、prompt 之前检查目录启用状态与绑定身份；撤销阻止新操作，显式 CloseChat 仍可收尾。

`chat.FileStore` 将冻结 SessionBinding 写入 Chat 目录的 `.agent-engine-session.json`，使用既有原子替换；不扩大通用 Store 接口、不改 SQLite 索引。绑定随目录归档/恢复，derive 明确拒绝外部会话，避免共享 CLI session ID。engine/workspace/profile 不一致、损坏绑定或有 Run 无绑定均拒绝恢复。lastUsed 的版本/摘要只作诊断，活进程才持有快照租约；CloseChat 保留绑定，后续恢复解析当前启用快照。

prepare 协商 initialize，再按声明选择 resume/load，只有无 prompt 的 prepared 丢失可新建。发 prompt 前先保存 submitted，确认未发送可回退，合法轮次完成后保存 persisted；断连/不合法响应保留不确定状态，不重放。恢复时丢弃历史正文和工具回放，只保留当前配置、命令、mode。反向请求必须匹配 session/连接代次及本轮 handler；Run/HITL 权限由现有 Query/RunControl 及下述外部交互适配承接。

`config.go` 将上游 select（包括分组）转换为既有 ExternalConfigOption，保持大小写与不透明 ID/value。配置响应确认后递增 revision；动态更新使旧 revision 失效，确认的默认值也保存并在恢复时重设。仅开放已验证的 model/mode/thought_level 语义类别与 select 类型；model_config、服务档位、boolean 和未知类别关闭，不按产品选项 ID 分派；未知值拒绝；不读取 Native 模型注册表。新 engineId 项目在 Native Session Builder 明确拒绝，正式 Query 分派已通过 runtime/query.Service 接通。

历史固定 macOS 组合已有多轮、HITL、重启与有限跨版本恢复验证；当前共享连接、配置预览及安全回收按下述新设计执行，不沿用早期“没有真实 prompt”或“始终关闭空闲回收”的阶段结论。真实产品覆盖与待实测组合见 [ACP 引擎验收](ACP引擎验收.md)，不能由 fixture 通过推断已安装发行候选通过。

## ACP 正式 Query 执行（ACP-007）

`catalog.ExecutionBackend` 明确区分 native/acp/proxy/channel。`query.Service` 负责入口能力/目录准入和外部准备，作为既有 Engine 接口将 ACP 交给 `Sessions.Stream`，其余本地执行仍委托 Native Engine；Proxy/Channel 保持既有驱动。公共执行器复用现有 Run、Coordinator、DeltaMapper、StepWriter 和收尾，不另建 ACP Run 框架。

`QuerySession.ExternalSession` 只在进程内冻结 preparation/config revision。root 会话复用 Chat 绑定；受控子任务使用临时独立 scope，结束时关闭且不保存父绑定。恢复不将上游历史重复送入 Run。外部模型选项保留大小写，不读取 Native model registry；未确认的 editing/权限不套用 Native 默认。

文本、工具、文件和计划转换成既有事件；工具 acp/ 名称仅为不透明工具标识。计划使用已有 taskId/description/status 数组，pending 映射 init，不增加 source 字段；根会话复用 plantasks 快照恢复，子任务计划沿用任务内实时投影。完整且非负的本轮 token 报告转换为现有 usage.current/run 及历史用量；缺失/部分统计、会话累计量和货币成本不冒充本轮计费，不增加 source/scope/known/values 旁路。

当前 execute、BTW/Explain、chat/site/selection 引用及未验证的控制项均前置拒绝；file/skill/kbase 的普通 query 走下述 ACP 输入转换；缺目录或宿主不创建 Chat/Run。Automation 允许预认证运行，缺登录或需要反向交互明确失败，不自动批准。steer 与运行中权限变更关闭；外部 HITL/软取消已由 ACP-008 接入，见下节。真实 Adapter/UI/Windows 与附件组合按 [ACP 引擎验收](ACP引擎验收.md) 单列复验。

ACP 工具详情沿用既有 summary/full 展示规则；默认 summary 隐藏普通工具，项目可用 `interactionConfig.defaultDetail: full` 展示已持久化调用。失败工具也必须保留最新 rawInput、rawOutput 与 content：适配层将 Structured 同步序列化到 ToolExecutionResult.Output，供既有失败结果管线生成 output/error；不修改 AGW 或共享工具结果语义。不根据正文猜测 commentary/final，也不把厂商私有元数据提升为公共字段。


## ACP 外部交互与运行收尾（ACP-008）

权限请求由 `runtime/acp/hitl.go` 转换为既有 `mode=approval`，使用 `approvals[{id,description,command,options,allowFreeText}]`，仅提供 approve/reject。前端提交 `params:[{id,decision:"approve"或"reject"}]`；`params:[]` 取消。精确原始 optionId 只留在适配器本次调用闭包，approve→allow_once，reject→reject_once；没有 reject_once 时返回 ACP cancelled。缺少 allow_once、存在多个同类一次性选项则 capability_unsupported，不能静默选择、持续授权或扩大为 approve_rule_run。描述保留上游范围名称、目标路径；allow_once 不保证只有一次工具调用。

elicitation 转为既有 `mode=question`；仅支持可准确表达的必填文本/单选，枚举 ID 转为现有 options.value，前端不读取 optionId、required 或 ACP schema。提交 `params:[{id,answer}]`，按原字段/枚举值校验；`params:[]` 取消整批。可选字段、数字、多选、URL、秘密输入和复杂约束返回 elicitation_unsupported，不能为了插件增加 AGW 字段。

所有权来自认证上下文。原始 RPC、连接生命周期、选项映射及期限保留在 ACP 层；公共 AwaitingSubmitContext 只持有不序列化的 ValidateSubmit 回调，RunControl 负责锁内复核、一次受理、正文冻结及幂等。跨 owner/Chat/Run、伪造决定、过期、同 submitId 异正文均拒绝；accepted 仅表示宿主受理，不能确认上游执行，断连不补发。重启依据会话绑定及已有子任务执行者身份收尾，不依赖 source=acp；不伪造 Native 工具结果。

取消前的排队仍使用请求上下文。已发送 prompt 使用有界收尾：发送 session/cancel，最多等待 2 秒；真实响应后才可复用，超时回收整棵进程树并保留不确定状态。detach 只移除订阅；Automation/无控制上下文遇到交互报 interaction_required，缺登录报 auth_required。未验证的 planning/accessLevel/steer 映射继续关闭，真实 select 配置沿用 ACP-006 上游确认流程。真实 CLI/UI/Windows 按 [ACP 引擎验收](ACP引擎验收.md) 逐组合验收。


## 外部认证与旧链路清理

runtime/acp 的 Sessions 管理认证操作、受信 helper、同 profile/代理、新进程认证复核及取消；agent 类型按 Manifest 的 agentMethods 与上游交互类型联合准入，methodId 仅作不透明关联，不再硬编码 chat-gpt。terminal 继续匹配 Manifest methodId/argv；未知方法不默认启用。复用 App 注入的 Terminal Manager。Server 仅映射已验证身份、解码冻结 DTO 和错误响应；Terminal WS 只允许附着当前身份的 auth 操作，不接受客户端启动命令。用户切换 Chat 取消未完成登录；宿主期限与关闭负责兜底回收。

普通会话准备只确认配置与会话可用，不能把 session/new 成功当作已认证：Claude 可延迟至 prompt 才检查凭据。初始认证状态保持 unknown，显式认证流程成功或非取消的有效 prompt 完成后才确认 authenticated；auth_required 仍撤销当前进程并禁止复用。取消不会凭空证明认证成功，也不自动重发原 prompt。

本地凭据可由 CLI/凭据管理器在宿主外恢复，因此下一次 Prepare 不能被历史 required 状态永久拦截；仍阻止正在 authenticating 的并发准备。新的进程成功准备后将旧 required 清为 unknown，不声称 authenticated；无效凭据仍由上游拒绝并收尾。此行为适用于所有引擎，不在 Platform 增加 Codex/cc-switch 分支，也不自动重发失败消息。

旧 ACP 配置、注册、HTTP 模型发现及到 Proxy 的转发已经删除。普通 Proxy/Channel 和 Native 会话保持既有边界，外部模型与配置由 prepared ACP session 确认。删除字段只用于拒绝输入，无迁移、别名或回退。

## ACP 更新与活动生命周期（ACP-014）

目录模块保存当前引用及进程租约；监督器装配候选协议验证回调，启用/切换内容前取得独立候选锁，复用同一启动/stdio/进程树逻辑进行 10 秒 initialize 检查并退出。不调用 authenticate、session/new 或 prompt。候选未通过保持原修订和当前版本；验证回调不能重入目录锁。候选受原进程容量限制，容量不足时保留当前版本等待重试。

每个发布候选的退休信号随更新、禁用、撤销关闭，相同内容和启用状态的重复对账不触发退休。Sessions 等待会话操作结束后关闭旧进程，覆盖 prompt 与等待审批；空闲进程立即关闭，下次 prepare 选择当前版本恢复。恢复失败保留原持久绑定。会话准备先检查目录授权，再获取操作锁；禁用/撤销不会因旧进程正在排空而误报 engine_busy。版本退役按共享连接的所有会话排空；普通空闲回收遵循下述独立安全条件。

目录按真实租约显示 busy/draining/stopped，取得/释放时复用目录失效通知；busy 表示进程占用，不承诺已有可发送的 prepared session。撤销 ACK 的 drainingEngineIds 与授权撤销分别表达；整树退出才能释放锁，失败则继续保留锁和容量。Desktop 必须等待排空才确认卸载。Chat/profile 始终与程序回收分离。

受控 macOS Node 的 SIGKILL、活动更新/禁用/撤销、双版本恢复/拒绝与 GC 已验证。真实 Codex 的 HTTP 产品链路已验证 Platform SIGKILL 后 guardian/Adapter/App Server 全部退出、旧 Run 单次收尾与迟到提交拒绝；显式下一轮模型记忆恢复且历史不重复。真实 Codex CLI 0.164→0.165 活动升级已验证旧 Run 排空、新版保持 session/profile/记忆和旧快照回收；不能扩大为所有 Adapter 版本兼容。Desktop 主壳已验证文件审批中强杀、旧 Run 唯一终态/旧审批失效、草稿保留及显式下一轮记忆；历史固定组合的后台/强杀/双活动隔离不代表所有版本通过；当前扩展跨版本、认证、后台、Node 和 Windows 组合按 [ACP 引擎验收](ACP引擎验收.md) 保留待实测项。

### Platform 强杀后的进程所有权

ACP 通过 `processgroup.StartGuarded` 启动进程。Unix 复用当前 Platform 可执行文件的内部 `__process-guardian` 入口，在正常应用初始化之前分派；不加载第二套 App、不启动 HTTP 服务。每个受管 Adapter 配一个随树生命周期退出的 guardian，Platform 仅持有私有存活管道写端。guardian 继承快照 flock，持有单独 Adapter 进程组；宿主强杀导致管道 EOF 后清理该组；macOS 同时清理按内核身份记录的 detached 后代，确认组和已记录后代全部消失才退出并关闭锁。新宿主 GC 因共享锁仍在而不能提前回收。原连接器与通用命令继续使用原 Start/Run，不扩展其进程行为。

guardian 启动 Adapter 后关闭自身 stdin/stdout/stderr 副本，不掩盖上游 EOF；避免孤立暂停进程组的 HUP 终止清理所有者。它不会把控制管道或租约描述符传给 Adapter。正常取消也是关闭私有管道，由同一清理路径收尾。guardian 异常退出不等于树清理成功，宿主保留租约与容量并返回失败。Windows 保持原有 kill-on-close Job Object，未增加 Unix guardian；原生 Windows 与“宿主和 guardian 同时被强杀”不在本次成功证据内。

内部入口由生产 main 和实际启动 ACP 的测试二进制显式分派，无包 init 副作用或独立守护服务。进程数上限仍按逻辑 Adapter 计数，guardian 的额外资源开销需纳入 T08 负载验收。

### ACP 独立部署管理

standalone 由 `agentengine.NewStandalone` 持有部署锁和安装状态，`agent-engine-manage` 经本机受限 HTTP 入口执行 ZIP 导入、启用、更新与撤销。安装事务复用既有 Manifest、受信交付、Apply、不可变快照与租约；App 在 ACP 候选校验器装配后 Restore，再发布本机管理凭据。Server 仍只做权限/请求/响应边界，Runtime/Query/AGW 不承担包管理。Desktop 不调用独立入口；浏览器没有安装权限。部署及实测边界见 [ACP 独立部署](ACP独立部署.md)。


### ACP 复用 Composer 上下文与工具（2026-11-04）

`session.PrepareExternalContext` 复用可信技能、知识库引用和连接器挂载解析，不调用 Native BuildQuerySession，不装配 Native 模型/历史/系统提示。`runtime/acp/input.go` 将上传资源转换为标准 ACP ContentBlock；`platform_tools.go` 提供随 ACP 会话释放关闭的带令牌 loopback MCP HTTP，`platform_tools` 每轮发现冻结工具，`platform_call` 调现有 ToolExecutor，`platform_connector` 经 connectorops 执行挂载 CLI（直接 argv、版本校验、连接器凭据注入）。这是 ACP 的反向工具通道，ACP 主链路仍是 stdio；不恢复旧固定端口 bridge，不修改 AGW。

只有活动 Run 可使用工具，现有 RunControl 承担审批、取消与收尾；业务工具要求本地文件预审时明确返回 capability_unsupported。工具回复遵循原 Output/Error 合同，内部 MCPResult、结构化诊断不发送给模型。App 仅给受理的 Agent 执行器注入 OneID 文件，普通网页应用 connectorops 不获得该身份。

ACP 的 CLI 连接器调用确认归外部引擎的 Mode；`platform_connector` 不再无条件追加一次人工审批，也不将引擎 Mode 映射为 Native 三档权限。引擎发出的标准 `session/request_permission` 仍通过现有 RunControl 展示并回传同意/拒绝。Platform 保留活动 Run、挂载、包指纹、凭据及 connectorops 执行授权检查；`platform_call` 的业务审阅和写工具审批不受此调整影响。

interactionConfig 开放既有附件/技能/连接器/知识库入口并保留显式关闭项；Native 模型、规划/编辑/终端与聊天引用仍不开放。图片/二进制和 MCP HTTP 在执行前后按实际协商检查，不声称任意 ACP 引擎都支持这些可选能力。前端与 Desktop 继续使用原菜单、上传、截图、选择和审批 UI。


### ACP 会话与连接复用（2026-11-04）

`Sessions` 负责 Chat 持久绑定和会话互斥，`engineWorker` 负责一次 initialize 的物理连接，`Supervisor` 继续持有进程树、版本租约和全局执行额度。同引擎/profile + 规范 Workspace 的连接只在标准能力声明 close 且 resume/load 可用时复用；受控子任务单独隔离。无这些能力时保持独立连接，不猜测厂商能力。版本退休与认证失效都阻止新 Chat 借用旧连接，活动兄弟会话继续排空。

`sessionId` 路由通知/反向请求，`preparationId` 为每个会话独立生成；同一连接的不同会话可以并发 Prompt，但仍受全局 active/queue 限制。同一会话仍串行。重复上游 sessionId 拒绝并关闭故障连接，不能把两个 Chat 绑定到同一上下文。每个会话拥有独立 MCP 通道，释放会话即撤销其工具入口。

`runtime.yml.agent-engine` 提供 max-processes=4、max-sessions=8、max-active=2、max-queued=8、queue-timeout=5s、idle-timeout=5m，按现有启动配置装配，修改需重启 Platform。进程额度统计 Adapter，不代表插件内部 CLI/后代数量。Chat 历史数量不受该额度限制。请求日志区分会话互斥、准备队列满及无可回收容量；对外仍沿用 engine_busy 与现有 AGW。

额度不足按 LRU 释放安全空闲会话；定时扫描回收超过 idle-timeout 的安全会话，空连接再保温一个 idle-timeout，压力下可提前关闭。仅 prepared 或已 persisted 且可恢复的会话符合候选条件。活动 Prompt/配置/准备/审批、受控子任务、submitted 不确定上下文、未完成工具均保护。标准 read/edit/delete/move/search/fetch/think 工具完成后可回收；execute/other/未知工具或 terminal/create 可能遗留后台任务，保守固定，不把 end_turn 当作后台排空证明。此限制依赖可信插件正确声明标准工具语义，不能替不透明上游保证后台静默。

再次 Prepare 使用原 sessionId 的 resume/load 并恢复确认配置，不重复 Prompt，不把恢复失败静默转换为新会话。已有 Chat 在前端对话切换就绪后自动准备并获取配置，不再等待 Composer 聚焦；冷会话可能恢复进程，仍遵循宿主额度和安全回收规则。New Chat 先做下述配置预览，发送前才准备真实会话。发送前恢复若选项一致可继续，选项变化仍需重新确认。进程崩溃、无响应的控制请求或拒绝取消属于共享连接故障，会影响该连接上的会话；不能宣称进程内会话具有独立 OS 故障隔离。


ACP 全局 Prompt 排队只等待执行名额，不发送上游请求。队列等待预算耗尽返回既有 `engine_busy/not_started`；使用 `WithTimeoutCause` 区分容量超时和调用方取消，不把本地排队超时映射为 `rpc_connection_lost`。默认全局同时执行 2 个、排队等待 5 秒，跨插件共用额度；审批/工具等待中的未结束 Prompt 仍占名额。超时不关闭共享连接、不取消其他 Run、不自动重放；释放名额后用户显式重试。真正的连接故障保持原错误语义。


### ACP 产物 Markdown 文件地址（2026-11-05）

ACP 转换层按当前 QuerySession 冻结的 WorkspaceRoot，把普通 Markdown 链接/图片目的地址中确实存在、解析软链接后仍在 Workspace 内的相对文件转为既有 `@workspace/` 引用。复用流式 Markdown destination guard，仅给 ACP 装配地址转换；普通文本和代码不替换，目的地址跨 chunk 缓冲，在内容边界/收尾刷新。网页、显式引用、绝对路径、缺失文件、越界路径与越界软链接不改写；最终访问继续受既有文件接口约束。不新增 AGW 字段、不按 Codex/Claude/Kimi 分支，不改前端的 unscoped 拒绝规则。新输出与新持久历史一致；既有 JSONL 不做迁移或原地替换。


### ACP 新聊天配置预览（2026-11-06）

`Sessions.Preview` 复用既有 target 校验、prepare/worker 与标准 ACP configOptions 转换，返回独立的 ConfigPreview（仅 engineId/options，不含 preparationId、Chat 或授权）。GET 仅读内存；POST 在 30 秒去重窗口之外刷新，同作用域并发调用合并；缓存最多 128 项，按有效 Agent 定义、规范 Workspace、引擎、目录 generation/revision 和平台观察到的认证代次隔离，重启不持久化。插件更新/撤销或平台认证变更后旧键不可命中；外部配置文件变化不靠文件监视发现，下一次实际探测仍取上游结果。

全局至多一个后台探测；执行满额、有 Prompt 排队或真实会话准备时推迟准入。探测只能使用空闲连接/剩余额度，不借用有驻留会话的连接、不抢占、不触发用户会话 LRU 回收。同 Workspace 的正常连接已有驻留会话或 setup 正忙时，转向独立探测连接，由原宿主进程额度限制准入；不能仅因正常连接非空就判定宿主满额。临时会话不写 Chat 绑定、不调用 Prompt；关闭临时会话期间继续持有 worker setup 锁，成功关闭后才允许真实会话接入。具备 close + resume/load 的空连接按原策略保温，其余进程关闭。探测等待者取消不打断共享请求，宿主关闭与原请求预算限制其寿命。冷启动成本和无缓存时的等待无法由协议消除；这不是后台探测抢占式调度器。

WebClient New Chat 先读缓存再后台刷新，busy 只延后探测、不锁输入；选项暂存在当前草稿，发送前使用真实 Prepare + Configure 验证并应用。无效/被移除的用户选择阻止发送、要求确认，绝不静默切模型或放宽权限。已有 Chat 恢复、Query、RunControl 和 AGW 保持原链路。Desktop 复用同一 WebClient。
