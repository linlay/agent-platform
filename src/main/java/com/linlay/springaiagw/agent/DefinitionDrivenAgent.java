package com.linlay.springaiagw.agent;

import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.runtime.AgentOrchestrator;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ToolExecutionService;
import com.linlay.springaiagw.agent.runtime.VerifyService;
import com.linlay.springaiagw.memory.ChatWindowMemoryStore;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.agw.AgentDelta;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.FrontendSubmitCoordinator;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.Message;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;
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
    private final AgentOrchestrator orchestrator;

    @SuppressWarnings("unused")
    private final DeltaStreamService deltaStreamService;

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper
    ) {
        this(definition, llmService, deltaStreamService, toolRegistry, objectMapper, null, null);
    }

    public DefinitionDrivenAgent(
            AgentDefinition definition,
            LlmService llmService,
            DeltaStreamService deltaStreamService,
            ToolRegistry toolRegistry,
            ObjectMapper objectMapper,
            ChatWindowMemoryStore chatWindowMemoryStore
    ) {
        this(definition, llmService, deltaStreamService, toolRegistry, objectMapper, chatWindowMemoryStore, null);
    }

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
        this.enabledToolsByName = resolveEnabledTools(definition.tools());

        ToolArgumentResolver argumentResolver = new ToolArgumentResolver(objectMapper);
        ToolExecutionService toolExecutionService = new ToolExecutionService(
                toolRegistry,
                argumentResolver,
                objectMapper,
                frontendSubmitCoordinator
        );
        VerifyService verifyService = new VerifyService(llmService);
        this.orchestrator = new AgentOrchestrator(llmService, toolExecutionService, verifyService, objectMapper);
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

        return Flux.defer(() -> {
                    List<Message> historyMessages = loadHistoryMessages(request.chatId());
                    TurnTrace trace = new TurnTrace();
                    return orchestrator.runStream(definition, request, historyMessages, enabledToolsByName)
                            .doOnNext(trace::capture)
                            .doOnComplete(() -> persistTurn(request, trace));
                })
                .subscribeOn(Schedulers.boundedElastic());
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

    private final class TurnTrace {
        private final StringBuilder pendingReasoning = new StringBuilder();
        private long pendingReasoningStartedAt;
        private final StringBuilder pendingAssistant = new StringBuilder();
        private long pendingAssistantStartedAt;
        private final List<ChatWindowMemoryStore.RunMessage> orderedMessages = new ArrayList<>();
        private final Map<String, ToolTrace> toolByCallId = new LinkedHashMap<>();

        private void capture(AgentDelta delta) {
            if (delta == null) {
                return;
            }
            long now = System.currentTimeMillis();

            if (StringUtils.hasText(delta.thinking())) {
                if (pendingReasoningStartedAt <= 0) {
                    pendingReasoningStartedAt = now;
                }
                pendingReasoning.append(delta.thinking());
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
