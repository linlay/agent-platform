package com.linlay.springaiagw.agent.mode;

import com.aiagent.agw.sdk.model.LlmDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.PlannedToolCall;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.ToolExecutionService;
import com.linlay.springaiagw.agent.runtime.VerifyService;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.service.LlmCallSpec;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
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
    private static final Pattern TOOL_CALL_SNIPPET = Pattern.compile("_[a-z0-9_]+_\\s*\\(");

    private final LlmService llmService;
    private final ToolExecutionService toolExecutionService;
    private final VerifyService verifyService;
    private final ObjectMapper objectMapper;

    public OrchestratorServices(
            LlmService llmService,
            ToolExecutionService toolExecutionService,
            VerifyService verifyService,
            ObjectMapper objectMapper
    ) {
        this.llmService = llmService;
        this.toolExecutionService = toolExecutionService;
        this.verifyService = verifyService;
        this.objectMapper = objectMapper;
    }

    public LlmService llmService() {
        return llmService;
    }

    public ToolExecutionService toolExecutionService() {
        return toolExecutionService;
    }

    public VerifyService verifyService() {
        return verifyService;
    }

    public ObjectMapper objectMapper() {
        return objectMapper;
    }

    public record ModelTurn(
            String finalText,
            String reasoningText,
            List<PlannedToolCall> toolCalls
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
        String deferredSkillPrompt = context.consumeDeferredSkillUserPrompt();
        String effectiveUserPrompt = mergeUserPrompt(userPrompt, deferredSkillPrompt);
        String effectiveSystemPrompt = toolExecutionService.applyBackendPrompts(
                stageSystemPrompt,
                stageTools,
                includeAfterCallHints
        );

        StringBuilder reasoning = new StringBuilder();
        StringBuilder content = new StringBuilder();
        Map<String, ToolAccumulator> toolsById = new LinkedHashMap<>();
        ToolAccumulator latest = null;
        int seq = 0;
        int deltaSeq = 0;
        boolean toolCallObserved = false;

        for (LlmDelta delta : llmService.streamDeltas(new LlmCallSpec(
                resolveProvider(stageSettings, context),
                resolveModel(stageSettings, context),
                effectiveSystemPrompt,
                messages,
                effectiveUserPrompt,
                tools,
                toolChoice,
                null,
                null,
                resolveEffort(stageSettings),
                stageSettings.reasoningEnabled(),
                4096,
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
                    emit(sink, AgentDelta.reasoning(delta.reasoning()));
                }
            }

            if (StringUtils.hasText(delta.content())) {
                content.append(delta.content());
                if (emitContent && !toolCallObserved) {
                    emit(sink, AgentDelta.content(delta.content()));
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
                emit(sink, AgentDelta.toolCalls(streamedCalls));
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

        return new ModelTurn(content.toString(), reasoning.toString(), plannedToolCalls);
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
                false
        );
        context.incrementToolCalls(batch.events().size());
        for (AgentDelta delta : batch.deltas()) {
            emit(sink, delta);
        }
        appendToolEvents(context.conversationMessages(), batch.events());
        appendToolEvents(context.executeMessages(), batch.events());
    }

    public void emitFinalAnswer(
            ExecutionContext context,
            List<Message> messages,
            String candidateFinalText,
            boolean contentAlreadyEmitted,
            FluxSink<AgentDelta> sink
    ) {
        VerifyPolicy verifyPolicy = context.definition().runSpec().verify();
        boolean secondPass = verifyService.requiresSecondPass(verifyPolicy);

        if (!secondPass) {
            if (!contentAlreadyEmitted && StringUtils.hasText(candidateFinalText)) {
                emit(sink, AgentDelta.content(candidateFinalText));
            }
            return;
        }

        if (!StringUtils.hasText(candidateFinalText)) {
            return;
        }
        StringBuilder verifyOutput = new StringBuilder();
        for (String chunk : verifyService.streamSecondPass(
                verifyPolicy,
                context.definition().providerKey(),
                context.definition().model(),
                context.stageSystemPrompt(context.definition().agentMode().primarySystemPrompt()),
                messages,
                candidateFinalText,
                "agent-verify"
        ).toIterable()) {
            if (!StringUtils.hasText(chunk)) {
                continue;
            }
            verifyOutput.append(chunk);
            emit(sink, AgentDelta.content(chunk));
        }

        if (verifyOutput.isEmpty() && !contentAlreadyEmitted) {
            emit(sink, AgentDelta.content(candidateFinalText));
        }
    }

    public String forceFinalAnswer(
            ExecutionContext context,
            StageSettings stageSettings,
            Map<String, BaseTool> stageTools,
            List<Message> messages,
            String stage,
            boolean emitContent,
            FluxSink<AgentDelta> sink
    ) {
        String forcedPrompt = """
                请基于当前信息直接输出最终答案，禁止再次调用工具。
                禁止输出任何继续动作（例如“先检查/先查看资源/调用工具”）。
                若信息不足，请按以下结构回答：
                1) 已确认信息
                2) 阻塞点
                3) 最小下一步
                """;
        ModelTurn turn = callModelTurnStreaming(
                context,
                stageSettings,
                messages,
                forcedPrompt,
                stageTools,
                List.of(),
                ToolChoice.NONE,
                stage,
                false,
                false,
                false,
                true,
                sink
        );

        String finalText = normalize(turn.finalText());
        boolean isReactForceFinal = isReactForceFinalStage(stage);
        String resolved = finalText;
        if (isReactForceFinal && shouldFallbackToBlockedFinal(finalText)) {
            resolved = buildBlockedFinalAnswer(context);
        }
        if (emitContent && StringUtils.hasText(resolved)) {
            emit(sink, AgentDelta.content(resolved));
        }
        return resolved;
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
        return context.definition().runSpec().toolPolicy() == ToolPolicy.REQUIRE;
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

    private boolean isReactForceFinalStage(String stage) {
        String normalized = normalize(stage).toLowerCase(Locale.ROOT);
        return normalized.contains("react-force-final");
    }

    private boolean shouldFallbackToBlockedFinal(String text) {
        if (!StringUtils.hasText(text)) {
            return true;
        }
        String normalized = normalize(text);
        if (TOOL_CALL_SNIPPET.matcher(normalized).find()) {
            return true;
        }
        String compact = normalized.replaceAll("\\s+", "");
        return compact.startsWith("我需要先检查")
                || compact.startsWith("让我先检查")
                || compact.startsWith("先检查")
                || compact.contains("先查看可用资源")
                || compact.contains("我将先检查")
                || compact.contains("先使用_bash_")
                || compact.contains("先调用工具")
                || compact.contains("继续调用工具")
                || compact.contains("调用工具获取");
    }

    private String buildBlockedFinalAnswer(ExecutionContext context) {
        return "已确认信息:\n"
                + summarizeLatestToolRecord(context)
                + "\n\n阻塞点:\n"
                + "当前回合已禁止继续调用工具，现有信息不足以完成目标。"
                + "\n\n最小下一步:\n"
                + "请在下一轮允许工具调用（如 _bash_、_skill_run_script_）后重试，我将继续执行并给出最终结果。";
    }

    private String summarizeLatestToolRecord(ExecutionContext context) {
        if (context == null || context.toolRecords().isEmpty()) {
            return "- 暂无可用工具结果。";
        }
        Map<String, Object> latest = context.toolRecords().get(context.toolRecords().size() - 1);
        String toolName = safeRecordText(latest, "toolName");
        Object rawResult = latest == null ? null : latest.get("result");
        String resultSummary = summarizeResult(rawResult);
        String effectiveToolName = StringUtils.hasText(toolName) ? toolName : "unknown";
        return "- 最近工具: " + effectiveToolName
                + "\n- 结果摘要: " + resultSummary;
    }

    private String safeRecordText(Map<String, Object> record, String key) {
        if (record == null || !StringUtils.hasText(key)) {
            return "";
        }
        Object raw = record.get(key);
        if (raw == null) {
            return "";
        }
        return normalize(String.valueOf(raw));
    }

    private String summarizeResult(Object rawResult) {
        if (rawResult == null) {
            return "无";
        }
        String rawText;
        if (rawResult instanceof JsonNode node) {
            rawText = node.isTextual() ? node.asText() : node.toString();
        } else {
            rawText = String.valueOf(rawResult);
        }
        String oneLine = normalize(rawText).replaceAll("\\s+", " ");
        if (oneLine.length() <= 240) {
            return oneLine;
        }
        return oneLine.substring(0, 240) + "...";
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

    private String mergeUserPrompt(String userPrompt, String deferredSkillPrompt) {
        boolean hasUserPrompt = StringUtils.hasText(userPrompt);
        boolean hasDeferred = StringUtils.hasText(deferredSkillPrompt);
        if (!hasUserPrompt) {
            return hasDeferred ? deferredSkillPrompt : null;
        }
        if (!hasDeferred) {
            return userPrompt;
        }
        return userPrompt + "\n\n" + deferredSkillPrompt;
    }

    private ComputePolicy resolveEffort(StageSettings stageSettings) {
        return stageSettings.reasoningEffort() == null ? ComputePolicy.MEDIUM : stageSettings.reasoningEffort();
    }

    private boolean isPlanGenerateStage(String stage) {
        return "agent-plan-generate".equals(stage);
    }
}
