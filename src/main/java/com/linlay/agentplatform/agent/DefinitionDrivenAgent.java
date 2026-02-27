package com.linlay.agentplatform.agent;

import com.linlay.agentplatform.stream.model.ToolCallDelta;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.agent.mode.AgentMode;
import com.linlay.agentplatform.agent.mode.OneshotMode;
import com.linlay.agentplatform.agent.mode.PlanExecuteMode;
import com.linlay.agentplatform.agent.mode.ReactMode;
import com.linlay.agentplatform.agent.mode.StageSettings;
import com.linlay.agentplatform.agent.runtime.AgentRuntimeMode;
import com.linlay.agentplatform.agent.runtime.ExecutionContext;
import com.linlay.agentplatform.agent.runtime.FrontendSubmitTimeoutException;
import com.linlay.agentplatform.agent.runtime.ToolExecutionService;
import com.linlay.agentplatform.agent.runtime.policy.Budget;
import com.linlay.agentplatform.agent.runtime.policy.RunSpec;
import com.linlay.agentplatform.agent.mode.OrchestratorServices;
import com.linlay.agentplatform.memory.ChatWindowMemoryStore;
import com.linlay.agentplatform.model.AgentRequest;
import com.linlay.agentplatform.model.AgentDelta;
import com.linlay.agentplatform.service.FrontendSubmitCoordinator;
import com.linlay.agentplatform.service.LlmService;
import com.linlay.agentplatform.service.RunIdGenerator;
import com.linlay.agentplatform.skill.SkillDescriptor;
import com.linlay.agentplatform.skill.SkillRegistryService;
import com.linlay.agentplatform.tool.BaseTool;
import com.linlay.agentplatform.tool.CapabilityDescriptor;
import com.linlay.agentplatform.tool.CapabilityKind;
import com.linlay.agentplatform.tool.ToolRegistry;
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
    private static final String FRONTEND_TIMEOUT_FALLBACK_MESSAGE = "前端工具等待用户提交超时，本次运行已结束。请重新发起或在超时前提交。";

    private final AgentDefinition definition;
    private final ChatWindowMemoryStore chatWindowMemoryStore;
    private final ToolRegistry toolRegistry;
    private final Map<String, BaseTool> enabledToolsByName;
    private final OrchestratorServices services;
    private final ObjectMapper objectMapper;
    private final SkillRegistryService skillRegistryService;

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore,
            FrontendSubmitCoordinator frontendSubmitCoordinator
    ) {
        this(
                definition,
                llmService,
                toolRegistry,
                objectMapper,
                chatWindowMemoryStore,
                frontendSubmitCoordinator,
                null
        );
    }

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore,
            FrontendSubmitCoordinator frontendSubmitCoordinator,
            SkillRegistryService skillRegistryService
    ) {
        this.definition = definition;
        this.toolRegistry = toolRegistry;
        this.chatWindowMemoryStore = chatWindowMemoryStore;
        this.objectMapper = objectMapper;
        this.skillRegistryService = skillRegistryService;
        this.enabledToolsByName = resolveEnabledTools(definition.tools());

        ToolArgumentResolver argumentResolver = new ToolArgumentResolver(objectMapper);
        ToolExecutionService toolExecutionService = new ToolExecutionService(
                toolRegistry,
                argumentResolver,
                objectMapper,
                frontendSubmitCoordinator
        );
        this.services = new OrchestratorServices(llmService, toolExecutionService, objectMapper);
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
    public List<String> skills() {
        return definition.skills();
    }

    @Override
    public Flux<AgentDelta> stream(AgentRequest request) {
        log.info(
                "[agent:{}] stream start provider={}, model={}, mode={}, tools={}, skills={}, message={}",
                id(),
                providerKey(),
                model(),
                mode(),
                enabledToolsByName.keySet(),
                definition.skills(),
                normalize(request.message(), "")
        );
        logRunSnapshot(request);

        return Flux.defer(() -> {
                    List<Message> historyMessages = loadHistoryMessages(request.chatId());
                    ChatWindowMemoryStore.PlanSnapshot latestPlanSnapshot = loadLatestPlanSnapshot(request.chatId());
                    ChatWindowMemoryStore.SystemSnapshot latestSystem = loadLatestSystemSnapshot(request.chatId());
                    String runId = resolveRunId(request);
                    TurnTrace trace = new TurnTrace(request, runId, latestSystem);
                    SkillPromptBundle skillPromptBundle = resolveSkillPrompts();
                    ExecutionContext context = new ExecutionContext(
                            definition,
                            request,
                            historyMessages,
                            skillPromptBundle.catalogPrompt(),
                            skillPromptBundle.resolvedSkillsById()
                    );
                    if (latestPlanSnapshot != null) {
                        context.initializePlan(latestPlanSnapshot.planId, toPlanTasks(latestPlanSnapshot.tasks));
                    }
                    return Flux.<AgentDelta>create(sink -> runWithMode(context, sink), FluxSink.OverflowStrategy.BUFFER)
                            .doOnNext(trace::capture)
                            .doOnComplete(() -> finalizeTrace(request, trace));
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
        } catch (FrontendSubmitTimeoutException ex) {
            log.info("[agent:{}] frontend submit timeout: {}", definition.id(), ex.getMessage());
            String timeoutMessage = resolveFrontendTimeoutMessage(ex);
            services.emit(sink, AgentDelta.content(timeoutMessage));
            services.emit(sink, AgentDelta.finish("timeout"));
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

    private String resolveFrontendTimeoutMessage(FrontendSubmitTimeoutException ex) {
        if (ex == null || !StringUtils.hasText(ex.getMessage())) {
            return FRONTEND_TIMEOUT_FALLBACK_MESSAGE;
        }
        String raw = ex.getMessage().trim();
        if (raw.contains("Frontend tool submit timeout")) {
            return FRONTEND_TIMEOUT_FALLBACK_MESSAGE;
        }
        return raw;
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

    private SkillPromptBundle resolveSkillPrompts() {
        if (skillRegistryService == null || definition.skills().isEmpty()) {
            return new SkillPromptBundle("", Map.of());
        }
        List<String> catalogBlocks = new ArrayList<>();
        Map<String, SkillDescriptor> resolvedSkillsById = new LinkedHashMap<>();
        for (String configuredSkill : definition.skills()) {
            String skillId = normalize(configuredSkill, "").trim().toLowerCase(Locale.ROOT);
            if (!StringUtils.hasText(skillId)) {
                continue;
            }
            SkillDescriptor descriptor = skillRegistryService.find(skillId).orElse(null);
            if (descriptor == null) {
                log.warn("[agent:{}] configured skill not found and will be ignored: {}", id(), skillId);
                continue;
            }
            resolvedSkillsById.putIfAbsent(descriptor.id(), descriptor);
            StringBuilder block = new StringBuilder();
            block.append("skillId: ").append(descriptor.id());
            if (StringUtils.hasText(descriptor.name())) {
                block.append("\nname: ").append(descriptor.name());
            }
            if (StringUtils.hasText(descriptor.description())) {
                block.append("\ndescription: ").append(descriptor.description());
            }
            catalogBlocks.add(block.toString());
        }
        if (catalogBlocks.isEmpty()) {
            return new SkillPromptBundle("", Map.copyOf(resolvedSkillsById));
        }
        String catalogPrompt = definition.agentMode().skillAppend().catalogHeader() + "\n\n"
                + String.join("\n\n---\n\n", catalogBlocks);
        return new SkillPromptBundle(catalogPrompt, Map.copyOf(resolvedSkillsById));
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

    private ChatWindowMemoryStore.SystemSnapshot loadLatestSystemSnapshot(String chatId) {
        if (chatWindowMemoryStore == null || !StringUtils.hasText(chatId)) {
            return null;
        }
        try {
            return chatWindowMemoryStore.loadLatestSystemSnapshot(chatId);
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to load latest system snapshot chatId={}", id(), chatId, ex);
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

    private void finalizeTrace(AgentRequest request, TurnTrace trace) {
        if (chatWindowMemoryStore == null || !StringUtils.hasText(request.chatId())) {
            return;
        }
        try {
            trace.finalFlush();
            chatWindowMemoryStore.trimToWindow(request.chatId());
        } catch (Exception ex) {
            log.warn("[agent:{}] failed to finalize chat trace chatId={}", id(), request.chatId(), ex);
        }
    }

    private String resolveRunId(AgentRequest request) {
        if (StringUtils.hasText(request.runId())) {
            return request.runId().trim();
        }
        return RunIdGenerator.nextRunId();
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
        snapshot.put("skills", skillsSnapshot());
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
        item.put("skills", definition.skills());
        return item;
    }

    private Map<String, Object> policySnapshot() {
        RunSpec runSpec = definition.runSpec();
        Map<String, Object> item = new LinkedHashMap<>();
        item.put("toolChoice", runSpec.toolChoice());
        Budget budget = runSpec.budget();
        Map<String, Object> budgetMap = new LinkedHashMap<>();
        budgetMap.put("runTimeoutMs", budget.runTimeoutMs());

        Map<String, Object> modelBudget = new LinkedHashMap<>();
        modelBudget.put("maxCalls", budget.model().maxCalls());
        modelBudget.put("timeoutMs", budget.model().timeoutMs());
        modelBudget.put("retryCount", budget.model().retryCount());
        budgetMap.put("model", modelBudget);

        Map<String, Object> toolBudget = new LinkedHashMap<>();
        toolBudget.put("maxCalls", budget.tool().maxCalls());
        toolBudget.put("timeoutMs", budget.tool().timeoutMs());
        toolBudget.put("retryCount", budget.tool().retryCount());
        budgetMap.put("tool", toolBudget);
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
            stages.put("plan", singleStageSnapshot(planExecuteMode.planStage(), true));
            stages.put("execute", singleStageSnapshot(planExecuteMode.executeStage(), false));
            stages.put("summary", singleStageSnapshot(planExecuteMode.summaryStage(), false));
            return stages;
        }
        return stages;
    }

    private Map<String, Object> singleStageSnapshot(StageSettings stage) {
        return singleStageSnapshot(stage, true);
    }

    private Map<String, Object> singleStageSnapshot(StageSettings stage, boolean includeDeepThinking) {
        Map<String, Object> item = new LinkedHashMap<>();
        if (stage == null) {
            return item;
        }
        item.put("provider", normalize(stage.providerKey(), definition.providerKey()));
        item.put("model", normalize(stage.model(), definition.model()));
        item.put("systemPrompt", stage.systemPrompt());
        if (includeDeepThinking) {
            item.put("deepThinking", stage.deepThinking());
        }
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

    private Map<String, Object> skillsSnapshot() {
        Map<String, Object> snapshot = new LinkedHashMap<>();
        snapshot.put("configured", definition.skills());
        if (skillRegistryService == null || definition.skills().isEmpty()) {
            snapshot.put("resolved", List.of());
            return snapshot;
        }
        List<Map<String, Object>> resolved = new ArrayList<>();
        for (String configuredSkill : definition.skills()) {
            String skillId = normalize(configuredSkill, "").trim().toLowerCase(Locale.ROOT);
            if (!StringUtils.hasText(skillId)) {
                continue;
            }
            SkillDescriptor descriptor = skillRegistryService.find(skillId).orElse(null);
            if (descriptor == null) {
                continue;
            }
            Map<String, Object> item = new LinkedHashMap<>();
            item.put("id", descriptor.id());
            item.put("name", descriptor.name());
            item.put("description", descriptor.description());
            item.put("promptTruncated", descriptor.promptTruncated());
            resolved.add(item);
        }
        snapshot.put("resolved", resolved);
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
        private final AgentRequest request;
        private final String runId;
        private ChatWindowMemoryStore.SystemSnapshot lastWrittenSystem;
        private StepAccumulator currentStep;
        private int seqCounter = 0;
        private boolean queryLineWritten = false;
        private ChatWindowMemoryStore.PlanSnapshot latestPlan;

        private TurnTrace(AgentRequest request, String runId, ChatWindowMemoryStore.SystemSnapshot lastWrittenSystem) {
            this.request = request;
            this.runId = runId;
            this.lastWrittenSystem = lastWrittenSystem;
        }

        private void capture(AgentDelta delta) {
            if (delta == null) {
                return;
            }
            long now = System.currentTimeMillis();

            if (delta.stageMarker() != null) {
                String marker = delta.stageMarker().trim();
                String newStage = parseStage(marker);
                String newTaskId = parseTaskId(marker);

                // plan-draft and plan-generate merge into one "plan" step
                if ("plan".equals(newStage) && currentStep != null && "plan".equals(currentStep.stage)) {
                    // continue same plan step, don't flush
                    return;
                }

                flushCurrentStep();
                currentStep = new StepAccumulator(newStage, newTaskId);
                return;
            }

            if (currentStep == null) {
                currentStep = new StepAccumulator("oneshot", null);
            }

            if (StringUtils.hasText(delta.reasoning())) {
                if (currentStep.needNewMsgId) {
                    currentStep.currentMsgId = StepAccumulator.generateMsgId();
                    currentStep.needNewMsgId = false;
                }
                if (currentStep.pendingReasoningStartedAt <= 0) {
                    currentStep.pendingReasoningStartedAt = now;
                }
                currentStep.pendingReasoning.append(delta.reasoning());
            }

            if (StringUtils.hasText(delta.content())) {
                if (currentStep.needNewMsgId) {
                    currentStep.currentMsgId = StepAccumulator.generateMsgId();
                    currentStep.needNewMsgId = false;
                }
                if (currentStep.pendingAssistantStartedAt <= 0) {
                    currentStep.pendingAssistantStartedAt = now;
                }
                currentStep.pendingAssistant.append(delta.content());
            }

            if (delta.toolCalls() != null && !delta.toolCalls().isEmpty()) {
                currentStep.flushReasoning(now);
                currentStep.flushAssistantContent();
                for (ToolCallDelta toolCall : delta.toolCalls()) {
                    if (toolCall == null) {
                        continue;
                    }
                    String toolCallId = StringUtils.hasText(toolCall.id()) ? toolCall.id() : generateToolCallId();
                    ToolTrace trace = currentStep.toolByCallId.computeIfAbsent(toolCallId, ToolTrace::new);
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
                currentStep.flushReasoning(now);
                currentStep.flushAssistantContent();
                for (AgentDelta.ToolResult toolResult : delta.toolResults()) {
                    if (toolResult == null || !StringUtils.hasText(toolResult.toolId())) {
                        continue;
                    }
                    ToolTrace trace = currentStep.toolByCallId.computeIfAbsent(toolResult.toolId(), ToolTrace::new);
                    if (trace.firstSeenAt <= 0) {
                        trace.firstSeenAt = now;
                    }
                    trace.resultAt = now;
                    currentStep.appendAssistantToolCallIfNeeded(trace, now);
                    String result = StringUtils.hasText(toolResult.result()) ? toolResult.result() : "null";
                    currentStep.orderedMessages.add(ChatWindowMemoryStore.RunMessage.toolResult(
                            trace.toolName,
                            trace.toolCallId,
                            result,
                            now,
                            durationOrNull(trace.firstSeenAt, now)
                    ));
                }
                currentStep.needNewMsgId = true;
            }

            if (delta.planUpdate() != null) {
                latestPlan = toPlanSnapshot(delta.planUpdate());
                if (currentStep != null) {
                    currentStep.plan = latestPlan;
                }
            }

            if (delta.usage() != null && !delta.usage().isEmpty()) {
                if (currentStep != null) {
                    currentStep.capturedUsage = delta.usage();
                }
            }
        }

        private void finalFlush() {
            flushCurrentStep();
        }

        private void flushCurrentStep() {
            if (currentStep == null || currentStep.isEmpty()) {
                return;
            }
            if (chatWindowMemoryStore == null || !StringUtils.hasText(request.chatId())) {
                currentStep = null;
                return;
            }

            // Write query line on first flush
            if (!queryLineWritten) {
                chatWindowMemoryStore.appendQueryLine(request.chatId(), runId, request.query());
                queryLineWritten = true;
            }

            seqCounter++;

            // Build messages: prepend user message on first step
            List<ChatWindowMemoryStore.RunMessage> stepMessages = new ArrayList<>();
            if (seqCounter == 1 && StringUtils.hasText(request.message())) {
                stepMessages.add(ChatWindowMemoryStore.RunMessage.user(request.message(), System.currentTimeMillis()));
            }
            stepMessages.addAll(currentStep.runMessages());
            if (stepMessages.isEmpty()) {
                currentStep = null;
                return;
            }

            // Resolve system snapshot: write on first step or when changed
            ChatWindowMemoryStore.SystemSnapshot stepSystem = null;
            ChatWindowMemoryStore.SystemSnapshot currentSystem = buildSystemSnapshot(request);
            if (seqCounter == 1) {
                stepSystem = currentSystem;
            } else if (currentSystem != null && (lastWrittenSystem == null
                    || !chatWindowMemoryStore.isSameSystem(lastWrittenSystem, currentSystem))) {
                stepSystem = currentSystem;
            }

            chatWindowMemoryStore.appendStepLine(
                    request.chatId(),
                    runId,
                    currentStep.stage,
                    seqCounter,
                    currentStep.taskId,
                    stepSystem,
                    currentStep.plan,
                    stepMessages
            );

            if (stepSystem != null) {
                lastWrittenSystem = stepSystem;
            }
            currentStep = null;
        }

        private String parseStage(String marker) {
            if (marker.startsWith("react-step-")) {
                return "react";
            }
            if (marker.equals("plan-draft") || marker.equals("plan-generate")) {
                return "plan";
            }
            if (marker.startsWith("execute-task-")) {
                return "execute";
            }
            if (marker.equals("summary")) {
                return "summary";
            }
            return marker;
        }

        private String parseTaskId(String marker) {
            if (marker.startsWith("execute-task-")) {
                return null;
            }
            return null;
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
            snapshot.tasks = List.copyOf(tasks);
            return snapshot;
        }
    }

    private static final class StepAccumulator {
        private final String stage;
        private final String taskId;
        private final StringBuilder pendingReasoning = new StringBuilder();
        private long pendingReasoningStartedAt;
        private final StringBuilder pendingAssistant = new StringBuilder();
        private long pendingAssistantStartedAt;
        private final List<ChatWindowMemoryStore.RunMessage> orderedMessages = new ArrayList<>();
        private final Map<String, ToolTrace> toolByCallId = new LinkedHashMap<>();
        private ChatWindowMemoryStore.PlanSnapshot plan;
        private Map<String, Object> capturedUsage;
        private String currentMsgId;
        private boolean needNewMsgId;

        private StepAccumulator(String stage, String taskId) {
            this.stage = stage;
            this.taskId = taskId;
            this.currentMsgId = generateMsgId();
        }

        private static String generateMsgId() {
            return "m_" + UUID.randomUUID().toString().replace("-", "").substring(0, 8);
        }

        private boolean isEmpty() {
            return pendingReasoning.isEmpty()
                    && pendingAssistant.isEmpty()
                    && orderedMessages.isEmpty()
                    && toolByCallId.isEmpty();
        }

        private List<ChatWindowMemoryStore.RunMessage> runMessages() {
            long now = System.currentTimeMillis();
            flushReasoning(now);
            flushAssistantContent();
            toolByCallId.values().forEach(toolTrace -> appendAssistantToolCallIfNeeded(
                    toolTrace,
                    toolTrace.resultAt > 0 ? toolTrace.resultAt : now
            ));
            // Only attach usage to the last assistant message
            if (capturedUsage != null && !capturedUsage.isEmpty()) {
                for (int i = orderedMessages.size() - 1; i >= 0; i--) {
                    ChatWindowMemoryStore.RunMessage msg = orderedMessages.get(i);
                    if ("assistant".equals(msg.role())) {
                        orderedMessages.set(i, withUsage(msg, capturedUsage));
                        break;
                    }
                }
            }
            return List.copyOf(orderedMessages);
        }

        private static ChatWindowMemoryStore.RunMessage withUsage(
                ChatWindowMemoryStore.RunMessage original,
                Map<String, Object> usage
        ) {
            return new ChatWindowMemoryStore.RunMessage(
                    original.role(),
                    original.kind(),
                    original.text(),
                    original.name(),
                    original.toolCallId(),
                    original.toolCallType(),
                    original.toolArgs(),
                    original.reasoningId(),
                    original.contentId(),
                    original.msgId(),
                    original.ts(),
                    original.timing(),
                    usage
            );
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
                    currentMsgId,
                    ts,
                    trace.firstSeenAt > 0 && (trace.resultAt > 0 ? trace.resultAt : ts) >= trace.firstSeenAt
                            ? (trace.resultAt > 0 ? trace.resultAt : ts) - trace.firstSeenAt
                            : null,
                    null
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
                    currentMsgId,
                    startedAt,
                    startedAt > 0 && now >= startedAt ? now - startedAt : null,
                    null
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
                    currentMsgId,
                    startedAt,
                    startedAt > 0 && now >= startedAt ? now - startedAt : null,
                    null
            ));
            pendingAssistant.setLength(0);
            pendingAssistantStartedAt = 0L;
        }
    }

    private record SkillPromptBundle(
            String catalogPrompt,
            Map<String, SkillDescriptor> resolvedSkillsById
    ) {
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
