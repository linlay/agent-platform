package com.linlay.agentplatform.agent.runtime;

import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.agentplatform.agent.PlannedToolCall;
import com.linlay.agentplatform.agent.ToolAppend;
import com.linlay.agentplatform.agent.ToolArgumentResolver;
import com.linlay.agentplatform.agent.runtime.policy.Budget;
import com.linlay.agentplatform.model.AgentDelta;
import com.linlay.agentplatform.service.FrontendSubmitCoordinator;
import com.linlay.agentplatform.tool.BaseTool;
import com.linlay.agentplatform.tool.CapabilityKind;
import com.linlay.agentplatform.tool.ToolRegistry;
import org.springframework.util.StringUtils;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ExecutionException;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.Future;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.TimeoutException;
import java.util.function.Consumer;

public class ToolExecutionService {

    public static final String FRONTEND_SUBMIT_TIMEOUT_CODE = "frontend_submit_timeout";

    private static final ExecutorService BACKEND_TOOL_EXECUTOR = Executors.newCachedThreadPool(runnable -> {
        Thread thread = new Thread(runnable, "agent-platform-backend-tool");
        thread.setDaemon(true);
        return thread;
    });

    private final ToolRegistry toolRegistry;
    private final ToolArgumentResolver toolArgumentResolver;
    private final ObjectMapper objectMapper;
    private final FrontendSubmitCoordinator frontendSubmitCoordinator;

    public ToolExecutionService(
            ToolRegistry toolRegistry,
            ToolArgumentResolver toolArgumentResolver,
            ObjectMapper objectMapper,
            FrontendSubmitCoordinator frontendSubmitCoordinator
    ) {
        this.toolRegistry = toolRegistry;
        this.toolArgumentResolver = toolArgumentResolver;
        this.objectMapper = objectMapper;
        this.frontendSubmitCoordinator = frontendSubmitCoordinator;
    }

    public ToolExecutionBatch executeToolCalls(
            List<PlannedToolCall> calls,
            Map<String, BaseTool> enabledToolsByName,
            List<Map<String, Object>> records,
            String runId,
            ExecutionContext context,
            boolean emitToolCallDelta
    ) {
        return executeToolCalls(calls, enabledToolsByName, records, runId, context, emitToolCallDelta, null, null);
    }

    public ToolExecutionBatch executeToolCalls(
            List<PlannedToolCall> calls,
            Map<String, BaseTool> enabledToolsByName,
            List<Map<String, Object>> records,
            String runId,
            ExecutionContext context,
            boolean emitToolCallDelta,
            String taskId
    ) {
        return executeToolCalls(calls, enabledToolsByName, records, runId, context, emitToolCallDelta, taskId, null);
    }

    public ToolExecutionBatch executeToolCalls(
            List<PlannedToolCall> calls,
            Map<String, BaseTool> enabledToolsByName,
            List<Map<String, Object>> records,
            String runId,
            ExecutionContext context,
            boolean emitToolCallDelta,
            String taskId,
            Consumer<AgentDelta> preExecutionEmitter
    ) {
        if (calls == null || calls.isEmpty()) {
            return new ToolExecutionBatch(List.of(), List.of());
        }

        List<AgentDelta> deltas = new ArrayList<>();
        List<ToolExecutionEvent> events = new ArrayList<>();
        List<PreparedToolCall> preparedCalls = new ArrayList<>();

        int seq = 0;
        for (PlannedToolCall call : calls) {
            if (call == null) {
                continue;
            }
            seq++;
            String toolName = normalizeToolName(call.name());
            String callId = StringUtils.hasText(call.callId())
                    ? call.callId().trim()
                    : "call_" + toolName + "_" + seq + "_" + shortId();
            String toolType = toolRegistry.toolCallType(toolName);

            Map<String, Object> plannedArgs = call.arguments() == null ? Map.of() : call.arguments();
            Map<String, Object> resolvedArgs = toolArgumentResolver.resolveToolArguments(toolName, plannedArgs, records);
            String argsJson = toJson(resolvedArgs);
            preparedCalls.add(new PreparedToolCall(callId, toolName, toolType, resolvedArgs, argsJson));

            if (emitToolCallDelta) {
                deltas.add(AgentDelta.toolCalls(List.of(new ToolCallDelta(callId, toolType, toolName, argsJson)), taskId));
                if (!isActionType(toolType)) {
                    deltas.add(AgentDelta.toolEnd(callId));
                }
            }
        }

        if (!emitToolCallDelta) {
            List<String> toolEnds = preparedCalls.stream()
                    .filter(call -> !isActionType(call.toolType()))
                    .map(PreparedToolCall::callId)
                    .toList();
            if (!toolEnds.isEmpty()) {
                if (preExecutionEmitter != null) {
                    preExecutionEmitter.accept(AgentDelta.toolEnds(toolEnds));
                } else {
                    deltas.add(AgentDelta.toolEnds(toolEnds));
                }
            }
        }

        for (PreparedToolCall call : preparedCalls) {
            JsonNode resultNode = invokeByKind(
                    runId,
                    call.callId(),
                    call.toolName(),
                    call.toolType(),
                    call.resolvedArgs(),
                    enabledToolsByName,
                    context
            );
            String resultText = toResultText(resultNode);
            deltas.add(AgentDelta.toolResult(call.callId(), resultText));
            records.add(buildToolRecord(call.callId(), call.toolName(), call.toolType(), call.resolvedArgs(), resultNode));
            events.add(new ToolExecutionEvent(call.callId(), call.toolName(), call.toolType(), call.argsJson(), resultText));

            AgentDelta planDelta = planUpdateDelta(context, call.toolName(), call.resolvedArgs(), resultNode);
            if (planDelta != null) {
                deltas.add(planDelta);
            }
        }

        return new ToolExecutionBatch(List.copyOf(deltas), List.copyOf(events));
    }

    public List<com.linlay.agentplatform.service.LlmService.LlmFunctionTool> enabledFunctionTools(Map<String, BaseTool> enabledToolsByName) {
        if (enabledToolsByName == null || enabledToolsByName.isEmpty()) {
            return List.of();
        }
        return enabledToolsByName.values().stream()
                .sorted(java.util.Comparator.comparing(BaseTool::name))
                .map(tool -> new com.linlay.agentplatform.service.LlmService.LlmFunctionTool(
                        normalizeToolName(tool.name()),
                        normalize(tool.description()),
                        tool.parametersSchema(),
                        false
                ))
                .toList();
    }

    public PlanSnapshot planSnapshot(ExecutionContext context) {
        if (context == null) {
            return new PlanSnapshot("plan_default", null, List.of());
        }
        String planId = context.planId();
        String chatId = context.request() == null ? null : context.request().chatId();
        return new PlanSnapshot(planId, chatId, context.planTasks());
    }

    public String applyBackendPrompts(String systemPrompt, Map<String, BaseTool> stageTools) {
        return applyBackendPrompts(systemPrompt, stageTools, ToolAppend.DEFAULTS, true);
    }

    public String applyBackendPrompts(
            String systemPrompt,
            Map<String, BaseTool> stageTools,
            boolean includeAfterCallHints
    ) {
        return applyBackendPrompts(systemPrompt, stageTools, ToolAppend.DEFAULTS, includeAfterCallHints);
    }

    public String applyBackendPrompts(
            String systemPrompt,
            Map<String, BaseTool> stageTools,
            ToolAppend toolAppend,
            boolean includeAfterCallHints
    ) {
        ToolAppend effective = toolAppend == null ? ToolAppend.DEFAULTS : toolAppend;
        String base = normalize(systemPrompt);
        List<String> sections = new ArrayList<>();
        // Tool appendix is always merged into system prompt after stage/skill sections.
        sections.add(backendToolDescriptionSection(stageTools, effective.toolDescriptionTitle()));
        if (includeAfterCallHints) {
            sections.add(backendAfterCallHintSection(stageTools, effective.afterCallHintTitle()));
        }
        List<String> filteredSections = sections.stream()
                .filter(StringUtils::hasText)
                .toList();
        if (filteredSections.isEmpty()) {
            return base;
        }
        String appendix = String.join("\n\n", filteredSections);
        if (!StringUtils.hasText(base)) {
            return appendix;
        }
        return base + "\n\n" + appendix;
    }

    public String backendToolDescriptionSection(Map<String, BaseTool> stageTools, String sectionTitle) {
        return formatToolSection(sectionTitle, backendDescriptionLines(stageTools));
    }

    public String backendAfterCallHintSection(Map<String, BaseTool> stageTools, String sectionTitle) {
        return formatToolSection(sectionTitle, backendAfterCallHintLines(stageTools));
    }

    private List<String> backendDescriptionLines(Map<String, BaseTool> stageTools) {
        return backendTools(stageTools).stream()
                .map(tool -> {
                    String description = normalize(tool.description());
                    if (!StringUtils.hasText(description)) {
                        return null;
                    }
                    return "- " + normalizeToolName(tool.name()) + ": " + description;
                })
                .filter(StringUtils::hasText)
                .distinct()
                .toList();
    }

    private List<String> backendAfterCallHintLines(Map<String, BaseTool> stageTools) {
        return backendTools(stageTools).stream()
                .map(tool -> {
                    String afterCallHint = normalize(tool.afterCallHint());
                    if (!StringUtils.hasText(afterCallHint)) {
                        return null;
                    }
                    return "- " + normalizeToolName(tool.name()) + ": " + afterCallHint;
                })
                .filter(StringUtils::hasText)
                .distinct()
                .toList();
    }

    private List<BaseTool> backendTools(Map<String, BaseTool> stageTools) {
        if (stageTools == null || stageTools.isEmpty()) {
            return List.of();
        }
        return stageTools.values().stream()
                .filter(tool -> tool != null && StringUtils.hasText(tool.name()))
                .filter(tool -> isBackendTool(tool.name()))
                .sorted(Comparator.comparing(tool -> normalizeToolName(tool.name())))
                .toList();
    }

    private String formatToolSection(String sectionTitle, List<String> lines) {
        if (lines == null || lines.isEmpty()) {
            return "";
        }
        String title = StringUtils.hasText(sectionTitle)
                ? sectionTitle.trim()
                : ToolAppend.DEFAULTS.toolDescriptionTitle();
        return title + "\n" + String.join("\n", lines);
    }

    private JsonNode invokeByKind(
            String runId,
            String toolId,
            String toolName,
            String toolType,
            Map<String, Object> args,
            Map<String, BaseTool> enabledToolsByName,
            ExecutionContext context
    ) {
        if (!enabledToolsByName.containsKey(toolName)) {
            return errorResult(toolName, "Tool is not enabled for this agent: " + toolName);
        }

        if ("_plan_get_tasks_".equals(toolName)) {
            return planGetResult(planSnapshot(context));
        }

        if ("action".equalsIgnoreCase(toolType)) {
            return objectMapper.getNodeFactory().textNode("OK");
        }

        if (toolRegistry.isFrontend(toolName)) {
            if (frontendSubmitCoordinator == null) {
                return errorResult(toolName, "Frontend submit coordinator is not configured");
            }
            if (!StringUtils.hasText(runId)) {
                return errorResult(toolName, "Missing runId for frontend tool submission");
            }
            try {
                Object payload = frontendSubmitCoordinator.awaitSubmit(runId.trim(), toolId).block();
                Object normalized = payload == null ? Map.of() : payload;
                return objectMapper.valueToTree(normalized);
            } catch (Exception ex) {
                if (isFrontendSubmitTimeout(ex)) {
                    return errorResult(toolName, FRONTEND_SUBMIT_TIMEOUT_CODE, resolveErrorMessage(ex));
                }
                return errorResult(toolName, resolveErrorMessage(ex));
            }
        }

        return invokeBackendWithPolicy(toolName, args, context);
    }

    private JsonNode invokeBackendWithPolicy(
            String toolName,
            Map<String, Object> args,
            ExecutionContext context
    ) {
        Budget.Scope scope = context == null || context.budget() == null
                ? Budget.DEFAULT.tool()
                : context.budget().tool();
        int retries = Math.max(0, scope.retryCount());
        long timeoutMs = Math.max(1L, scope.timeoutMs());

        for (int attempt = 0; attempt <= retries; attempt++) {
            try {
                return invokeBackendOnce(toolName, args, timeoutMs);
            } catch (IllegalArgumentException ex) {
                return errorResult(toolName, ex.getMessage());
            } catch (TimeoutException ex) {
                if (attempt >= retries) {
                    return errorResult(toolName, ex.getMessage());
                }
            } catch (RuntimeException ex) {
                if (attempt >= retries) {
                    return errorResult(toolName, ex.getMessage());
                }
            }
        }

        return errorResult(toolName, "Tool invocation failed after retries");
    }

    private JsonNode invokeBackendOnce(
            String toolName,
            Map<String, Object> args,
            long timeoutMs
    ) throws TimeoutException {
        Future<JsonNode> future = BACKEND_TOOL_EXECUTOR.submit(() -> toolRegistry.invoke(toolName, args));
        try {
            return future.get(timeoutMs, TimeUnit.MILLISECONDS);
        } catch (TimeoutException ex) {
            future.cancel(true);
            throw new TimeoutException("Backend tool timeout: tool=" + toolName + ", timeoutMs=" + timeoutMs);
        } catch (InterruptedException ex) {
            Thread.currentThread().interrupt();
            throw new RuntimeException("Backend tool invocation interrupted", ex);
        } catch (ExecutionException ex) {
            Throwable cause = ex.getCause();
            if (cause instanceof IllegalArgumentException illegalArgumentException) {
                throw illegalArgumentException;
            }
            if (cause instanceof RuntimeException runtimeException) {
                throw runtimeException;
            }
            throw new RuntimeException(cause == null ? ex : cause);
        }
    }

    private Map<String, Object> buildToolRecord(
            String callId,
            String toolName,
            String toolType,
            Map<String, Object> args,
            JsonNode result
    ) {
        Map<String, Object> record = new LinkedHashMap<>();
        record.put("callId", callId);
        record.put("toolName", toolName);
        record.put("toolType", toolType);
        record.put("arguments", args == null ? Map.of() : args);
        record.put("result", result);
        return record;
    }

    private String toJson(Object value) {
        try {
            return objectMapper.writeValueAsString(value == null ? Map.of() : value);
        } catch (Exception ex) {
            return "{}";
        }
    }

    private String toResultText(JsonNode result) {
        if (result == null || result.isNull()) {
            return "null";
        }
        if (result.isTextual()) {
            return result.asText();
        }
        return result.toString();
    }

    private boolean isActionType(String toolType) {
        return "action".equalsIgnoreCase(toolType);
    }

    private JsonNode planGetResult(PlanSnapshot snapshot) {
        return objectMapper.getNodeFactory().textNode(planSnapshotText(snapshot));
    }

    private String planSnapshotText(PlanSnapshot snapshot) {
        StringBuilder text = new StringBuilder();
        text.append("计划ID: ").append(normalize(snapshot.planId())).append('\n');
        text.append("任务列表:");
        if (snapshot.tasks().isEmpty()) {
            text.append("\n- (空)");
        } else {
            for (AgentDelta.PlanTask task : snapshot.tasks()) {
                if (task == null) {
                    continue;
                }
                text.append("\n- ")
                        .append(normalize(task.taskId()))
                        .append(" | ")
                        .append(normalizeStatus(task.status()))
                        .append(" | ")
                        .append(normalize(task.description()));
            }
        }
        text.append('\n')
                .append("当前应执行 taskId: ")
                .append(firstUnfinishedTaskId(snapshot.tasks()));
        return text.toString();
    }

    private String firstUnfinishedTaskId(List<AgentDelta.PlanTask> tasks) {
        if (tasks == null || tasks.isEmpty()) {
            return "none";
        }
        for (AgentDelta.PlanTask task : tasks) {
            if (task == null || !StringUtils.hasText(task.taskId())) {
                continue;
            }
            String status = normalizeStatus(task.status());
            if (!"completed".equals(status) && !"canceled".equals(status) && !"failed".equals(status)) {
                return task.taskId().trim();
            }
        }
        return "none";
    }

    private String normalizeToolName(String raw) {
        return normalize(raw).toLowerCase(Locale.ROOT);
    }

    private String normalize(String value) {
        return value == null ? "" : value.trim();
    }

    private String shortId() {
        return UUID.randomUUID().toString().replace("-", "").substring(0, 8);
    }

    private AgentDelta planUpdateDelta(
            ExecutionContext context,
            String toolName,
            Map<String, Object> args,
            JsonNode resultNode
    ) {
        if (context == null || toolName == null || toolName.isBlank()) {
            return null;
        }
        String normalizedName = toolName.trim().toLowerCase(Locale.ROOT);
        if ("_plan_add_tasks_".equals(normalizedName)) {
            String resultText = toResultText(resultNode);
            if (isFailedPlanToolResult(resultText)) {
                return null;
            }
            List<AgentDelta.PlanTask> created = parsePlanTasksFromResult(resultText);
            if (created.isEmpty()) {
                return null;
            }
            context.appendPlanTasks(created);
            return AgentDelta.planUpdate(context.planId(), context.request().chatId(), context.planTasks());
        }
        if ("_plan_update_task_".equals(normalizedName)) {
            String resultText = toResultText(resultNode);
            if (!isSuccessfulPlanUpdateResult(resultText, resultNode)) {
                return null;
            }
            String taskId = asString(args, "taskId");
            String status = asString(args, "status");
            if (!StringUtils.hasText(taskId) || !StringUtils.hasText(status)) {
                return null;
            }
            String description = asString(args, "description");
            boolean updated = context.updatePlanTask(taskId, status, description);
            if (!updated) {
                return null;
            }
            return AgentDelta.planUpdate(context.planId(), context.request().chatId(), context.planTasks());
        }
        return null;
    }

    private List<AgentDelta.PlanTask> parsePlanTasksFromResult(String resultText) {
        if (!StringUtils.hasText(resultText)) {
            return List.of();
        }
        String normalized = resultText.replace("\r\n", "\n");
        List<AgentDelta.PlanTask> tasks = new ArrayList<>();
        for (String line : normalized.split("\n")) {
            String trimmed = line == null ? "" : line.trim();
            if (!StringUtils.hasText(trimmed)) {
                continue;
            }
            String[] parts = trimmed.split("\\|", 3);
            if (parts.length != 3) {
                continue;
            }
            String taskId = normalize(parts[0]);
            String status = parseLineStatus(parts[1]);
            String description = normalize(parts[2]);
            if (!StringUtils.hasText(taskId) || !StringUtils.hasText(description) || !StringUtils.hasText(status)) {
                continue;
            }
            tasks.add(new AgentDelta.PlanTask(taskId, description, status));
        }
        return tasks.isEmpty() ? List.of() : List.copyOf(tasks);
    }

    private String readString(Map<?, ?> map, String key) {
        if (map == null || key == null) {
            return null;
        }
        Object value = map.get(key);
        if (value == null) {
            return null;
        }
        String text = value.toString();
        return text == null || text.isBlank() ? null : text;
    }

    private String asString(Map<String, Object> args, String key) {
        if (args == null || key == null) {
            return null;
        }
        Object value = args.get(key);
        if (value == null) {
            return null;
        }
        String text = value.toString();
        return text == null || text.isBlank() ? null : text;
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

    private String parseLineStatus(String raw) {
        if (!StringUtils.hasText(raw)) {
            return "init";
        }
        String normalized = raw.trim().toLowerCase(Locale.ROOT);
        return switch (normalized) {
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> null;
        };
    }

    private boolean isFailedPlanToolResult(String resultText) {
        if (!StringUtils.hasText(resultText)) {
            return true;
        }
        return normalize(resultText).startsWith("失败:");
    }

    private boolean isSuccessfulPlanUpdateResult(String resultText, JsonNode resultNode) {
        if ("OK".equals(normalize(resultText))) {
            return true;
        }
        if (resultNode == null || resultNode.isNull()) {
            return false;
        }
        if (resultNode.isObject() && resultNode.has("ok")) {
            return resultNode.path("ok").asBoolean(false);
        }
        return false;
    }

    private boolean isBackendTool(String toolName) {
        return toolRegistry.capability(toolName)
                .map(descriptor -> descriptor.kind() == CapabilityKind.BACKEND)
                .orElse(true);
    }

    private ObjectNode errorResult(String toolName, String message) {
        return errorResult(toolName, null, message);
    }

    private ObjectNode errorResult(String toolName, String code, String message) {
        ObjectNode error = objectMapper.createObjectNode();
        error.put("tool", toolName);
        error.put("ok", false);
        if (StringUtils.hasText(code)) {
            error.put("code", code);
        }
        error.put("error", message == null ? "unknown error" : message);
        return error;
    }

    private boolean isFrontendSubmitTimeout(Throwable throwable) {
        Throwable cursor = throwable;
        while (cursor != null) {
            if (cursor instanceof TimeoutException) {
                return true;
            }
            String message = cursor.getMessage();
            if (StringUtils.hasText(message) && message.contains("Frontend tool submit timeout")) {
                return true;
            }
            cursor = cursor.getCause();
        }
        return false;
    }

    private String resolveErrorMessage(Throwable throwable) {
        Throwable cursor = throwable;
        while (cursor != null) {
            if (StringUtils.hasText(cursor.getMessage())) {
                return cursor.getMessage();
            }
            cursor = cursor.getCause();
        }
        return "unknown error";
    }

    public record ToolExecutionEvent(
            String callId,
            String toolName,
            String toolType,
            String argsJson,
            String resultText
    ) {
    }

    private record PreparedToolCall(
            String callId,
            String toolName,
            String toolType,
            Map<String, Object> resolvedArgs,
            String argsJson
    ) {
    }

    public record ToolExecutionBatch(
            List<AgentDelta> deltas,
            List<ToolExecutionEvent> events
    ) {
    }

    public record PlanSnapshot(
            String planId,
            String chatId,
            List<AgentDelta.PlanTask> tasks
    ) {
        public PlanSnapshot {
            if (tasks == null) {
                tasks = List.of();
            } else {
                tasks = List.copyOf(tasks);
            }
        }
    }
}
