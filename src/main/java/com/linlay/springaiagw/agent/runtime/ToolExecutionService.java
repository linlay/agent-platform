package com.linlay.springaiagw.agent.runtime;

import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.springaiagw.agent.PlannedToolCall;
import com.linlay.springaiagw.agent.ToolArgumentResolver;
import com.linlay.springaiagw.model.agw.AgentDelta;
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
