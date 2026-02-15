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
import com.linlay.springaiagw.tool.ToolRegistry;
import org.springframework.util.StringUtils;

import java.util.ArrayList;
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

            JsonNode resultNode = invokeByKind(runId, callId, toolName, toolType, resolvedArgs, enabledToolsByName);
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

    private JsonNode invokeByKind(
            String runId,
            String toolId,
            String toolName,
            String toolType,
            Map<String, Object> args,
            Map<String, BaseTool> enabledToolsByName
    ) {
        if (!enabledToolsByName.containsKey(toolName)) {
            return errorResult(toolName, "Tool is not enabled for this agent: " + toolName);
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
        if ("_plan_create_".equals(normalizedName)) {
            if (!isSuccessfulPlanToolResult(resultNode)) {
                return null;
            }
            List<AgentDelta.PlanTask> created = parsePlanTasks(args);
            if (created.isEmpty()) {
                return null;
            }
            context.appendPlanTasks(created);
            return AgentDelta.planUpdate(context.planId(), context.request().chatId(), context.planTasks());
        }
        if ("_plan_task_update_".equals(normalizedName)) {
            if (!isSuccessfulPlanToolResult(resultNode)) {
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

    private List<AgentDelta.PlanTask> parsePlanTasks(Map<String, Object> args) {
        if (args == null || args.isEmpty()) {
            return List.of();
        }
        List<AgentDelta.PlanTask> tasks = new ArrayList<>();
        Object rawTasks = args.get("tasks");
        if (rawTasks instanceof List<?> list) {
            for (Object item : list) {
                if (!(item instanceof Map<?, ?> map)) {
                    continue;
                }
                String taskId = readString(map, "taskId");
                String description = readString(map, "description");
                if (!StringUtils.hasText(description)) {
                    continue;
                }
                tasks.add(new AgentDelta.PlanTask(
                        taskId,
                        description.trim(),
                        normalizeStatus(readString(map, "status"))
                ));
            }
        }
        if (!tasks.isEmpty()) {
            return List.copyOf(tasks);
        }
        String description = asString(args, "description");
        if (!StringUtils.hasText(description)) {
            return List.of();
        }
        return List.of(new AgentDelta.PlanTask(
                asString(args, "taskId"),
                description.trim(),
                normalizeStatus(asString(args, "status"))
        ));
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
            case "init", "in_progress", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
    }

    private boolean isSuccessfulPlanToolResult(JsonNode resultNode) {
        if (resultNode == null || resultNode.isNull()) {
            return false;
        }
        if (!resultNode.isObject() || !resultNode.has("ok")) {
            return true;
        }
        return resultNode.path("ok").asBoolean(false);
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
}
