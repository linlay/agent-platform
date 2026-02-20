package com.linlay.springaiagw.model;

import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;

import java.util.List;
import java.util.Map;
import java.util.Objects;

public record AgentDelta(
        String reasoning,
        String content,
        List<ToolCallDelta> toolCalls,
        List<ToolResult> toolResults,
        PlanUpdate planUpdate,
        TaskLifecycle taskLifecycle,
        String taskId,
        String finishReason,
        String stageMarker,
        Map<String, Object> usage
) {

    public AgentDelta {
        if (toolCalls == null) {
            toolCalls = List.of();
        } else {
            toolCalls = List.copyOf(toolCalls);
        }
        if (toolResults == null) {
            toolResults = List.of();
        } else {
            toolResults = List.copyOf(toolResults);
        }
    }

    public static AgentDelta reasoning(String delta) {
        return reasoning(delta, null);
    }

    public static AgentDelta reasoning(String delta, String taskId) {
        return new AgentDelta(delta, null, List.of(), List.of(), null, null, normalizeTaskId(taskId), null, null, null);
    }

    public static AgentDelta content(String delta) {
        return content(delta, null);
    }

    public static AgentDelta content(String delta, String taskId) {
        return new AgentDelta(null, delta, List.of(), List.of(), null, null, normalizeTaskId(taskId), null, null, null);
    }

    public static AgentDelta toolCalls(List<ToolCallDelta> toolCalls) {
        return toolCalls(toolCalls, null);
    }

    public static AgentDelta toolCalls(List<ToolCallDelta> toolCalls, String taskId) {
        return new AgentDelta(null, null, toolCalls, List.of(), null, null, normalizeTaskId(taskId), null, null, null);
    }

    public static AgentDelta toolResult(String toolId, JsonNode result) {
        String resultText;
        if (result == null || result.isNull()) {
            resultText = "null";
        } else if (result.isTextual()) {
            resultText = result.asText();
        } else {
            resultText = result.toString();
        }
        return toolResult(toolId, resultText);
    }

    public static AgentDelta toolResult(String toolId, String result) {
        return new AgentDelta(null, null, List.of(), List.of(new ToolResult(toolId, result)), null, null, null, null, null, null);
    }

    public static AgentDelta planUpdate(String planId, String chatId, List<PlanTask> plan) {
        return new AgentDelta(null, null, List.of(), List.of(), new PlanUpdate(planId, chatId, plan), null, null, null, null, null);
    }

    public static AgentDelta taskStart(String taskId, String runId, String taskName, String description) {
        String normalizedTaskId = requireTaskId(taskId, "task.start");
        String normalizedRunId = requireRunId(runId);
        TaskLifecycle lifecycle = new TaskLifecycle("start", normalizedTaskId, normalizedRunId, normalizeText(taskName), normalizeText(description), null);
        return new AgentDelta(null, null, List.of(), List.of(), null, lifecycle, normalizedTaskId, null, null, null);
    }

    public static AgentDelta taskComplete(String taskId) {
        String normalizedTaskId = requireTaskId(taskId, "task.complete");
        TaskLifecycle lifecycle = new TaskLifecycle("complete", normalizedTaskId, null, null, null, null);
        return new AgentDelta(null, null, List.of(), List.of(), null, lifecycle, normalizedTaskId, null, null, null);
    }

    public static AgentDelta taskCancel(String taskId) {
        String normalizedTaskId = requireTaskId(taskId, "task.cancel");
        TaskLifecycle lifecycle = new TaskLifecycle("cancel", normalizedTaskId, null, null, null, null);
        return new AgentDelta(null, null, List.of(), List.of(), null, lifecycle, normalizedTaskId, null, null, null);
    }

    public static AgentDelta taskFail(String taskId, Map<String, Object> error) {
        String normalizedTaskId = requireTaskId(taskId, "task.fail");
        Map<String, Object> normalizedError = error == null ? Map.of("message", "Task failed") : Map.copyOf(error);
        TaskLifecycle lifecycle = new TaskLifecycle("fail", normalizedTaskId, null, null, null, normalizedError);
        return new AgentDelta(null, null, List.of(), List.of(), null, lifecycle, normalizedTaskId, null, null, null);
    }

    public static AgentDelta finish(String finishReason) {
        return new AgentDelta(null, null, List.of(), List.of(), null, null, null, finishReason, null, null);
    }

    public static AgentDelta stageMarker(String marker) {
        return new AgentDelta(null, null, List.of(), List.of(), null, null, null, null, marker, null);
    }

    public static AgentDelta usage(Map<String, Object> usage) {
        return new AgentDelta(null, null, List.of(), List.of(), null, null, null, null, null, usage);
    }

    private static String normalizeTaskId(String taskId) {
        String normalized = normalizeText(taskId);
        return normalized == null || normalized.isBlank() ? null : normalized;
    }

    private static String normalizeText(String text) {
        if (text == null) {
            return null;
        }
        String normalized = text.trim();
        return normalized.isEmpty() ? null : normalized;
    }

    private static String requireTaskId(String taskId, String eventType) {
        String normalized = normalizeTaskId(taskId);
        if (normalized == null) {
            throw new IllegalArgumentException(eventType + " requires non-blank taskId");
        }
        return normalized;
    }

    private static String requireRunId(String runId) {
        String normalized = normalizeText(runId);
        if (normalized == null) {
            throw new IllegalArgumentException("task.start requires non-blank runId");
        }
        return normalized;
    }

    public record ToolResult(
            String toolId,
            String result
    ) {
    }

    public record PlanUpdate(
            String planId,
            String chatId,
            List<PlanTask> plan
    ) {
        public PlanUpdate {
            if (plan == null) {
                plan = List.of();
            } else {
                plan = List.copyOf(plan);
            }
        }
    }

    public record PlanTask(
            String taskId,
            String description,
            String status
    ) {
    }

    public record TaskLifecycle(
            String kind,
            String taskId,
            String runId,
            String taskName,
            String description,
            Map<String, Object> error
    ) {
        public TaskLifecycle {
            Objects.requireNonNull(kind, "kind");
            Objects.requireNonNull(taskId, "taskId");
            if (error == null) {
                error = Map.of();
            } else {
                error = Map.copyOf(error);
            }
        }
    }
}
