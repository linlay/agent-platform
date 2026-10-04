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

## 调用与依赖规则

1. `runtime/**` 不得 import `internal/server`。
2. `runtime/types`、`query`、`session`、`reference`、`runexec`、`orchestration`、`proxy` 不得 import `internal/api`。公共输入叶子值归入 `contracts/queryinput`，API 保留类型别名与原 JSON 契约。
3. `query`、`session`、`runexec`、`orchestration` 不得 import `net/http` 或 `internal/ws`。
4. Server 不得直接 import `internal/llm`、`internal/tools` 或具体 Agent mode 包。
5. Server 通过 `QueryRuntime` 接口进入应用运行时，Session 由 App 注入；生产 Runtime 的创建和绑定只发生在 `app.New`。测试 fixture 显式组装同一套服务，没有 Server fallback Runtime。
6. automation 与 `runops` 直接调用 Runtime，不允许借用 Server 业务门面。

上述规则由 `internal/architecture/boundaries_test.go` 使用标准库 AST 检查；另检查 Server 不得重新声明 Native 准入、Session、执行和恢复业务方法，query.Dependencies 不得恢复为 Server 函数回调表。

## 生命周期时序

`runtime/runstate.Manager` 是生产与集成测试唯一的 RunManager 实现，统一由 `runstate.NewManager()` 创建；同一运行环境的 Query、控制接口与客户端目标绑定共享该实例。`contracts` 保留 `RunManager` 等接口、`RunControl`、错误与不透明 `CompactControlHandle`，依赖方向固定为 `runstate -> contracts`，不提供旧 Manager 的别名或转发构造函数。

Manager 行为测试位于 `runtime/runstate` 同包，回收测试可以直接调整私有时间状态并调用回收逻辑，不为测试扩展生产接口。`contracts` 中的纯 RunControl 和 owner 契约测试继续保留。Server fixture 固定持有 `*runstate.Manager`；模拟重启时显式注入新 Manager，只从持久化存储恢复 awaiting、原始开始时间和事件游标，不复用旧实例中的 Run、claim 或 compact 状态。

恢复等待项统一使用 `runstate.NewDeferredAwaitingStore()` 创建的内存存储。`app.New` 创建后注入 Query 服务。Server 测试 fixture 也注入同一存储。存储持有等待记录、supervisor 取消函数和按等待项串行的 resolution coordinator；Chat 持久化读取、恢复 claim、supervisor 与终态补写全部由 `runtime/query` 负责，App 绑定 Runtime 后调用 `Reconcile`。

异步 Query 的稳定时序是：

```text
decode/auth -> Runtime.StartQuery -> Runtime.AttachRun -> transport forward
            -> executor persist terminal -> EventBus freeze
            -> subscription acknowledges delivery -> RunState finish
```

客户端断开只关闭本订阅，不中断 Run。EventBus freeze 时订阅必须确认 delivery done，否则终态注销、Chat admission 和下一轮 Query 都可能被阻塞。SSE 与 WS 现在复用 Runtime 的相同订阅语义。

Native LLM stream 在工具执行中收到取消后，先完成工具结果收尾，再交付终态或 context 错误；运行中的异步工具共享 2 秒收尾期限，结果不可确认时保留明确的未知副作用失败记录。`runexec` 继续通过 Mapper/Processor/StepWriter 持久化这些普通工具结果，终态前 flush；worker 不直接操作 Chat store 或 EventBus。持久层失败、进程崩溃或强制终止不在此内存收尾保证内，历史读取仍对无结果的有效调用 fail closed。

订阅关闭必须同时执行 detach 和 `Observer.MarkDone()`：freeze 已从 EventBus 移除 live observer 后，单独 detach 无法再找到它并确认交付完成。Manager 的 `Finish` 不接管 EventBus 的冻结等待，执行端仍先完成终态持久化与交付确认，再释放 Run 的活动状态。

## 同步与异步 Query 的共用执行核心

HTTP 默认 SSE、WebSocket 启动的 Native Run、HTTP `stream:false`、Runtime 进程内阻塞调用和旧内部 Query 接口都调用 `runexec.Execute`。同步入口不再维护另一份 `Next/Map` 循环；子 Agent、Team 调度、阶段标记、awaiting、usage 聚合和 continuation 使用同一路径。子 Session 构建由共享 Session Builder 提供，Proxy 子任务 SSE 驱动位于 `runtime/proxy`。

正文、usage 和 finishReason 以执行器生成并交给持久化的 `RunCompletion` 为唯一结果来源。Native 非流式响应不再用 `queryEventCollector` 的计算结果补写它们；该 collector 仍服务于 Proxy 协议适配。`fullText` 单独观察经过 Processor 的 normalized 内部事件，保留重试丢弃信号，不从公共 EventBus 反推模型轮次。StepWriter 在最终完成记录之前 flush，以保留最后一个阶段的模型信息。

等待方式由入口决定：SSE/WS 断线只解除订阅，Run 可继续并通过 attach 重新订阅；阻塞调用在执行期间保持 observer，沿用原有 RunControl context，不把请求取消改成新的 Run 中断策略。执行结束后先冻结并排空事件，再注销 Run、释放准入；只有成功持久化完成的 Run 才尝试启动 continuation。

Runtime 的 Native 阻塞调用直接取得执行结果，不再生成 HTTP 请求或解析 SSE。旧内部 Query 的 status/body 与 SSE 回调编码器只保留在 Server 测试文件中，测试经实际 Runtime 启动/订阅或阻塞入口执行。Proxy 主 Run 保留现有 SSE/WS 驱动、完成记录捕获及非流式 collector，尚未统一到 Native 执行核心。此次调整不改变 JSONL、数据库 schema、assembler/mapper 渲染与缓冲规则、事件字段或序列。

## 相邻应用服务

- `conversation.Service` 承担批量 Archive/Restore、active-run gate、Archive list/search/load/delete；`chat` 仍是持久化事实源。
- `adminsource.Service` 承担 source mutation 锁以及 Agent ZIP 的 staging、reload 校验、commit、rollback 和恢复 reload 事务。
- `chatresource.Service` 承担资源解析、图片 commit 的 Chat owner 校验与文档持久化。
- `project.Service` 由 app 创建，承担 Workspace tree/changes/diff；Server 只映射 HTTP 参数和错误。
- `modelclient` 承担 Provider HTTP、首响应超时、响应生命周期和 provider 错误分类。

## 保留的协议与旧契约适配

R16 已移走 Native admission/session、根 Run 执行/恢复及子 Agent/Team 编排，`app.New` 不再把 `srv.StartQueryRuntime`、`srv.ExecuteQuery`、`srv.SubmitRuntime` 等回注到 Query。

两处适配仍明确保留：

- `runtime/adapter` 只转换旧 `contracts.AgentEngine` / system-init / catalog 的 DTO。Core 接受 `types.QueryCommand`；旧执行器仍接受 `api.QueryRequest`，转换集中在适配包，不持有准入、恢复或生命周期。
- 根 Proxy 仍经 `query.ProxyPort` 接入；受管 Run 的上游 SSE/WS/channel 驱动已移到 `runtime/proxy.Driver`，公共收尾及 recorder/usage 已移到 `runtime/runexec`。Server 保留 HTTP/SSE 响应、阻塞结果捕获、非流式 collector、channel socket 适配和控制转发；需要注册时仍复用 Runtime prepared registration。旧未注册阻塞 SSE 入口暂留 Server，不改变其注册和取消语义。R18 尚未完成，不能据此宣称 Proxy 已完全迁移；Native 路径不进入 ProxyPort。

HTTP/WS 保留外部请求解码、认证、来源/transport/device/lane 校验与错误编码。Runtime 保留 Agent/Team owner、输入能力、等待项身份和权限级别校验。Submit 仍允许跨设备及 HTTP/WS；其他控制仍校验持久化 control scope。未改变外部路由、SSE/WS 字段、JSONL/SQLite schema、模型协议或工具取消收尾策略。

## 集成注意事项

- R11/R12 的参数收敛已同步到迁移后的 Session、continuation、恢复和 Proxy Reference 实现。原 `server/session_builder.go`、Session 上下文 helper 和 Server fixture 已迁移到 `runtime/session` 及显式组装的测试适配，不恢复旧 Server 业务文件。
- R13–R15 的 Memory 去重保持现有实现；Query/Session 的重新装配不改变 Memory 服务边界。
- `contextConfig.agents` 的不可用候选继续由公共 catalog 解析器跳过并生成有界诊断，`runtime/session` 只记录警告；历史 Chat 读取不依赖当前 Agent 可执行。`runtime/query` 准入保留 `422 agent_configuration_invalid` / `404 agent_not_found` 的区分，HTTP/WS 保留结构化错误与本地化提示。
- Windows 验证使用 `GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build ./...`，不等同于 Windows 原生运行测试。
