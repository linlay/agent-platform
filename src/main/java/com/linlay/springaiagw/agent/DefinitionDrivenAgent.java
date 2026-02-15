package com.linlay.springaiagw.agent;

import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.mode.AgentMode;
import com.linlay.springaiagw.agent.mode.OneshotMode;
import com.linlay.springaiagw.agent.mode.PlanExecuteMode;
import com.linlay.springaiagw.agent.mode.ReactMode;
import com.linlay.springaiagw.agent.mode.StageSettings;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.PlanExecutionStalledException;
import com.linlay.springaiagw.agent.runtime.ToolExecutionService;
import com.linlay.springaiagw.agent.runtime.VerifyService;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.mode.OrchestratorServices;
import com.linlay.springaiagw.memory.ChatWindowMemoryStore;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.FrontendSubmitCoordinator;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.CapabilityDescriptor;
import com.linlay.springaiagw.tool.CapabilityKind;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.Message;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;
import reactor.core.publisher.FluxSink;
import reactor.core.scheduler.Schedulers;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.UUID;

public class DefinitionDrivenAgent implements Agent {

    private static final Logger log = LoggerFactory.getLogger(DefinitionDrivenAgent.class);

    private final AgentDefinition definition;
    private final ChatWindowMemoryStore chatWindowMemoryStore;
    private final ToolRegistry toolRegistry;
    private final Map<String, BaseTool> enabledToolsByName;
    private final OrchestratorServices services;
    private final ObjectMapper objectMapper;

    @SuppressWarnings("unused")
    private final DeltaStreamService deltaStreamService;

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore,
            FrontendSubmitCoordinator frontendSubmitCoordinator
    ) {
        this.definition = definition;
        this.deltaStreamService = deltaStreamService;
        this.toolRegistry = toolRegistry;
        this.chatWindowMemoryStore = chatWindowMemoryStore;
        this.objectMapper = objectMapper;
        this.enabledToolsByName = resolveEnabledTools(definition.tools());

        ToolArgumentResolver argumentResolver = new ToolArgumentResolver(objectMapper);
        ToolExecutionService toolExecutionService = new ToolExecutionService(
                toolRegistry,
                argumentResolver,
                objectMapper,
                frontendSubmitCoordinator
        );
        VerifyService verifyService = new VerifyService(llmService);
        this.services = new OrchestratorServices(llmService, toolExecutionService, verifyService, objectMapper);
    }

    @Override
    public String id() {
        return definition.id();
    }

    @Override
    public String description() {
        return definition.description();
    }

    @Override
    public String name() {
        return definition.name() == null || definition.name().isBlank() ? definition.id() : definition.name();
    }

    @Override
    public String icon() {
        return definition.icon();
    }

    @Override
    public String providerKey() {
        return definition.providerKey();
    }

    @Override
    public String model() {
        return definition.model();
    }

    @Override
    public String systemPrompt() {
        return definition.systemPrompt();
    }

    @Override
    public AgentRuntimeMode mode() {
        return definition.mode();
    }

    @Override
    public List<String> tools() {
        return definition.tools();
    }

    @Override
    public Flux<AgentDelta> stream(AgentRequest request) {
        log.info(
                "[agent:{}] stream start provider={}, model={}, mode={}, tools={}, message={}",
                id(),
                providerKey(),
                model(),
                mode(),
                enabledToolsByName.keySet(),
                normalize(request.message(), "")
        );
        logRunSnapshot(request);

        return Flux.defer(() -> {
                    List<Message> historyMessages = loadHistoryMessages(request.chatId());
                    ChatWindowMemoryStore.PlanSnapshot latestPlanSnapshot = loadLatestPlanSnapshot(request.chatId());
                    TurnTrace trace = new TurnTrace();
                    ExecutionContext context = new ExecutionContext(definition, request, historyMessages);
                    if (latestPlanSnapshot != null) {
                        context.initializePlan(latestPlanSnapshot.planId, toPlanTasks(latestPlanSnapshot.plan));
                    }
                    return Flux.<AgentDelta>create(sink -> runWithMode(context, sink), FluxSink.OverflowStrategy.BUFFER)
                            .doOnNext(trace::capture)
                            .doOnComplete(() -> persistTurn(request, trace));
                })
                .subscribeOn(Schedulers.boundedElastic());
    }

    private void runWithMode(ExecutionContext context, FluxSink<AgentDelta> sink) {
        try {
            if (context.hasPlan()) {
                services.emit(sink, AgentDelta.planUpdate(context.planId(), context.request().chatId(), context.planTasks()));
            }
            definition.agentMode().run(context, enabledToolsByName, services, sink);
            services.emit(sink, AgentDelta.finish("stop"));
            if (!sink.isCancelled()) {
                sink.complete();
            }
        } catch (PlanExecutionStalledException ex) {
            log.warn("[agent:{}] plan execution stalled", definition.id(), ex);
            services.emit(sink, AgentDelta.content(ex.getMessage()));
            services.emit(sink, AgentDelta.finish("stop"));
            if (!sink.isCancelled()) {
                sink.complete();
            }
        } catch (Exception ex) {
            log.warn("[agent:{}] orchestration failed", definition.id(), ex);
            services.emit(sink, AgentDelta.content("模型调用失败，请稍后重试。"));
            services.emit(sink, AgentDelta.finish("stop"));
            if (!sink.isCancelled()) {
                sink.complete();
            }
        }
    }

    private Map<String, BaseTool> resolveEnabledTools(List<String> configuredTools) {
        Map<String, BaseTool> allToolsByName = new LinkedHashMap<>();
        for (BaseTool tool : toolRegistry.list()) {
            allToolsByName.put(normalizeToolName(tool.name()), tool);
        }

        List<String> requested = configuredTools == null ? List.of() : configuredTools;
        Map<String, BaseTool> enabled = new LinkedHashMap<>();
        for (String rawName : requested) {
            String name = normalizeToolName(rawName);
            if (name.isBlank()) {
                continue;
            }
            BaseTool tool = allToolsByName.get(name);
            if (tool == null) {
                log.warn("[agent:{}] configured tool not found and will be ignored: {}", id(), name);
                continue;
            }
            enabled.put(name, tool);
        }
        return Map.copyOf(enabled);
    }

    private String normalizeToolName(String raw) {
        return normalize(raw, "").trim().toLowerCase(Locale.ROOT);
    }

    private String sanitize(String input) {
        return normalize(input, "tool").replaceAll("[^a-zA-Z0-9_]", "_").toLowerCase(Locale.ROOT);
    }

    private List<Message> loadHistoryMessages(String chatId) {
        if (chatWindowMemoryStore == null || !StringUtils.hasText(chatId)) {
            return List.of();
        }
        try {
            return chatWindowMemoryStore.loadHistoryMessages(chatId);
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to load chat history chatId={}", id(), chatId, ex);
            return List.of();
        }
    }

    private ChatWindowMemoryStore.PlanSnapshot loadLatestPlanSnapshot(String chatId) {
        if (chatWindowMemoryStore == null || !StringUtils.hasText(chatId)) {
            return null;
        }
        try {
            return chatWindowMemoryStore.loadLatestPlanSnapshot(chatId);
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to load latest plan snapshot chatId={}", id(), chatId, ex);
            return null;
        }
    }

    private List<AgentDelta.PlanTask> toPlanTasks(List<ChatWindowMemoryStore.PlanTaskSnapshot> snapshotTasks) {
        if (snapshotTasks == null || snapshotTasks.isEmpty()) {
            return List.of();
        }
        List<AgentDelta.PlanTask> tasks = new ArrayList<>();
        for (ChatWindowMemoryStore.PlanTaskSnapshot task : snapshotTasks) {
            if (task == null || !StringUtils.hasText(task.taskId) || !StringUtils.hasText(task.description)) {
                continue;
            }
            tasks.add(new AgentDelta.PlanTask(task.taskId.trim(), task.description.trim(), normalizeStatus(task.status)));
        }
        return List.copyOf(tasks);
    }

    private void persistTurn(AgentRequest request, TurnTrace trace) {
        if (chatWindowMemoryStore == null || !StringUtils.hasText(request.chatId())) {
            return;
        }
        try {
            List<ChatWindowMemoryStore.RunMessage> runMessages = new ArrayList<>();
            if (StringUtils.hasText(request.message())) {
                runMessages.add(ChatWindowMemoryStore.RunMessage.user(request.message(), System.currentTimeMillis()));
            }
            runMessages.addAll(trace.runMessages());
            if (runMessages.isEmpty()) {
                return;
            }
            String runId = resolveRunId(request);
            chatWindowMemoryStore.appendRun(
                    request.chatId(),
                    runId,
                    request.query(),
                    buildSystemSnapshot(request),
                    trace.planSnapshot(),
                    runMessages
            );
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to persist chat turn chatId={}", id(), request.chatId(), ex);
        }
    }

    private String resolveRunId(AgentRequest request) {
        if (StringUtils.hasText(request.runId())) {
            return request.runId().trim();
        }
        return UUID.randomUUID().toString();
    }

    private ChatWindowMemoryStore.SystemSnapshot buildSystemSnapshot(AgentRequest request) {
        ChatWindowMemoryStore.SystemSnapshot snapshot = new ChatWindowMemoryStore.SystemSnapshot();
        snapshot.model = model();

        if (StringUtils.hasText(systemPrompt())) {
            ChatWindowMemoryStore.SystemMessageSnapshot systemMessage = new ChatWindowMemoryStore.SystemMessageSnapshot();
            systemMessage.role = "system";
            systemMessage.content = systemPrompt();
            snapshot.messages = List.of(systemMessage);
        }

        if (!enabledToolsByName.isEmpty()) {
            List<ChatWindowMemoryStore.SystemToolSnapshot> tools = enabledToolsByName.values().stream()
                    .sorted(java.util.Comparator.comparing(BaseTool::name))
                    .map(tool -> {
                        ChatWindowMemoryStore.SystemToolSnapshot item = new ChatWindowMemoryStore.SystemToolSnapshot();
                        item.type = toolRegistry.toolCallType(tool.name());
                        item.name = tool.name();
                        item.description = tool.description();
                        item.parameters = tool.parametersSchema();
                        return item;
                    })
                    .toList();
            if (!tools.isEmpty()) {
                snapshot.tools = tools;
            }
        }

        snapshot.stream = resolveStreamFlag(request);
        return snapshot;
    }

    private boolean resolveStreamFlag(AgentRequest request) {
        if (request == null || request.query() == null || !request.query().containsKey("stream")) {
            return true;
        }
        Object stream = request.query().get("stream");
        if (stream instanceof Boolean value) {
            return value;
        }
        if (stream instanceof Number value) {
            return value.intValue() != 0;
        }
        if (stream instanceof String value && !value.isBlank()) {
            return Boolean.parseBoolean(value.trim());
        }
        return true;
    }

    private Map<String, Object> usagePlaceholder() {
        Map<String, Object> usage = new LinkedHashMap<>();
        usage.put("input_tokens", null);
        usage.put("output_tokens", null);
        usage.put("total_tokens", null);
        return usage;
    }

    private Long durationOrNull(long startTs, long endTs) {
        if (startTs <= 0 || endTs < startTs) {
            return null;
        }
        return endTs - startTs;
    }

    private String generateToolCallId() {
        return "call_" + UUID.randomUUID().toString().replace("-", "").substring(0, 16);
    }

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }

    private String normalizeStatus(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "init";
        }
        String normalized = raw.trim().toLowerCase(Locale.ROOT);
        return switch (normalized) {
            case "in_progress" -> "init";
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
    }

    private void logRunSnapshot(AgentRequest request) {
        Map<String, Object> snapshot = new LinkedHashMap<>();
        snapshot.put("request", requestSnapshot(request));
        snapshot.put("agent", agentSnapshot());
        snapshot.put("policy", policySnapshot());
        snapshot.put("stages", stageSnapshot());
        snapshot.put("tools", toolsSnapshot());
        try {
            String pretty = objectMapper.writerWithDefaultPrettyPrinter().writeValueAsString(snapshot);
            log.info("[agent:{}] run snapshot:\n{}", id(), pretty);
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to serialize run snapshot", id(), ex);
        }
    }

    private Map<String, Object> requestSnapshot(AgentRequest request) {
        Map<String, Object> item = new LinkedHashMap<>();
        item.put("runId", request == null ? null : request.runId());
        item.put("chatId", request == null ? null : request.chatId());
        item.put("requestId", request == null ? null : request.requestId());
        item.put("message", request == null ? null : request.message());
        return item;
    }

    private Map<String, Object> agentSnapshot() {
        Map<String, Object> item = new LinkedHashMap<>();
        item.put("id", definition.id());
        item.put("name", definition.name());
        item.put("mode", definition.mode());
        item.put("provider", definition.providerKey());
        item.put("model", definition.model());
        return item;
    }

    private Map<String, Object> policySnapshot() {
        RunSpec runSpec = definition.runSpec();
        Map<String, Object> item = new LinkedHashMap<>();
        item.put("control", runSpec.control());
        item.put("output", runSpec.output());
        item.put("toolPolicy", runSpec.toolPolicy());
        item.put("verify", runSpec.verify());
        Budget budget = runSpec.budget();
        Map<String, Object> budgetMap = new LinkedHashMap<>();
        budgetMap.put("maxModelCalls", budget.maxModelCalls());
        budgetMap.put("maxToolCalls", budget.maxToolCalls());
        budgetMap.put("maxSteps", budget.maxSteps());
        budgetMap.put("timeoutMs", budget.timeoutMs());
        item.put("budget", budgetMap);
        return item;
    }

    private Map<String, Object> stageSnapshot() {
        Map<String, Object> stages = new LinkedHashMap<>();
        AgentMode modeImpl = definition.agentMode();
        if (modeImpl instanceof OneshotMode oneshotMode) {
            stages.put("oneshot", singleStageSnapshot(oneshotMode.stage()));
            return stages;
        }
        if (modeImpl instanceof ReactMode reactMode) {
            stages.put("react", singleStageSnapshot(reactMode.stage()));
            return stages;
        }
        if (modeImpl instanceof PlanExecuteMode planExecuteMode) {
            stages.put("plan", singleStageSnapshot(planExecuteMode.planStage()));
            stages.put("execute", singleStageSnapshot(planExecuteMode.executeStage()));
            stages.put("summary", singleStageSnapshot(planExecuteMode.summaryStage()));
            return stages;
        }
        return stages;
    }

    private Map<String, Object> singleStageSnapshot(StageSettings stage) {
        Map<String, Object> item = new LinkedHashMap<>();
        if (stage == null) {
            return item;
        }
        item.put("provider", normalize(stage.providerKey(), definition.providerKey()));
        item.put("model", normalize(stage.model(), definition.model()));
        item.put("systemPrompt", stage.systemPrompt());
        item.put("deepThinking", stage.deepThinking());
        item.put("reasoningEnabled", stage.reasoningEnabled());
        item.put("reasoningEffort", stage.reasoningEffort());
        item.put("tools", groupToolNames(stage.tools()));
        return item;
    }

    private Map<String, Object> toolsSnapshot() {
        Map<String, Object> snapshot = new LinkedHashMap<>();
        snapshot.put("enabled", groupToolNames(enabledToolsByName.keySet()));

        Map<String, Object> stageTools = new LinkedHashMap<>();
        AgentMode modeImpl = definition.agentMode();
        if (modeImpl instanceof OneshotMode oneshotMode) {
            stageTools.put("oneshot", groupToolNames(oneshotMode.stage().tools()));
        } else if (modeImpl instanceof ReactMode reactMode) {
            stageTools.put("react", groupToolNames(reactMode.stage().tools()));
        } else if (modeImpl instanceof PlanExecuteMode planExecuteMode) {
            stageTools.put("plan", groupToolNames(planExecuteMode.planStage().tools()));
            stageTools.put("execute", groupToolNames(planExecuteMode.executeStage().tools()));
            stageTools.put("summary", groupToolNames(planExecuteMode.summaryStage().tools()));
        }
        snapshot.put("stageTools", stageTools);
        return snapshot;
    }

    private Map<String, Object> groupToolNames(Iterable<String> toolNames) {
        List<String> backend = new ArrayList<>();
        List<String> frontend = new ArrayList<>();
        List<String> action = new ArrayList<>();

        if (toolNames != null) {
            for (String raw : toolNames) {
                String name = normalizeToolName(raw);
                if (!StringUtils.hasText(name)) {
                    continue;
                }
                switch (resolveToolGroup(name)) {
                    case "action" -> action.add(name);
                    case "frontend" -> frontend.add(name);
                    default -> backend.add(name);
                }
            }
        }

        backend = backend.stream().distinct().sorted().toList();
        frontend = frontend.stream().distinct().sorted().toList();
        action = action.stream().distinct().sorted().toList();

        Map<String, Object> grouped = new LinkedHashMap<>();
        grouped.put("backend", backend);
        grouped.put("frontend", frontend);
        grouped.put("action", action);
        return grouped;
    }

    private String resolveToolGroup(String toolName) {
        CapabilityDescriptor descriptor = toolRegistry.capability(toolName).orElse(null);
        if (descriptor != null && descriptor.kind() != null) {
            CapabilityKind kind = descriptor.kind();
            if (kind == CapabilityKind.ACTION) {
                return "action";
            }
            if (kind == CapabilityKind.FRONTEND) {
                return "frontend";
            }
            return "backend";
        }
        String callType = normalize(toolRegistry.toolCallType(toolName), "function").toLowerCase(Locale.ROOT);
        if ("action".equals(callType)) {
            return "action";
        }
        if ("frontend".equals(callType)) {
            return "frontend";
        }
        return "backend";
    }

    private final class TurnTrace {
        private final StringBuilder pendingReasoning = new StringBuilder();
        private long pendingReasoningStartedAt;
        private final StringBuilder pendingAssistant = new StringBuilder();
        private long pendingAssistantStartedAt;
        private final List<ChatWindowMemoryStore.RunMessage> orderedMessages = new ArrayList<>();
        private final Map<String, ToolTrace> toolByCallId = new LinkedHashMap<>();
        private ChatWindowMemoryStore.PlanSnapshot latestPlanSnapshot;

        private void capture(AgentDelta delta) {
            if (delta == null) {
                return;
            }
            long now = System.currentTimeMillis();

            if (StringUtils.hasText(delta.reasoning())) {
                if (pendingReasoningStartedAt <= 0) {
                    pendingReasoningStartedAt = now;
                }
                pendingReasoning.append(delta.reasoning());
            }

            if (StringUtils.hasText(delta.content())) {
                if (pendingAssistantStartedAt <= 0) {
                    pendingAssistantStartedAt = now;
                }
                pendingAssistant.append(delta.content());
            }

            if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
                flushReasoning(now);
                flushAssistantContent();
                for (ToolCallDelta toolCall : delta.toolCalls()) {
                    if (toolCall == null) {
                        continue;
                    }
                    String toolCallId = StringUtils.hasText(toolCall.id()) ? toolCall.id() : generateToolCallId();
                    ToolTrace trace = toolByCallId.computeIfAbsent(toolCallId, ToolTrace::new);
                    if (trace.firstSeenAt <= 0) {
                        trace.firstSeenAt = now;
                    }
                    if (StringUtils.hasText(toolCall.name())) {
                        trace.toolName = toolCall.name();
                    }
                    if (StringUtils.hasText(toolCall.type())) {
                        trace.toolType = toolCall.type();
                    }
                    if (StringUtils.hasText(toolCall.arguments())) {
                        trace.appendArguments(toolCall.arguments());
                    }
                }
            }

            if (delta.toolResults() != null && !delta.toolResults().isEmpty()) {
                flushReasoning(now);
                flushAssistantContent();
                for (AgentDelta.ToolResult toolResult : delta.toolResults()) {
                    if (toolResult == null || !StringUtils.hasText(toolResult.toolId())) {
                        continue;
                    }
                    ToolTrace trace = toolByCallId.computeIfAbsent(toolResult.toolId(), ToolTrace::new);
                    if (trace.firstSeenAt <= 0) {
                        trace.firstSeenAt = now;
                    }
                    trace.resultAt = now;
                    appendAssistantToolCallIfNeeded(trace, now);
                    String result = StringUtils.hasText(toolResult.result()) ? toolResult.result() : "null";
                    orderedMessages.add(ChatWindowMemoryStore.RunMessage.toolResult(
                            trace.toolName,
                            trace.toolCallId,
                            result,
                            now,
                            durationOrNull(trace.firstSeenAt, now)
                    ));
                }
            }

            if (delta.planUpdate() != null) {
                latestPlanSnapshot = toPlanSnapshot(delta.planUpdate());
            }
        }

        private List<ChatWindowMemoryStore.RunMessage> runMessages() {
            long now = System.currentTimeMillis();
            flushReasoning(now);
            flushAssistantContent();
            toolByCallId.values().forEach(toolTrace -> appendAssistantToolCallIfNeeded(
                    toolTrace,
                    toolTrace.resultAt > 0 ? toolTrace.resultAt : now
            ));
            return List.copyOf(orderedMessages);
        }

        private ChatWindowMemoryStore.PlanSnapshot planSnapshot() {
            return latestPlanSnapshot;
        }

        private void appendAssistantToolCallIfNeeded(ToolTrace trace, long ts) {
            if (trace == null || trace.recorded) {
                return;
            }
            if (!StringUtils.hasText(trace.toolName)) {
                return;
            }
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantToolCall(
                    trace.toolName,
                    trace.toolCallId,
                    trace.toolType,
                    trace.arguments(),
                    ts,
                    durationOrNull(trace.firstSeenAt, trace.resultAt > 0 ? trace.resultAt : ts),
                    usagePlaceholder()
            ));
            trace.recorded = true;
        }

        private void flushReasoning(long now) {
            if (!StringUtils.hasText(pendingReasoning)) {
                return;
            }
            long startedAt = pendingReasoningStartedAt > 0 ? pendingReasoningStartedAt : now;
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantReasoning(
                    pendingReasoning.toString(),
                    startedAt,
                    durationOrNull(startedAt, now),
                    usagePlaceholder()
            ));
            pendingReasoning.setLength(0);
            pendingReasoningStartedAt = 0L;
        }

        private void flushAssistantContent() {
            if (!StringUtils.hasText(pendingAssistant)) {
                return;
            }
            long now = System.currentTimeMillis();
            long startedAt = pendingAssistantStartedAt > 0 ? pendingAssistantStartedAt : now;
            orderedMessages.add(ChatWindowMemoryStore.RunMessage.assistantContent(
                    pendingAssistant.toString(),
                    startedAt,
                    durationOrNull(startedAt, now),
                    usagePlaceholder()
            ));
            pendingAssistant.setLength(0);
            pendingAssistantStartedAt = 0L;
        }

        private ChatWindowMemoryStore.PlanSnapshot toPlanSnapshot(AgentDelta.PlanUpdate planUpdate) {
            if (planUpdate == null || !StringUtils.hasText(planUpdate.planId()) || planUpdate.plan() == null || planUpdate.plan().isEmpty()) {
                return null;
            }
            List<ChatWindowMemoryStore.PlanTaskSnapshot> tasks = new ArrayList<>();
            for (AgentDelta.PlanTask task : planUpdate.plan()) {
                if (task == null || !StringUtils.hasText(task.taskId()) || !StringUtils.hasText(task.description())) {
                    continue;
                }
                ChatWindowMemoryStore.PlanTaskSnapshot item = new ChatWindowMemoryStore.PlanTaskSnapshot();
                item.taskId = task.taskId().trim();
                item.description = task.description().trim();
                item.status = normalizeStatus(task.status());
                tasks.add(item);
            }
            if (tasks.isEmpty()) {
                return null;
            }
            ChatWindowMemoryStore.PlanSnapshot snapshot = new ChatWindowMemoryStore.PlanSnapshot();
            snapshot.planId = planUpdate.planId().trim();
            snapshot.plan = List.copyOf(tasks);
            return snapshot;
        }
    }

    private static final class ToolTrace {
        private final String toolCallId;
        private String toolName;
        private String toolType;
        private final StringBuilder arguments = new StringBuilder();
        private long firstSeenAt;
        private long resultAt;
        private boolean recorded;

        private ToolTrace(String toolCallId) {
            this.toolCallId = Objects.requireNonNull(toolCallId);
        }

        private void appendArguments(String delta) {
            if (StringUtils.hasText(delta)) {
                this.arguments.append(delta);
            }
        }

        private String arguments() {
            return arguments.isEmpty() ? null : arguments.toString();
        }
    }
}
