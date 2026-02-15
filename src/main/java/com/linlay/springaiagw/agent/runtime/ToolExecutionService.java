package com.linlay.springaiagw.agent.runtime;

import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.agent.PlannedToolCall;
import com.linlay.springaiagw.agent.ToolArgumentResolver;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.service.FrontendSubmitCoordinator;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.CapabilityKind;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.springframework.util.StringUtils;

import java.util.ArrayList;
import java.util.Comparator;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.UUID;

public class ToolExecutionService {

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
        if (calls == null || calls.isEmpty()) {
            return new ToolExecutionBatch(List.of(), List.of());
        }

        List<AgentDelta> deltas = new ArrayList<>();
        List<ToolExecutionEvent> events = new ArrayList<>();

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

            if (emitToolCallDelta) {
                deltas.add(AgentDelta.toolCalls(List.of(new ToolCallDelta(callId, toolType, toolName, argsJson))));
            }

            JsonNode resultNode = invokeByKind(runId, callId, toolName, toolType, resolvedArgs, enabledToolsByName, context);
            String resultText = toResultText(resultNode);
            deltas.add(AgentDelta.toolResult(callId, resultText));
            records.add(buildToolRecord(callId, toolName, toolType, resolvedArgs, resultNode));
            events.add(new ToolExecutionEvent(callId, toolName, toolType, argsJson, resultText));

            AgentDelta planDelta = planUpdateDelta(context, toolName, resolvedArgs, resultNode);
            if (planDelta != null) {
                deltas.add(planDelta);
            }
        }

        return new ToolExecutionBatch(List.copyOf(deltas), List.copyOf(events));
    }

    public List<com.linlay.springaiagw.service.LlmService.LlmFunctionTool> enabledFunctionTools(Map<String, BaseTool> enabledToolsByName) {
        if (enabledToolsByName == null || enabledToolsByName.isEmpty()) {
            return List.of();
        }
        return enabledToolsByName.values().stream()
                .sorted(java.util.Comparator.comparing(BaseTool::name))
                .map(tool -> new com.linlay.springaiagw.service.LlmService.LlmFunctionTool(
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
        String base = normalize(systemPrompt);
        if (stageTools == null || stageTools.isEmpty()) {
            return base;
        }
        List<BaseTool> backendTools = stageTools.values().stream()
                .filter(tool -> tool != null && StringUtils.hasText(tool.name()))
                .filter(tool -> isBackendTool(tool.name()))
                .sorted(Comparator.comparing(tool -> normalizeToolName(tool.name())))
                .toList();
        if (backendTools.isEmpty()) {
            return base;
        }
        List<String> descriptionLines = backendTools.stream()
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
        List<String> promptLines = backendTools.stream()
                .map(tool -> {
                    String prompt = normalize(tool.prompt());
                    if (!StringUtils.hasText(prompt)) {
                        return null;
                    }
                    return "- " + normalizeToolName(tool.name()) + ": " + prompt;
                })
                .filter(StringUtils::hasText)
                .distinct()
                .toList();
        if (descriptionLines.isEmpty() && promptLines.isEmpty()) {
            return base;
        }
        List<String> sections = new ArrayList<>();
        if (!descriptionLines.isEmpty()) {
            sections.add("工具说明:\n" + String.join("\n", descriptionLines));
        }
        if (!promptLines.isEmpty()) {
            sections.add("工具补充指令:\n" + String.join("\n", promptLines));
        }
        String appendix = String.join("\n\n", sections);
        if (!StringUtils.hasText(base)) {
            return appendix;
        }
        return base + "\n\n" + appendix;
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
                return errorResult(toolName, ex.getMessage());
            }
        }

        try {
            return toolRegistry.invoke(toolName, args);
        } catch (Exception ex) {
            return errorResult(toolName, ex.getMessage());
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
        ObjectNode error = objectMapper.createObjectNode();
        error.put("tool", toolName);
        error.put("ok", false);
        error.put("error", message == null ? "unknown error" : message);
        return error;
    }

    public record ToolExecutionEvent(
            String callId,
            String toolName,
            String toolType,
            String argsJson,
            String resultText
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
