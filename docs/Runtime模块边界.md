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
- `runtime/query`：Query/continuation 门面、控制命令准入和 restart-safe deferred awaiting registry；通过端口连接正在迁移的 session/admission 实现。
- `runtime/runstate`：活动 Run 注册、Chat 独占、observer/backlog、attach/detach、控制状态、compact barrier、Run 环境销毁和快照查询。
- `runtime/runexec`：Native Query 共用执行核心 `Execute`，负责启动模型流、驱动编排、组装事件消费、StepLine 写入、模型轮次提交/丢弃、usage/cost 聚合和终态识别。
- `runtime/orchestration`：解释普通 delta、`agent_invoke` 与 Team dispatch，并提供 Reference 去重等公共逻辑；具体子 Session 与结果回注通过端口完成。
- `runtime/proxy`：上游 HTTP/WS 协议值对象、Reference 物化规则、事件/usage 映射、活动 Proxy route 和 submit/steer/interrupt 控制客户端。

## 调用与依赖规则

1. `runtime/**` 不得 import `internal/server`。
2. `runtime/types`、`query`、`runexec`、`orchestration`、`proxy` 不得 import `internal/api`。
3. `query`、`runexec`、`orchestration` 不得 import `net/http` 或 `internal/ws`。
4. Server 不得直接 import `internal/llm`、`internal/tools` 或具体 Agent mode 包。
5. Server 只依赖窄 `QueryRuntime` 接口；Runtime 的创建和绑定只发生在 `app.New`。
6. automation 与 `runops` 直接调用 Runtime，不允许借用 Server 业务门面。

上述规则由 `internal/architecture/boundaries_test.go` 使用标准库 AST 检查。

## 生命周期时序

`runtime/runstate.Manager` 是生产与集成测试唯一的 RunManager 实现，统一由 `runstate.NewManager()` 创建；同一运行环境的 Query、控制接口与客户端目标绑定共享该实例。`contracts` 保留 `RunManager` 等接口、`RunControl`、错误与不透明 `CompactControlHandle`，依赖方向固定为 `runstate -> contracts`，不提供旧 Manager 的别名或转发构造函数。

Manager 行为测试位于 `runtime/runstate` 同包，回收测试可以直接调整私有时间状态并调用回收逻辑，不为测试扩展生产接口。`contracts` 中的纯 RunControl 和 owner 契约测试继续保留。Server fixture 固定持有 `*runstate.Manager`；模拟重启时显式注入新 Manager，只从持久化存储恢复 awaiting、原始开始时间和事件游标，不复用旧实例中的 Run、claim 或 compact 状态。

异步 Query 的稳定时序是：

```text
decode/auth -> Runtime.StartQuery -> Runtime.AttachRun -> transport forward
            -> executor persist terminal -> EventBus freeze
            -> subscription acknowledges delivery -> RunState finish
```

客户端断开只关闭本订阅，不中断 Run。EventBus freeze 时订阅必须确认 delivery done，否则终态注销、Chat admission 和下一轮 Query 都可能被阻塞。SSE 与 WS 现在复用 Runtime 的相同订阅语义。

订阅关闭必须同时执行 detach 和 `Observer.MarkDone()`：freeze 已从 EventBus 移除 live observer 后，单独 detach 无法再找到它并确认交付完成。Manager 的 `Finish` 不接管 EventBus 的冻结等待，执行端仍先完成终态持久化与交付确认，再释放 Run 的活动状态。

## 同步与异步 Query 的共用执行核心

HTTP 默认 SSE、WebSocket 启动的 Native Run、HTTP `stream:false`、Runtime 进程内阻塞调用和旧内部 Query 接口都调用 `runexec.Execute`。同步入口不再维护另一份 `Next/Map` 循环；子 Agent、Team 调度、阶段标记、awaiting、usage 聚合和 continuation 使用同一路径。具体子 Session 构建和 Proxy 子任务仍由现有编排适配器提供。

正文、usage 和 finishReason 以执行器生成并交给持久化的 `RunCompletion` 为唯一结果来源。Native 非流式响应不再用 `queryEventCollector` 的计算结果补写它们；该 collector 仍服务于 Proxy 协议适配。`fullText` 单独观察经过 Processor 的 normalized 内部事件，保留重试丢弃信号，不从公共 EventBus 反推模型轮次。StepWriter 在最终完成记录之前 flush，以保留最后一个阶段的模型信息。

等待方式由入口决定：SSE/WS 断线只解除订阅，Run 可继续并通过 attach 重新订阅；阻塞调用在执行期间保持 observer，沿用原有 RunControl context，不把请求取消改成新的 Run 中断策略。执行结束后先冻结并排空事件，再注销 Run、释放准入；只有成功持久化完成的 Run 才尝试启动 continuation。

Runtime 的 Native 阻塞调用直接取得执行结果，不再生成 HTTP 请求或解析 SSE。旧内部接口的 status/body 只做兼容编码；内部 SSE 回调入口直接复用 Runtime 启动/订阅适配。Proxy 主 Run 保留现有 SSE/WS 驱动、完成记录捕获及非流式 collector，尚未统一到 Native 执行核心。此次调整不改变 JSONL、数据库 schema、assembler/mapper 渲染与缓冲规则、事件字段或序列。

## 相邻应用服务

- `conversation.Service` 承担批量 Archive/Restore、active-run gate、Archive list/search/load/delete；`chat` 仍是持久化事实源。
- `adminsource.Service` 承担 source mutation 锁以及 Agent ZIP 的 staging、reload 校验、commit、rollback 和恢复 reload 事务。
- `chatresource.Service` 承担资源解析、图片 commit 的 Chat owner 校验与文档持久化。
- `project.Service` 由 app 创建，承担 Workspace tree/changes/diff；Server 只映射 HTTP 参数和错误。
- `modelclient` 承担 Provider HTTP、首响应超时、响应生命周期和 provider 错误分类。

## 迁移期约束

为保持一次重构内的外部协议、JSONL 与 SQLite 行为不变，Query admission/session 构建、子任务具体执行和 Proxy 数据流仍通过显式端口连接现有实现。新增业务不得再进入这些 Server 适配文件；后续迁移应按可回归的垂直行为切片删除端口，而不是再建立新的反向依赖或大一统 engine。
