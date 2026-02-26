package com.linlay.agentplatform.agent.mode;

import com.linlay.agentplatform.stream.model.LlmDelta;
import com.linlay.agentplatform.stream.model.ToolCallDelta;
import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.agent.PlannedToolCall;
import com.linlay.agentplatform.agent.runtime.ExecutionContext;
import com.linlay.agentplatform.agent.runtime.FrontendSubmitTimeoutException;
import com.linlay.agentplatform.agent.runtime.ToolExecutionService;
import com.linlay.agentplatform.agent.runtime.policy.ComputePolicy;
import com.linlay.agentplatform.agent.runtime.policy.ToolChoice;
import com.linlay.agentplatform.model.AgentDelta;
import com.linlay.agentplatform.model.ModelProtocol;
import com.linlay.agentplatform.service.LlmCallSpec;
import com.linlay.agentplatform.service.LlmService;
import com.linlay.agentplatform.tool.BaseTool;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.messages.AssistantMessage;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.ToolResponseMessage;
import org.springframework.util.StringUtils;
import reactor.core.publisher.FluxSink;

import java.util.ArrayList;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Objects;
import java.util.regex.Pattern;

public class OrchestratorServices {

    private static final Logger log = LoggerFactory.getLogger(OrchestratorServices.class);
    private static final TypeReference<Map<String, Object>> MAP_TYPE = new TypeReference<>() {
    };
    private static final Pattern STEP_PREFIX = Pattern.compile(
            "^(?:[-*•]|\\d+[.)]|步骤\\s*\\d+[:：.)]?|[一二三四五六七八九十]+[、.)])\\s*(.+)$"
    );
    private static final String FRONTEND_TIMEOUT_MESSAGE = "前端工具等待用户提交超时，本次运行已结束。请重新发起或在超时前提交。";

    private final LlmService llmService;
    private final ToolExecutionService toolExecutionService;
    private final ObjectMapper objectMapper;

    public OrchestratorServices(
            LlmService llmService,
            ToolExecutionService toolExecutionService,
            ObjectMapper objectMapper
    ) {
        this.llmService = llmService;
        this.toolExecutionService = toolExecutionService;
        this.objectMapper = objectMapper;
    }

    public LlmService llmService() {
        return llmService;
    }

    public ToolExecutionService toolExecutionService() {
        return toolExecutionService;
    }

    public ObjectMapper objectMapper() {
        return objectMapper;
    }

    public record ModelTurn(
            String finalText,
            String reasoningText,
            List<PlannedToolCall> toolCalls,
            Map<String, Object> usage
    ) {
    }

    public ModelTurn callModelTurnStreaming(
            ExecutionContext context,
            StageSettings stageSettings,
            List<Message> messages,
            String userPrompt,
            Map<String, BaseTool> stageTools,
            List<LlmService.LlmFunctionTool> tools,
            ToolChoice toolChoice,
            String stage,
            boolean parallelToolCalls,
            boolean emitReasoning,
            boolean emitContent,
            boolean emitToolCalls,
            FluxSink<AgentDelta> sink
    ) {
        return callModelTurnStreaming(
                context,
                stageSettings,
                messages,
                userPrompt,
                stageTools,
                tools,
                toolChoice,
                stage,
                parallelToolCalls,
                emitReasoning,
                emitContent,
                emitToolCalls,
                true,
                sink
        );
    }

    public ModelTurn callModelTurnStreaming(
            ExecutionContext context,
            StageSettings stageSettings,
            List<Message> messages,
            String userPrompt,
            Map<String, BaseTool> stageTools,
            List<LlmService.LlmFunctionTool> tools,
            ToolChoice toolChoice,
            String stage,
            boolean parallelToolCalls,
            boolean emitReasoning,
            boolean emitContent,
            boolean emitToolCalls,
            boolean includeAfterCallHints,
            FluxSink<AgentDelta> sink
    ) {
        Objects.requireNonNull(stageSettings, "stageSettings must not be null");
        context.incrementModelCalls();
        String stageSystemPrompt = context.stageSystemPrompt(stageSettings.systemPrompt());
        String deferredSkillPrompt = context.consumeDeferredSkillSystemPrompt();
        String mergedSystemPrompt = mergeSystemPrompt(stageSystemPrompt, deferredSkillPrompt);
        // System prompt merge order is strict:
        // 1) stage system prompt (agent.json stage-level prompt),
        // 2) skill catalog/deferred disclosure blocks (system),
        // 3) backend tool appendix (system).
        String effectiveSystemPrompt = toolExecutionService.applyBackendPrompts(
                mergedSystemPrompt,
                stageTools,
                context.definition().agentMode().toolAppend(),
                includeAfterCallHints
        );

        StringBuilder reasoning = new StringBuilder();
        StringBuilder content = new StringBuilder();
        Map<String, ToolAccumulator> toolsById = new LinkedHashMap<>();
        ToolAccumulator latest = null;
        int seq = 0;
        int deltaSeq = 0;
        boolean toolCallObserved = false;
        Map<String, Object> capturedUsage = null;

        for (LlmDelta delta : llmService.streamDeltas(new LlmCallSpec(
                resolveProvider(stageSettings, context),
                resolveModel(stageSettings, context),
                resolveProtocol(stageSettings, context),
                effectiveSystemPrompt,
                messages,
                userPrompt,
                tools,
                toolChoice,
                null,
                resolveEffort(stageSettings),
                stageSettings.reasoningEnabled(),
                4096,
                context.budget().model().timeoutMs(),
                stage,
                parallelToolCalls
        )).toIterable()) {
            if (delta == null) {
                continue;
            }
            deltaSeq++;

            boolean hasToolCalls = delta.toolCalls() != null && !delta.toolCalls().isEmpty();
            if (hasToolCalls) {
                toolCallObserved = true;
            }

            if (StringUtils.hasText(delta.reasoning())) {
                reasoning.append(delta.reasoning());
                if (emitReasoning && !toolCallObserved) {
                    emit(sink, AgentDelta.reasoning(delta.reasoning(), context.activeTaskId()));
                }
            }

            if (StringUtils.hasText(delta.content())) {
                content.append(delta.content());
                if (emitContent && !toolCallObserved) {
                    emit(sink, AgentDelta.content(delta.content(), context.activeTaskId()));
                }
            }

            List<ToolCallDelta> streamedCalls = new ArrayList<>();
            if (hasToolCalls) {
                for (ToolCallDelta call : delta.toolCalls()) {
                    if (call == null) {
                        continue;
                    }
                    String callId = normalize(call.id());
                    if (!StringUtils.hasText(callId)) {
                        callId = latest == null ? "call_native_" + (++seq) : latest.callId;
                    }

                    ToolAccumulator acc = toolsById.computeIfAbsent(callId, ToolAccumulator::new);
                    if (StringUtils.hasText(call.name())) {
                        acc.toolName = call.name();
                    }
                    if (StringUtils.hasText(call.type())) {
                        acc.toolType = call.type();
                    }
                    if (StringUtils.hasText(call.arguments())) {
                        acc.arguments.append(call.arguments());
                    }
                    latest = acc;
                    String emittedName = StringUtils.hasText(call.name()) ? call.name() : acc.toolName;
                    String argumentsDelta = call.arguments();
                    if (isPlanGenerateStage(stage) && StringUtils.hasText(argumentsDelta)) {
                        log.info(
                                "[plan-delta] runId={}, stage={}, deltaSeq={}, toolCallId={}, toolName={}, argumentsDelta={}",
                                context.request().runId(),
                                stage,
                                deltaSeq,
                                callId,
                                emittedName,
                                argumentsDelta
                        );
                    }

                    if (!emitToolCalls || !StringUtils.hasText(call.arguments())) {
                        continue;
                    }
                    String emittedType = StringUtils.hasText(call.type())
                            ? call.type()
                            : (StringUtils.hasText(acc.toolType) ? acc.toolType : "function");
                    streamedCalls.add(new ToolCallDelta(callId, emittedType, emittedName, call.arguments()));
                }
            }
            if (!streamedCalls.isEmpty()) {
                emit(sink, AgentDelta.toolCalls(streamedCalls, context.activeTaskId()));
            }

            if (delta.usage() != null && !delta.usage().isEmpty()) {
                capturedUsage = delta.usage();
                emit(sink, AgentDelta.usage(capturedUsage));
            }
        }

        List<PlannedToolCall> plannedToolCalls = new ArrayList<>();
        for (ToolAccumulator acc : toolsById.values()) {
            String toolName = normalize(acc.toolName).toLowerCase(Locale.ROOT);
            if (!StringUtils.hasText(toolName)) {
                continue;
            }
            Map<String, Object> args = parseMap(acc.arguments.toString());
            plannedToolCalls.add(new PlannedToolCall(toolName, args, acc.callId));
        }

        return new ModelTurn(content.toString(), reasoning.toString(), plannedToolCalls, capturedUsage);
    }

    public void executeToolsAndEmit(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            List<PlannedToolCall> plannedToolCalls,
            FluxSink<AgentDelta> sink
    ) {
        context.registerSkillUsageFromToolCalls(plannedToolCalls);
        ToolExecutionService.ToolExecutionBatch batch = toolExecutionService.executeToolCalls(
                plannedToolCalls,
                enabledToolsByName,
                context.toolRecords(),
                context.request().runId(),
                context,
                false,
                context.activeTaskId(),
                delta -> emit(sink, delta)
        );
        FrontendSubmitTimeoutException frontendTimeout = detectFrontendSubmitTimeout(batch.events());
        for (AgentDelta delta : batch.deltas()) {
            emit(sink, delta);
        }
        appendToolEvents(context.conversationMessages(), batch.events());
        appendToolEvents(context.executeMessages(), batch.events());
        if (frontendTimeout != null) {
            throw frontendTimeout;
        }
        context.incrementToolCalls(batch.events().size());
    }

    public void emitFinalAnswer(
            String candidateFinalText,
            boolean contentAlreadyEmitted,
            FluxSink<AgentDelta> sink
    ) {
        if (!contentAlreadyEmitted && StringUtils.hasText(candidateFinalText)) {
            emit(sink, AgentDelta.content(candidateFinalText));
        }
    }

    public Map<String, BaseTool> selectTools(Map<String, BaseTool> enabledToolsByName, List<String> configuredTools) {
        if (enabledToolsByName == null || enabledToolsByName.isEmpty()) {
            return Map.of();
        }
        if (configuredTools == null || configuredTools.isEmpty()) {
            return enabledToolsByName;
        }
        Map<String, BaseTool> selected = new LinkedHashMap<>();
        for (String raw : configuredTools) {
            String name = normalize(raw).toLowerCase(Locale.ROOT);
            if (!StringUtils.hasText(name)) {
                continue;
            }
            BaseTool tool = enabledToolsByName.get(name);
            if (tool != null) {
                selected.put(name, tool);
            }
        }
        return Map.copyOf(selected);
    }

    public void emit(FluxSink<AgentDelta> sink, AgentDelta delta) {
        if (delta == null || sink.isCancelled()) {
            return;
        }
        sink.next(delta);
    }

    public boolean requiresTool(ExecutionContext context) {
        return context.definition().runSpec().toolChoice() == ToolChoice.REQUIRED;
    }

    public boolean allowsTool(ExecutionContext context) {
        return context.definition().runSpec().toolChoice() != ToolChoice.NONE;
    }

    public int modelRetryCount(ExecutionContext context, int fallback) {
        if (context == null || context.budget() == null) {
            return fallback;
        }
        int configured = context.budget().model().retryCount();
        return configured > 0 ? configured : fallback;
    }

    public int toolRetryCount(ExecutionContext context, int fallback) {
        if (context == null || context.budget() == null) {
            return fallback;
        }
        int configured = context.budget().tool().retryCount();
        return configured > 0 ? configured : fallback;
    }

    public void appendAssistantMessage(List<Message> messages, String text) {
        String normalized = normalize(text);
        if (!normalized.isBlank()) {
            messages.add(new AssistantMessage(normalized));
        }
    }

    public String normalize(String value) {
        return value == null ? "" : value.trim();
    }

    public List<PlanStep> parsePlanSteps(String raw) {
        JsonNode root = readJson(raw);
        if (root != null && root.isObject() && root.path("steps").isArray()) {
            List<PlanStep> steps = new ArrayList<>();
            int index = 0;
            for (JsonNode node : root.path("steps")) {
                index++;
                String id = normalize(node.path("id").asText("step-" + index));
                String title = normalize(node.path("title").asText("Step " + index));
                String goal = normalize(node.path("goal").asText(title));
                String success = normalize(node.path("successCriteria").asText("完成步骤"));
                steps.add(new PlanStep(id, title, goal, success));
            }
            if (!steps.isEmpty()) {
                return steps;
            }
        }

        if (!StringUtils.hasText(raw)) {
            return List.of();
        }
        List<PlanStep> steps = new ArrayList<>();
        String normalized = raw.replace("\r\n", "\n");
        int index = 0;
        for (String line : normalized.split("\n")) {
            if (!StringUtils.hasText(line)) {
                continue;
            }
            String trimmed = line.trim();
            java.util.regex.Matcher matcher = STEP_PREFIX.matcher(trimmed);
            if (!matcher.matches()) {
                continue;
            }
            String content = normalize(matcher.group(1));
            if (content.isBlank()) {
                continue;
            }
            index++;
            String id = "step-" + index;
            String title = content;
            String goal = content;
            String success = "完成任务: " + content;
            steps.add(new PlanStep(id, title, goal, success));
        }
        return List.copyOf(steps);
    }

    public record PlanStep(
            String id,
            String title,
            String goal,
            String successCriteria
    ) {
    }

    private void appendToolEvents(List<Message> messages, List<ToolExecutionService.ToolExecutionEvent> events) {
        for (ToolExecutionService.ToolExecutionEvent event : events) {
            AssistantMessage.ToolCall toolCall = new AssistantMessage.ToolCall(
                    event.callId(),
                    event.toolType(),
                    event.toolName(),
                    event.argsJson()
            );
            messages.add(new AssistantMessage("", Map.of(), List.of(toolCall)));

            ToolResponseMessage.ToolResponse toolResponse = new ToolResponseMessage.ToolResponse(
                    event.callId(),
                    event.toolName(),
                    event.resultText()
            );
            messages.add(new ToolResponseMessage(List.of(toolResponse)));
        }
    }

    private FrontendSubmitTimeoutException detectFrontendSubmitTimeout(List<ToolExecutionService.ToolExecutionEvent> events) {
        if (events == null || events.isEmpty()) {
            return null;
        }
        for (ToolExecutionService.ToolExecutionEvent event : events) {
            if (event == null || !StringUtils.hasText(event.resultText())) {
                continue;
            }
            try {
                JsonNode result = objectMapper.readTree(event.resultText());
                if (!result.isObject()) {
                    continue;
                }
                String code = result.path("code").asText(null);
                if (!ToolExecutionService.FRONTEND_SUBMIT_TIMEOUT_CODE.equals(code)) {
                    continue;
                }
                String rawMessage = result.path("error").asText(null);
                String message = StringUtils.hasText(rawMessage) ? rawMessage.trim() : FRONTEND_TIMEOUT_MESSAGE;
                return new FrontendSubmitTimeoutException(message);
            } catch (Exception ignored) {
                // ignore malformed tool results and continue scanning
            }
        }
        return null;
    }

    private Map<String, Object> parseMap(String raw) {
        if (!StringUtils.hasText(raw)) {
            return new LinkedHashMap<>();
        }
        try {
            JsonNode node = objectMapper.readTree(raw);
            if (!node.isObject()) {
                return new LinkedHashMap<>();
            }
            Map<String, Object> mapped = objectMapper.convertValue(node, MAP_TYPE);
            return mapped == null ? new LinkedHashMap<>() : new LinkedHashMap<>(mapped);
        } catch (Exception ex) {
            return new LinkedHashMap<>();
        }
    }


    private JsonNode readJson(String raw) {
        if (!StringUtils.hasText(raw)) {
            return null;
        }
        String normalized = raw.trim();
        if (normalized.startsWith("```") && normalized.endsWith("```")) {
            normalized = normalized.substring(3, normalized.length() - 3).trim();
            if (normalized.startsWith("json")) {
                normalized = normalized.substring(4).trim();
            }
        }
        try {
            return objectMapper.readTree(normalized);
        } catch (Exception ex) {
            int start = normalized.indexOf('{');
            int end = normalized.lastIndexOf('}');
            if (start >= 0 && end > start) {
                try {
                    return objectMapper.readTree(normalized.substring(start, end + 1));
                } catch (Exception ignored) {
                    return null;
                }
            }
            return null;
        }
    }

    private static final class ToolAccumulator {
        private final String callId;
        private String toolName;
        private String toolType;
        private final StringBuilder arguments = new StringBuilder();

        private ToolAccumulator(String callId) {
            this.callId = callId;
        }
    }

    private String resolveProvider(StageSettings stageSettings, ExecutionContext context) {
        String provider = normalize(stageSettings.providerKey());
        if (StringUtils.hasText(provider)) {
            return provider;
        }
        return context.definition().providerKey();
    }

    private String resolveModel(StageSettings stageSettings, ExecutionContext context) {
        String model = normalize(stageSettings.model());
        if (StringUtils.hasText(model)) {
            return model;
        }
        return context.definition().model();
    }

    private ModelProtocol resolveProtocol(StageSettings stageSettings, ExecutionContext context) {
        if (stageSettings.protocol() != null) {
            return stageSettings.protocol();
        }
        return context.definition().protocol();
    }

    private String mergeSystemPrompt(String base, String appendix) {
        boolean hasBase = StringUtils.hasText(base);
        boolean hasAppendix = StringUtils.hasText(appendix);
        if (!hasBase) {
            return hasAppendix ? appendix : null;
        }
        if (!hasAppendix) {
            return base;
        }
        return base + "\n\n" + appendix;
    }

    private ComputePolicy resolveEffort(StageSettings stageSettings) {
        return stageSettings.reasoningEffort() == null ? ComputePolicy.MEDIUM : stageSettings.reasoningEffort();
    }

    private boolean isPlanGenerateStage(String stage) {
        return "agent-plan-generate".equals(stage);
    }
}
