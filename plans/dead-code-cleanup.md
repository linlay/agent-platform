# 死代码与遗留链路清理（一级、二级）

## 一句话

删除生产中已无调用方的函数、常量与转发壳，以及一次性文档和脚本；再清掉只剩测试在调用的遗留链路，同时删除或改写对应测试。两级都不改变任何对外行为。

## 基线与方法

- 基线：`d6aac5f8`（v0.5.26）加 2026-10-28 的未提交工作区（Mailbox 改动）。执行前先提交或确认这批改动，清理在独立分支上进行。
- 函数：`deadcode` 分别在 darwin、linux、windows 三个平台跑两遍——从 `./cmd/... ./scripts/...` 出发（生产可达性），以及带 `-test ./...`（含测试的可达性），取三平台交集，平台专用代码不进清单。
- 常量、变量、类型：自写分析器统计包级对象的引用，不计落在死函数体内的引用；再用 `staticcheck U1000` 复核，生产代码没有新增项。
- 级别：一级 = 测试也不调用；二级 = 生产不可达、但测试仍在调用。
- 行数含函数上方的注释，统计的是生产代码，不含配套测试。

| 级别 | 生产函数 | 生产行数 | 其他 |
|---|---|---|---|
| 一级 | 63 | 489 | 39 个常量/变量/类型（另有 4 个随函数连带删除）；测试里 29 个死辅助函数和 86 个死别名；约 4.4k 行文档、实验数据与脚本 |
| 二级 | 61（另有 4 个排除） | 约 670 | 加上 deadcode 测不出的"恢复等待项"链路，约 450 行（估算） |

## 执行规则

1. 每组删完：`gofmt`，然后分别在 darwin、linux、windows 下 `go build ./...`，再 `go test ./...`；复跑 deadcode 与对象分析，确认没有新增或漏删。
2. 每组一个 commit，便于单独回退。
3. 二级改测试时，只改"测试的进入方式"，不放宽断言；删掉的测试必须是只验证死代码的。
4. 文档同步：`AGENTS.md`、`docs/README.md`、`docs/MCP与工具交互.md`（见 2.3）。

---

## 一级：确定可删，不改变行为

### 1.1 无调用的生产函数（63 个，489 行）

路径省略 `internal/` 前缀。

**agent / api / accesspolicy**

| 位置 | 符号 | 行 | 说明 |
|---|---|---|---|
| `accesspolicy/accesspolicy.go:713` | `WorkspaceEditingDisabled` | 4 | |
| `agent/builtin/runtime_facade.go:39` | `IsCoderMode` | 1 | 门面转发，无调用方 |
| `agent/builtin/runtime_facade.go:44` | `ExecutorCreateToolNames` | 3 | 同上 |
| `agent/builtin/runtime_facade.go:55` | `IsCoderNativeBackend` | 3 | 同上 |
| `agent/builtin/runtime_facade.go:156` | `TeamDefaultBudget` | 1 | 同上 |
| `agent/builtin/runtime_facade.go:157` | `TeamDefaultToolNames` | 1 | 同上 |
| `agent/builtin/runtime_facade.go:158` | `TeamDefaultContextTags` | 1 | 同上 |
| `agent/executor/profile.go:38` | `CreateToolNames` | 6 | 连带 `createToolNames` 变量 |
| `agent/team/profile.go:40` | `DefaultContextTags` | 3 | 连带 `defaultContextTags` 变量 |
| `api/types.go:347` | `DecodeSubmitParam` | 3 | 转发 queryinput |

**catalog / chat / config / connector**

| 位置 | 符号 | 行 | 说明 |
|---|---|---|---|
| `catalog/agent_type.go:58` | `AgentModeAliases` | 9 | 历史 mode 别名表 |
| `catalog/control_source.go:5` | `RenderDefinition` | 3 | 整个文件只有这一个函数 |
| `catalog/registry.go:1044` | `defaultString` | 6 | |
| `chat/store_schema.go:140` | `nilIfEmpty` | 6 | |
| `config/config_files.go:279` | `durationValue` | 32 | |
| `connector/state.go:52` | `InternalStateLink` | 31 | |
| `connectorauth/environment.go:107` | `ResolveEnvironments` | 16 | |
| `connectormigrate/desktop.go:358` | `containsMigrationString` | 8 | 迁移包本身的去留属于三级 |
| `contracts/value_helpers.go:121` | `IntValue` | 3 | |

**kbx / knowledge / llm / memoryworker / models / pathutil**

| 位置 | 符号 | 行 | 说明 |
|---|---|---|---|
| `kbx/query.go:304` | `documentPath` | 7 | |
| `knowledge/config.go:147` | `anyInt` | 4 | |
| `knowledge/config.go:174` | `applyIntAliases` | 7 | |
| `knowledge/tool_handler.go:348` | `toolBoolArg` | 4 | |
| `llm/run_stream_turn.go:780` | `sessionHasTool` | 8 | |
| `memoryworker/config_sync.go:19` | `ConfigSyncFunc.Sync` | 1 | 连带类型 `ConfigSyncFunc` |
| `models/model_registry.go:277` | `IsTTSModel` | 4 | |
| `pathutil/pathutil.go:46` | `NearestExistingAncestor` | 20 | |

**platformcontrol**：旧 platform_control 的 validate 动作，已被 `catalog_manage` 的 validate/apply 取代。

| 位置 | 符号 | 行 |
|---|---|---|
| `platformcontrol/tool_handler.go:108` | `ToolHandler.validate` | 43 |
| `platformcontrol/tool_handler.go:152` | `ToolHandler.teamMemberDiagnostics` | 25 |
| `platformcontrol/tool_handler.go:227` | `hasErrorDiagnostic` | 8 |

连带常量 `maxCandidateBytes`（`tool_handler.go:28`）。

**runtime**

| 位置 | 符号 | 行 | 说明 |
|---|---|---|---|
| `runtime/query/awaiting_continuation.go:26` | `Service.startAwaitingContinuation` | 3 | 属于 2.1 链路中已完全断开的部分 |
| `runtime/query/recovered_awaiting_run.go:77` | `Service.superviseRecoveredAwaiting` | 38 | 同上 |
| `runtime/query/restart_awaiting.go:286` | `persistedAwaitingCanContinue` | 15 | 同上 |
| `runtime/query/resource.go:8` | `safeFilename` | 7 | |
| `runtime/query/server_tools_meta.go:12` | `normalizeQueryRole` | 3 | role 不参与执行 |
| `runtime/query/server_tools_meta.go:16` | `defaultRole` | 7 | 同上 |
| `runtime/runexec/native.go:661` | `LenAnyMap` | 6 | |

**server**：大多是逻辑迁到 runtime 后留下的一行转发。

| 位置 | 符号 | 行 |
|---|---|---|
| `server/chat_tools_facade.go:35` | `runOwnerMatchesChat` | 7 |
| `server/chat_tools_facade.go:43` | `mapRunAdmissionError` | 11 |
| `server/coder_acp.go:99` | `Server.acpCoderModelOptions` | 10 |
| `server/handler_automation.go:158` | `Server.automationDepsReady` | 1 |
| `server/handler_automation.go:186` | `Server.automationExecutionHistoryStatus` | 3 |
| `server/handler_automation.go:189` | `Server.findAutomation` | 3 |
| `server/handler_automation.go:196` | `Server.automationDisplayLocation` | 3 |
| `server/handler_automation.go:199` | `Server.nextAutomationID` | 3 |
| `server/handler_query_prepare.go:41` | `Server.resolvedQueryExecution` | 13 |
| `server/handler_query_prepare.go:92` | `chatAgentMode` | 6 |
| `server/interrupt_info.go:17` | `interruptRequestForQuery` | 8 |
| `server/interrupt_info.go:26` | `serverSetupInterruptRequest` | 3 |
| `server/proxy_ws_forward.go:17` | `proxyWebSocketTarget` | 3 |
| `server/proxy_ws_forward.go:41` | `proxyRequestHasReservedCWD` | 3 |
| `server/proxy_ws_forward.go:45` | `proxyAgentKey` | 3 |
| `server/proxy_ws_forward.go:59` | `proxyFrameMatchesRequest` | 3 |
| `server/proxy_ws_forward.go:63` | `proxyFrameError` | 3 |
| `server/proxy_ws_forward.go:67` | `decodeProxyEventAt` | 3 |
| `server/proxy_ws_forward.go:71` | `normalizeProxyEventIdentity` | 3 |
| `server/proxy_ws_forward.go:332` | `lenAnyMap` | 6 |
| `server/run_executor.go:13` | `Server.broadcastRunCompletionNotifications` | 16 |
| `server/server_tools_meta.go:242` | `normalizeQueryRole` | 3 |

**其他**

| 位置 | 符号 | 行 |
|---|---|---|
| `shellanalysis/git.go:200` | `ExecutableGitKey` | 3 |
| `sqlitecontract/contract.go:178` | `IsUnmarked` | 5 |
| `supportpkg/registry.go:130` | `Registry.Root` | 6 |
| `tools/tool_value_helpers.go:45` | `firstNonBlank` | 8 |

### 1.2 无引用的常量、变量、类型（39 个）

| 位置 | 符号 |
|---|---|
| `agent/builtin/runtime_facade.go:21,23` | `CoderMainStage`、`TeamMode` |
| `agent/kbase/boundary.go:6-9` | `ToolSearch`、`ToolFiles`、`ToolRead`、`ToolStatus` |
| `api/types.go:216,220` | `QueryRoleValidationMessage`、`ChatSourceRunQueryPrefix` |
| `apperrors/codes.go:24` | `CategoryInterrupt` |
| `bashsec/bash_security.go:32` | `RuleKeyRedirections` |
| `catalog/registry.go:201` | `TeamDefaultMaxParallel` |
| `chat/archive_types.go:50` | type `ArchiveResult` |
| `contracts/errors.go:15` | `ErrBudgetExceeded` |
| `contracts/interfaces.go:29` | `ToolExecutionPolicyDefault` |
| `conversationexport/snapshot.go:22` | `MaxLabelBytes` |
| `gateway/registry.go:199` | type `Resolver` |
| `models/model_registry.go:89` | `PricingUnitPer1MTokens` |
| `runtime/query/full_text.go:8-11` | `isDiscardIncompleteModelTurnRecovery`、`stringSetFromAny`、`firstNonBlankString`、`formatFullTextValue`（`var x = runexec.X` 形式的壳） |
| `runtime/query/time_contract.go:30-36` | `errTimeContractViolation`、`chatStorageSchemaErrorData`、`contractViolationMessage`、`localTimeContractRunErrorEvent`、`nextLocalTimeContractErrorSeq` |
| `server/document_kind.go:18,20-22` | `documentKindPDF`、`documentKindAudio`、`documentKindVideo`、`documentKindArchive`（转发 documentmeta，不是 iota，删除不改变值） |
| `server/full_text.go:9-10` | `stringSetFromAny`、`firstNonBlankString` |
| `server/proxy_references.go:41,43` | type `proxyReferenceOptions`、`prepareProxyReferences` |
| `server/run_executor.go:11,31,32` | `persistedRunMode`、`rawAwaitingIDForTask`、`maybeBroadcastInterruptedAwaiting` |
| `server/runtime_lease.go:6` | `acquireTeamRuntime` |
| `server/time_contract.go:123` | `errTimeContractViolation` |

另有 4 个随 1.1 一起删除的连带对象：`createToolNames`、`defaultContextTags`、`ConfigSyncFunc`、`maxCandidateBytes`。

### 1.3 测试代码里的无用项

**deadcode：测试文件中无人调用的辅助函数（29 个）**

| 文件 | 符号 |
|---|---|
| `llm/hitl_submit_test.go` | `mustEncodeHITLSubmitParams` |
| `llm/run_stream_test.go` | `sampleExpenseCommand`、`canonicalExpenseCommand`、`sampleProcurementCommand`；类型 `stubChecker`、`commandResultChecker` 及各自的 `Check/Tool/Tools` |
| `llm/tool_filter_test.go` | `containsToolName` |
| `server/deferred_submit_test.go` | `seedCoderPlanningAwaitingForDeferredSubmit`、`assertDeferredPlanningApproveJSONL`、`waitForRecordedNotificationType` |
| `server/prompt_context_test.go` | `testContextAgentSummaries` |
| `server/query_application_test.go` | `Server.persistedRunLiveSeq` |
| `server/query_helpers_test.go` | `validateSubmitParam` |
| `server/server_hitl_test.go` | 类型 `recordingMCPClient` 及 `CallTool`、`assertSpecificEventOrder` |
| `server/session_adapter_test.go` | `Server.buildCurrentMessages`、`Server.resolveQueryReferencedSkills`、`Server.newRunEnvironmentScope`、`Server.prepareSystemInitCacheFrom` |
| `server/test_support_test.go` | `assertSSEEventDuration` |
| `server/time_contract_fixture_helpers_test.go` | `bindFixtureRunScopes` |
| `server/wait_recovery_test.go` | 类型 `runEnvWaitCheckpointSink` 及 `EmitToolOutput/EmitToolWait` |
| `tools/test_support_test.go` | `newTestMemoryStore` |

`runops/query_arguments_test.go:76` 的 `func _` 是编译期断言，保留。

**staticcheck：server 测试里无人使用的别名壳（86 处）**

这些是逻辑迁到 runtime 之后，为了让旧测试还能编译而留在 server 包里的 `var x = session.X` 之类的别名。只删没人用的条目；删完变空的文件直接删除。

| 文件（总行数） | 无用条目 |
|---|---|
| `server/session_helpers_test.go`（84） | 47 个：`sortedStringKeys`、`referencePathWithinHostRoot`、`translateReferencePathForContainer`、`localSemanticRoots`、`requireReferenceWorkspacePath`、`currentChatResourceRelativePath`、`referenceResourceRelativePath`、`referenceName`、`resourceFileName`、`skillCenterCatalog`、`skillCatalogBlock`、`normalizeReferencedSkills`、`resolveReferencedSkillRoot`、`resolveCenterSkillID`、`ensureChatDir`、`chatDirPath`、`resolveContainerSandboxPaths`、`resolveLocalSandboxPaths`、`buildSandboxContext`、`fetchSandboxPrompt`、`summarizeSandboxMounts`、`promptContextSandboxMounts`、`cleanOrEmpty`、`absOrEmpty`、`resolveLocalSkillsDir`、`promptContextHasPlatformMount`、`ifNonEmpty`、`boolPath`、`agentConnectorPath`、`resourceFileParamForChat`、`isResourceURL`、`normalizedAgentTools`、`normalizeRuntimeMount`、`runtimeExtraMounts`、`nullableStringValue`、`extractRuntimeField`、`runEnvironmentLookup`、`lookupRunEnvironment`、`isProxyRoutedAgent`、`containsTool`、`shouldLoadPlanTaskContext`、`resolvedModeCapabilities`、`coderProjectPromptFiles`、`normalizedAccessLevel`、`runtimeHostAccess`、`mcpServerToolResolver`、`mcpToolNamesForServers` |
| `server/native_helpers_test.go`（22） | 13 个：`compactCheckpointPersistenceFailedEvent`、`compactCycleFlag`、`shouldStartRunContinuation`、`decorateNotificationRunOwner`、`awaitingEventItemCount`、`awaitingEventQuestions`、`awaitingPayloadItemCount`、`awaitingAnswerErrorCode`、`completeCompactControl`、`compactFloat64`、`clientVisibleEventData`、`publishLocalTimeContractRunError`、`publishLocalRunProcessingError` |
| `server/proxy_references_bindings_test.go`（19） | 12 个：`normalizeProxyReferenceURL`、`sameURLOrigin`、`resourceURLForFileParam`、`safePathSegment`、`deduplicateProxyInputPath`、`sameFileContent`、`sameFilesystemPath`、`copyProxyFile`、`guessProxyMimeType`、`isPathOutsideBase`、`materializeProxyFileReference`、`normalizeProxyReferencePath` |
| `server/orchestration_helpers_test.go`（14） | 6 个：`namespaceChildID`、`namespaceChildIDs`、`firstPayloadString`、`currentMessagesFromSession`、`containsInvokeAgentsTool`、`sameAgentKey` |
| `server/system_init_bindings_test.go`（11） | 5 个：`cloneMap`、`cloneAnySlice`、`sameSystemInitPayload`、`queryLineSystemFromProfile`、`systemInitSnapshotFromLine` |
| `server/frame_orchestrator_alias_test.go`（13） | 2 个：`firstNonEmpty`、`childRouteEvent` |
| `server/proxy_recorder_adapter_test.go` | 1 个：`newProxyUsageTracker` |

删掉 `containsTool` 别名后，`runtime/session/prompt_context.go:1058 ContainsString`（8 行）就完全没人用了，一并删除。

### 1.4 文档、实验数据、脚本（约 4.4k 行）

| 路径 | 行 | 理由 | 需同步修改 |
|---|---|---|---|
| `docs/审计R11-R12复核.md` | 5 | 只剩跳转 | `docs/README.md` 的"历史归档"一节 |
| `docs/archive/audits/审计R11-R12复核.md` | 123 | 2026-10 的一次性审计记录 | 同上；删后 `docs/archive/` 为空，一并删除 |
| `docs/KBX全目录验证报告.md` | 134 | 10-05 的一次验证，基线 `aad4d4d1`，没有任何引用 | 无 |
| `docs/Anthropic缓存实测.md` | 108 | 10-09 的实测；结论已在同日实现为 `llm/protocol_anthropic.go` 的显式缓存断点 | 无 |
| `docs/experiments/anthropic-cache-2026-10-09.json` | 3,448 | 上一份的原始数据 | 删后 `docs/experiments/` 为空，一并删除 |
| `scripts/anthropic-cache-probe/main.go` | 454 | 实测脚本，只被上一份文档引用 | 无 |
| `scripts/backup-compact-chat.mjs` | 51 | compact 实验用的备份脚本，没有引用 | 无 |
| `plans/view-cli-intercept.md` | 94 | 已实现（`83bdbb7f`），规则已写进 `docs/VIEW连接器.md` | 文中记的 Jira `worklog.add` 待办属于 zenmind-env 仓库，如需保留，先挪过去 |

### 1.5 静态检查顺手修（不改变行为）

| 位置 | 问题 | 改法 |
|---|---|---|
| `builtins/stage.go:740`、`cmd/verify-program-bundle/main.go:347` | `tar.TypeRegA` 已废弃 | 改用 `tar.TypeReg`；先确认判断逻辑仍能覆盖旧归档 |
| `conversation/control.go:413` | `ids := []string{}` 的初值没被用到 | 改成 `var ids []string` |
| `runtime/orchestration/coordinator.go:256` | `teamMaxParallel` 的初值没被用到 | 把赋值挪进分支 |
| `adminsource/control.go:557` | 循环逐个 append | 改成一次 `append(..., xs...)` |
| 测试：`chat/compact_categories_test.go:96`、`project/git_branches_test.go:120`、`server/handler_agents_mode_test.go:103`、`server/skill_execution_test.go:33,73` | 赋值后未使用 | 删除或补上断言 |
| 测试：`server/handler_resource_integration_test.go:236` | `runtime.GOROOT` 已废弃 | 改用 `go env GOROOT` |

`runtime/query/mailbox.go:467`（`DeliverMail` 收到的 `ctx` 在使用前就被覆盖）在未提交的 Mailbox 改动里，不放进本次清理，由 Mailbox 那边决定。

---

## 二级：只剩测试在调用的遗留链路

处理方式说明：
- **删除**：连同只验证它的测试一起删。
- **改入口**：测试改为调用生产实际使用的函数，断言不变。
- **移入测试**：只有同包测试在用的辅助函数，挪到 `_test.go`，生产代码里不再保留。
- **保留**：列在本节末尾的排除项。

### 2.1 重启恢复等待项（recovered awaiting）链路

**事实**
- 启动时 `runtime/query/restart_runs.go:14 abandonPersistedRuns` 会取消所有持久化的等待项和未完成的 Run（与 AGENTS.md 中"重启统一取消、清除等待项"一致）。
- 生产代码里没有任何地方调用 `Service.RegisterRecoveredAwaitingRun`，唯一调用方是测试包装 `server/query_application_test.go:52`。所以 runstate 里永远不会有 `recoveredAwaitingID` 非空的 Run：`IsRecoveredAwaiting` 和 `ClaimRecoveredAwaiting` 恒为 false，`recovered` 恒为 nil。
- deadcode 查不出这条链路，因为相关方法挂在接口上、形式上是可达的，只能人工确认。

**生产代码删除范围**（约 450 行，估算）

| 位置 | 内容 |
|---|---|
| `contracts/interfaces.go:187-205` | `RecoveredAwaitingRun`、`RecoveredAwaitingRunService` |
| `contracts/awaiting_resolution.go:27-28` | `AwaitingHasLiveExecutor` 中的 recovered 判断 |
| `contracts/awaiting_resolution.go:39-43` | `ClaimAwaitingResolution` 中的 claim 分支 |
| `runtime/runstate/manager.go:40-43` | 字段 `reaperStartOverride`、`recoveredAwaitingID`、`recoveredClaimed` |
| `runtime/runstate/manager.go:266-355` | `RegisterRecoveredAwaiting`、`ClaimRecoveredAwaiting`、`ReleaseRecoveredAwaiting`、`ActivateRecoveredAwaiting`、`IsRecoveredAwaiting`、`recoveredAwaitingRunFromManaged` |
| `runtime/runstate/manager.go:836` | reaper 的 `reaperStartOverride` 分支 |
| `runtime/runstate/manager.go:901` | 接口断言 |
| `runtime/query/recovered_awaiting_run.go` | 整个文件（266 行）。其中 `publishRecoveredEvent` 和 `recoveredAnsweredToolResult` 只在 `submit_resolution.go:389-394` 的 recovered 分支里用到 |
| `runtime/query/submit_resolution.go:309-330, 360, 389-394` | recovered 分支 |
| `runtime/query/awaiting_continuation.go:54, 78-87, 290, 310-330` | `recovered` 参数及其分支 |
| `runtime/query/restart_awaiting.go:180-181, 194-195, 230-231` | `FinishTerminalAwaiting` 中的 claimed 分支 |

**测试**

| 文件 | 测试 | 处理 |
|---|---|---|
| `runtime/runstate/manager_test.go` | `TestManagerRecoveredClaimIsExclusiveAndRetryable` | 删除 |
| `runtime/runstate/manager_lifecycle_test.go` | `TestManagerRecoveredAwaitingReaperStartsAtHydration`、`TestManagerRecoveredAwaitingIsAttachableAndClaimedOnce` | 删除 |
| `runtime/runstate/snapshot_test.go` | `TestSnapshotRecoveredAwaitingUsesPersistedAsk` | 删除 |
| `runtime/query/awaiting_resolution_test.go` | `TestAwaitingResolutionClaimHasSingleOwnerAcrossActivation` | 删除 |
| `server/awaiting_resolution_test.go` | `TestRecoveredAwaitingClaimBlocksConcurrentTimeoutReadAndSubmit`、`TestRecoveredAwaitingSubmitWinsConcurrentExpiredRead` | 删除 |
| `server/query_application_test.go` | `registerRecoveredAwaitingRun` 包装 | 删除 |
| `architecture/boundaries_test.go` | 名单里提到的 `RecoveredAwaiting` | 同步更新 |

**注意**：提交与超时并发路径里，只删 `recovered != nil` 的分支，nil 分支保持原样。`runstate/snapshot.go:56` 附近的 "Recovered waiters live in the deferred registry" 说的是延迟审批，不属于这条链路，不动。

### 2.2 Agent 运行目录的旧"稳定根同步"发布

现在的发布方式是 `ru-agents/<key>/<revision>` 版本目录，旧的"同步到稳定根、带 Windows 重命名重试"的发布已经没人调用。

| 位置 | 符号 | 行 | 测试引用 | 处理 |
|---|---|---|---|---|
| `catalog/runtime_agent_rename.go` | 整个文件：`renameRuntimeAgent`、`runtimeAgentRenameRetryable`、`runtimeAgentPathState` | 74 | `runtime_agent_rename_test.go`：`TestRuntimeAgentRenameRetryPolicy`、`TestRuntimeAgentRenamePreservesRecreatedDestination`；`runtime_agent_rename_windows_test.go`：`TestRuntimeAgentRenameWindowsDirectoryHandle` | 删除 |
| `catalog/runtime_connector_publish.go` | 整个文件：`publishAgentCandidate`、`publishAgentCandidateWithRename` | 60 | `TestRuntimeAgentPublicationRollback`、`TestRuntimeAgentPublicationReplacesAndSkipsUnchanged` | 删除 |
| `catalog/runtime_agent_assembler.go:836` | `syncRuntimeTree` | 85 | 无（只被上面调用） | 删除 |
| `catalog/runtime_agent_assembler.go:922` | `atomicCopyRuntimeFile` | 44 | 无 | 删除 |

两个测试文件共 183 行，整体删除。`privateFileMode` 仍有生产调用，保留。删之前看一眼 `docs/Agent运行时组装.md` 有没有提到重命名重试。

### 2.3 已退役的 `desktop_action` 工具残留

`catalog/agent_loader.go:964` 已经拒绝 `desktop_action` 这个工具名。Desktop 动作现在由 `desktop_shell`、`desktop_settings`、`desktop_site`、`desktop_webapp`、`desktop_service`、`desktop_market`、`desktop_kanban` 这些域工具（`tools/tool_executor.go:264`）和 web-control 直接调用 `dispatchDesktopAction` 完成，这些调用不经过白名单。

**生产代码**

| 位置 | 符号 | 行 |
|---|---|---|
| `tools/tool_desktop_action.go:72` | `invokeDesktopAction` | 14 |
| `tools/tool_desktop_action.go:51` | `getDesktopActionAllowlist` | 6 |
| `tools/tool_desktop_action.go:39-41` | 变量 `desktopActionAllowlistOnce`、`desktopActionAllowlist`、`desktopActionAllowlistErr` | 3 |
| `tools/desktop_action_allowlist.go:116` | `loadDesktopActionAllowlist` | 3 |
| `tools/desktop_action_allowlist.go:120` | `buildDesktopActionAllowlist` | 16 |
| `tools/desktop_action_allowlist.go:12` | `desktopActionNames` | 约 85 |
| `tools/desktop_action_allowlist.go:101` | `webControlActionNames` | 约 12 |
| `tools/desktop_action_allowlist.go:114` | `desktopActionNamePattern` | 1 |

`desktop_action_allowlist.go` 基本可以整个文件删除；动手前先确认其中有没有域工具在用的条目。

**测试**

| 测试 | 处理 |
|---|---|
| `tool_desktop_action_test.go` 里验证传输与路由的 11 个：`TestDesktopReverseRequestUsesCallerToolDeadline`、`TestDesktopRuntimeModeRoutingMatrix`、`TestDesktopRuntimeRebindsStaleRunTargetBeforeDispatch`、`TestDesktopRuntimeDoesNotReplayAfterClientDisconnect`、`TestDesktopRuntimeReportsDesktopMainAvailability`、`TestDesktopRuntimeTeamSourceKeepsTeamIdentity`、`TestDesktopRuntimeConcurrentRunsKeepReverseRequestsIsolated`、`TestDesktopActionAllowlistUsesDirectReverseRequestFrames`、`TestDesktopActionRejectsReservedArgs`、`TestDesktopConfirmationErrorsRemainDistinct`、`TestDesktopTransportTimeoutAndCancellationRemainDistinct` | **改入口**：改调 `dispatchDesktopAction`，断言不变 |
| `desktop_action_paths_test.go`：`TestDesktopActionAliasDispatch`、`TestDesktopInstallChatAliasDispatchWithoutWorkspace` | **改入口** |
| `tool_desktop_action_test.go` 里验证白名单本身的 8 个：`TestInvokeDesktopActionCallsBridge`、`TestInvokeDesktopActionReassemblesValidatedStream`、`TestInvokeDesktopActionRejectsInvalidStreamManifest`、`TestInvokeDesktopActionRejectsUnknownAction`、`TestInvokeDesktopActionAllowsCurrentDesktopActions`、`TestInvokeDesktopActionRejectsLegacyAndUnsupportedActions`、`TestInvokeDesktopActionRejectsPageActions`、`TestInvokeDesktopActionRequiresClientProvider` | 前三个如果覆盖了 bridge/stream 的行为，改入口保留；其余删除 |
| `tool_desktop_contract_test.go`：`TestDesktopActionRejectsWebappPageOnlyActions` | 删除 |
| `tool_desktop_contract_test.go`：`TestDesktopActionContractMatchesDesktopSource` | 改为核对各域工具实际发出的动作是否仍存在于 Desktop 上游定义；做不到就删除 |
| `desktop_action_allowlist_test.go`：`TestDesktopActionRuntimePolicyRejectsInvalidDefinitions` | 删除 |
| `tool_desktop_action_test.go` 中的辅助函数 `sortedDesktopActionAllowlist` | 删除 |

**文档**
- `AGENTS.md`："Desktop 普通 Action 白名单跟随 desktop-actions.ts……DESKTOP_SOURCE"这一条。
- `docs/MCP与工具交互.md:99、171、173`：仍把 `desktop_action` 当作现行工具来描述，按域工具的现状改写。
- `docs/API与协议.md`、`docs/自动化.md`、`docs/子智能体调度.md`、`docs/Platform控制工具设计.md`、`docs/连接器共享包与Desktop迁移.md` 里提到 `desktop_action` 的地方逐处核对。

### 2.4 技能 `.bash-hooks` 删除后的残留

| 位置 | 符号 | 行 | 测试引用 | 处理 |
|---|---|---|---|---|
| `shellanalysis/command.go:117` | `HookCommand` | 45 | `operands_test.go:160`，`TestShellScriptAndEnvSplit` 中的一条断言 | 删除函数和这条断言 |
| `shellanalysis/command.go:163` | `gitSubcommandArgs` | 21 | 无 | 删除 |

### 2.5 server 层只给测试用的壳

| 位置 | 符号 | 行 | 测试引用 | 处理 |
|---|---|---|---|---|
| `server/push_payloads.go` | 整个文件：`runStartedPushPayload`、`runFinishedPushPayload`、`normalizedRunFinishedReason`、`runFinishedStatus` | 51 | `push_payloads_test.go`（43 行）：`TestLifecycleAndCatalogPushPayloadsUseSemanticTimeFields`、`TestRunFinishedPushPayloadMapsTerminalStatus` | **删除**。这份测试验证的是副本，生产实现在 `runtime/runexec/proxy.go:148` 等处。删前确认 runexec 那边有等价测试，没有就把断言迁过去 |
| `server/server_routes.go:201-254` | 类型 `sseInterceptor` 及 `newSSEInterceptor/Header/WriteHeader/Write/Flush` | 约 50 | `internal_query_adapter_test.go` 的 `ExecuteInternalQueryStream` | **移入测试** |
| `server/proxy_ws_forward.go:33,37` | `proxyQueryPayloadWithWorkspace`、`proxyForwardParams` | 6 | `proxy_workspace_test.go`：`TestProxyQueryPayloadWithWorkspaceDoesNotExposeHostCWD`、`TestProxyForwardParamsWithoutWorkspaceKeepsLegacyParams` | **改入口**：测试迁到 `runtime/proxy`，直接调 `QueryPayloadWithWorkspace`、`ForwardParams` |
| `server/proxy_ws_forward.go:49,55,75` | 类型 `proxyDecodedFrame`、`decodeProxyFrameAt`、`proxyRunErrorEvent` | 约 12 | `proxy_ws_forward_test.go`：`TestDecodeProxyFrameRejectsInvalidUpstreamTimestamp`、`TestProxyRunErrorEventUsesLocalTimestampForContractViolation` | **改入口**：迁到 `runtime/proxy` 和 `runexec` |
| `server/proxy_ws_handler.go:7,14` | `registerProxyRun`、`unregisterProxyRun` | 12 | `chat_steer_test.go` 2 个测试；`run_control_agentkey_test.go` 4 个测试 | **改入口**：测试直接用 `s.proxyRuntime.Register/Unregister`。`lookupProxyRun` 在生产里有用，保留 |
| `server/runtime_lifecycle_port.go:13,17,26` | `registerQueryRun`、`finishRegisteredQueryRun`、`runtimePreparedQuery`；连带类型 `registeredQueryRun`（`query_gate.go:17`） | 约 20 | `query_executor_lifecycle_test.go`、`query_jsonl_time_contract_test.go`、`run_access_level_reload_test.go` 各 1 个测试；`query_application_test.go` 的两个测试方法 | **移入测试**。同文件的 `validateRunOwner`、`validPendingAwaitingInfo` 在生产里有用，保留 |
| `server/handler_query.go:242,247,257` | `queryEventErrorPayload`、`queryEventErrorMessage`、`queryErrorValueMessage`；连带 `server/full_text.go:11 formatFullTextValue` | 28 | `handler_query_collector_test.go` 的测试 collector `Consume` | **移入测试** |
| `server/handler_automation.go:193` | `mapAutomationSummary` | 3 | `handler_automation_test.go`：`TestMapAutomationSummaryUsesPlatformDisplayZoneAndKeepsNextFireEpochPrecision` | **改入口**：测试直接调被转发的 automation 函数 |
| `server/webclient_target.go:50` | `resolveRunWebClientTarget` | 8 | `selection_explain_ws_test.go`：`TestSelectionExplainDetachAttachAndInterruptKeepExistingRunSemantics` | **移入测试** |
| `server/run_executor.go:8,10,30,34` | 类型 `RunExecutorParams`、变量 `broadcastRunCompletion`、`handleAwaitingLifecycle`、类型 `awaitingTracker` | 4 | 测试共 18 处引用 | **改入口**：测试直接引用 `runexec.*`；删完后 `run_executor.go` 整个文件删除 |
| `server/full_text.go:5,7,8` | `queryFullTextBuilder`、`newQueryFullTextBuilder`、`isDiscardIncompleteModelTurnRecovery` | 3 | 各 1 处测试引用 | **改入口**：改用 `runexec.*`；删完后 `full_text.go` 整个文件删除 |
| `server/document_kind.go:19,23` | `documentKindOffice`、`documentKindBinary` | 2 | 测试共 3 处 | **改入口**：改用 `documentmeta.Kind*` |

### 2.6 只剩测试在用的便捷入口和重复实现

| 位置 | 符号 | 行 | 测试引用 | 处理 |
|---|---|---|---|---|
| `api/types.go:351,355` | `DecodeSubmitParams`、`EncodeSubmitParams`（转发 queryinput） | 4 | `frame_orchestrator_team_hitl_test.go` 等 | **改入口**：测试直接用 `queryinput.*` |
| `agent/coder/backend.go:13` | `IsNativeBackend` | 3 | `backend_test.go`：`TestCoderModeGuardsKeepProxySeparate` 中的断言 | 删除函数和这条断言 |
| `agent/team/profile.go:36` | `DefaultToolNames`，连带 `defaultToolNames` | 4 | `builtin/registry_test.go`、`team/prompt_test.go`、`llm/system_init_test.go` 各 1 个 | **改入口**：测试里直接写出 `[]string{team.ToolDelegate}` |
| `knowledge/constants.go:15,23,27` | `capabilityToolNames`、`CapabilityToolNames`、`DefaultToolNames` | 约 14 | `catalog/agent_loader_test.go`、`llm/prompt_builder_test.go`、`server/session_builder_test.go` | **改入口**：测试里直接写出工具名 |
| `llm/llm_engine.go:55` | `NewLLMAgentEngine` | 3 | `llm/team_runtime_test.go`、`server/test_support_test.go` | **改入口**：改调 `NewLLMAgentEngineWithHTTPClient(..., nil)` |
| `connectorauth/execution.go:135` | `nodeShimScript`（转发 connector） | 3 | `execution_test.go`：`TestWindowsNodeShimUsesArgvNotCmd` | **改入口**：改调 `connector.WindowsNodeShimScript` |
| `deprecation/removed.go:28` | `Is` | 1 | `config/config_test.go` 4 个测试 | **改入口**：改用 `errors.Is(err, deprecation.ErrRemoved)` |
| `kbases/storage.go:320` | `Service.fingerprint` | 3 | `collection_metadata_test.go` 1 个测试 | **改入口**：改调 `scopeFingerprint(s.effectiveCollections(...))` |
| `catalog/skill_package.go:688` | `readSkillPackageOwners` | 26 | `skill_import_transaction_test.go`、`skill_package_resilience_test.go` 各 1 个 | **移入测试**；如果断言能改用生产路径表达，就直接删 |
| `catalog/skill_package.go:748` | `skillPackageVersionDiagnostics` | 23 | `skill_metadata_test.go`：`TestSkillPackageVersionDiagnosticsDoNotRewrite` | 删除函数和该测试（这是失去生产用途的诊断） |
| `connector/shared.go:129` | `ReadMount`（含完整性校验） | 21 | `connector/runtime_test.go` 2 个、`mcp/agent_registry_test.go` 2 个、`mcp/connector_isolation_test.go` 1 个 | 先确认生产读取挂载的路径有没有同等的摘要校验：有就删，测试改用生产入口；没有就是漏校验，转为单独问题，不在本次删 |
| `kbx/library_engine.go:26,28` | `NewLibraryEngine`、`defaultLibraryConfig` | 3 | kbx 包内约 15 个测试（含 acceptance） | **移入测试** |
| `kbx/library_engine_config.go:17` | `NewConfiguredLibraryEngine` | 9 | `library_engine_config_test.go`：`TestGlobalLibraryConfigUsesSelectedRegistryModel` | **移入测试**，或改调 `NewLibraryEngineWithSource` |
| `kbx/manager.go:72` | `Manager.resolve` | 11 | kbx 包内 4 个文件（含 `newTestManager`） | **移入测试** |
| `supportpkg/registry.go:145` | `Registry.Executable` | 7 | `registry_test.go` 5 个测试 | **移入测试** |
| `tools/tool_bash.go:464` | `mergeCommandEnv` | 4 | `connector_path_test.go`、`tool_bash_test.go` | **移入测试** |
| `mcp/registry.go:47` | `NewRegistryWithSources` | 7 | mcp 包内 4 个文件；`server/desktop_bridge_local_test.go` 跨包调用 | 先**改入口**处理跨包那一处，再**移入测试** |
| `modelclient/client.go:171,202` | `ResponseError`、`ClassifyResponseError` | 7 | `llm/provider_error_test.go`、`llm/responses_recovery_test.go`（跨包）、modelclient 自己的测试 | 可选：跨包调用改入口后移入测试；收益小，可以放到最后 |
| `bashast/parser.go:14` | `ParseForSecurity` | 3 | bashast 包内 16 个测试 | 可选：**移入测试**（生产代码用的是 `ParseForSecurityWithKnownVariables`） |
| `interaction/config.go:30,70` | `Defaults`、`Parse` | 2 | 跨 8 个包的测试 | 可选：测试改调 `DefaultsForEngine(mode, false)`、`ParseForEngine(mode, false, raw)` |
| `api/kbases.go:6` | 类型 `KBaseCollectionSummary` | — | 测试 2 处 | 改入口或删除 |
| `api/types.go:217` | `ChatSourceQuery` | — | 测试 3 处 | 改用 `queryinput.ChatSourceQuery` |
| `runtime/query/admission.go:722` | 类型 `Admission` | — | 测试 1 处 | 改入口或删除 |
| `tools/tool_plan_persistence.go:11` | 类型 `planTasksSnapshot` | — | 测试 2 处 | **移入测试** |

### 2.7 排除项（工具报告了，但不删）

| 位置 | 原因 |
|---|---|
| `contracts/queryinput/control.go:150 SubmitParams.UnmarshalJSON`、`input.go:37 Reference.UnmarshalJSON` | encoding/json 通过反射调用，deadcode 可能误报；而且删了会改变解码行为 |
| `plantasks/state.go:20 TransitionError.Error` | `error` 接口方法，生产代码会构造这个类型 |
| `contracts/queryinput/control.go:200 EncodeSubmitParams` | 与 Decode 成对，8 个包的测试在用 |
| `mcp/types.go:60 ProtocolVersion` | 记录 SDK 首选版本的说明性常量，测试拿它断言 |
| `llm/logging.go:18-22` 中的 console 类别常量 | 日志类别是一整组枚举，保持完整 |
| `processgroup/run_other.go:7 Run`，以及 httpclient 中的平台解析函数 | 平台相关，交集计算里已排除 |
| `hostshell` 的 `FreezeContext`、`KnownVariables`、`Paths.ResolveMSYS`、`Paths.CommandUsesMSYS` | Windows Git Bash 预制件，归三级决定 |

## 执行顺序

1. 1.1、1.2 → 1.3 → 1.4 → 1.5，分别提交。
2. 2.2、2.4、2.5、2.6，每个小节一个提交。
3. 2.3（涉及测试改入口和文档）。
4. 2.1 最后做，单独提交并完整回归（提交、超时、计划审批相关的测试）。
5. 全部做完后再跑一遍 deadcode 和对象分析，结果附在这份文档的末尾。

---

## 执行结果（2026-10-28）

分支 `chore/dead-code-cleanup`，基于 `4799fce2`，共 10 个提交。

| 提交 | 内容 | 生产 Go | 测试 Go | 其他 |
|---|---|---|---|---|
| `7b305a09` | 1.1/1.2 无调用函数与包级声明 | -635 | 0 | 0 |
| `be4f567e` | 1.3 测试中的无用项 | -2 | -523 | 0 |
| `ba612005` | 1.4 一次性文档、实验数据与脚本（含 `scripts/anthropic-cache-probe`） | -454 | 0 | -3,967 |
| `e94afaf3` | 1.5 staticcheck 小项 | -3 | +3 | 0 |
| `217442a0` | 2.2 旧稳定根发布 | -265 | -183 | 0 |
| `302b205c` | 2.4 HookCommand | -68 | -3 | 0 |
| `8c7c8760` | 2.5 server 测试专用壳 | -231 | +136 | 0 |
| `062b8a39` | 2.6 只剩测试调用的便捷入口 | -122 | +78 | 0 |
| `f79ccbc9` | 2.3 desktop_action 白名单残留 | -163 | -273 | -3 |
| `5218ab6f` | 2.1 重启恢复等待项链路 | -461 | -300 | 0 |
| 合计 | | **-2,404** | **-1,065** | **-3,970** |

### 与计划的差异

- 1.3：`runtime/session.ContainsString` 还被另一个测试别名 `containsString` 使用，没有变成死代码，保留。
- 2.5：`server/push_payloads.go` 中仍在使用的 `catalogUpdatedPushPayload` 保留；run.started/run.finished 的测试迁到 `runtime/query`，测的是生产实际使用的那份实现。
- 2.6 以下几项保留不动：
  - `mcp.NewRegistryWithSources`、`modelclient.ResponseError/ClassifyResponseError`、`query.Admission`：跨包测试在用，生产没有等价的公开入口。
  - `connector.ReadMount`：从 `8e7bf1a1` 引入时就只给测试用。生产的完整性校验走 `RuntimeFingerprint`，不是漏掉的校验。
  - `catalog.skillPackageVersionDiagnostics`：调用方在 `87d96b77`（嵌套技能包重构）中被删，此后不再产出 `missing_skill_version`、`skill_package_version_mismatch` 诊断，**疑似重构遗漏，待确认是恢复调用还是删除**。
- 2.1 补充：`ClaimAwaitingResolution` 改名为 `AwaitingResolutionOwner`，只返回状态；它和 `AwaitingHasLiveExecutor` 都去掉了不再使用的 `awaitingID` 参数。
- 删除 server/query 里的 `var` 别名后，`runexec.IsDiscardIncompleteModelTurnRecovery` 只剩 server 测试的 collector 在用（以前靠别名显得"生产可达"）。跨包测试使用，保留。

### 验证

- 每个提交都在 darwin、linux、windows 上通过 `go build` 和 `go vet ./...`（含测试编译）。
- 收尾时复跑 deadcode（三平台）和 staticcheck U1000：除三级的 hostshell 预制件和 `runops` 中作为编译期断言的 `func _` 外，没有完全无引用的函数，也没有 U1000 告警。
- 完整 `go test ./...` 中出现的失败，都确认在单独运行或基线上同样存在，与本次清理无关：
  - `hostenv.TestNPMDiscoveryRefreshAndIsolation`、`connectorauth.TestCLIOrderedAuthStepsAndJSONStatus`、`TestPreparationExternalCommandWithoutInit`：整套并发运行时超时，单独运行通过。
  - `server.TestChatHistoryHonorsDetailLevel`：基线上 40 次失败 4 次，属于原本就有的时序竞争。
  - `server.TestQuerySSEPersistsChatHistory`、`TestProxyQueryStreamingPersistsCapturedRunStartAndLifecyclePushTimes`：只在整套并发运行时偶发，基线和清理后各单独运行 40 次都通过。
- 跨仓契约测试（需 `DESKTOP_SOURCE=…/zenmind-desktop`）在基线上就失败，见下一节。

### 清理中发现、未在本次处理的问题

1. `TestDesktopActionContractMatchesDesktopSource` 在基线上已失败：Desktop 上游删除了部分 page-only 动作（如 `desktop.capabilities.list`、`desktop.native.dialog.selectDirectory`），`desktopWebappPageOnlyActions` 需要同步。另外，测试按相邻目录 `../desktop` 查找，而本机 checkout 是 `zenmind-desktop`，默认会跳过。
2. 上面列出的几个 server 测试存在时序竞争（`/api/chat` 在 Run 收尾落盘前读取）。
3. EXECUTOR 模板的推荐工具列表（已删除的 `executor.CreateToolNames`）从未接入创建选项，`handler_agent_defaults.go` 只给 general、kbase、coder 提供 baseTools。
4. `runtime/query/mailbox.go` 中 `DeliverMail` 收到的 `ctx` 在使用前就被覆盖（staticcheck SA4009）。
5. `skillPackageVersionDiagnostics` 疑似重构遗漏（见上文）。
