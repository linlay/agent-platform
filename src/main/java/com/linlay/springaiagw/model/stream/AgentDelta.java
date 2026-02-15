package com.linlay.springaiagw.model.stream;

import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;

import java.util.List;

public record AgentDelta(
        String reasoning,
        String content,
        List<ToolCallDelta> toolCalls,
        List<ToolResult> toolResults,
        PlanUpdate planUpdate,
        String finishReason
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
        return new AgentDelta(delta, null, List.of(), List.of(), null, null);
    }

    public static AgentDelta content(String delta) {
        return new AgentDelta(null, delta, List.of(), List.of(), null, null);
    }

    public static AgentDelta toolCalls(List<ToolCallDelta> toolCalls) {
        return new AgentDelta(null, null, toolCalls, List.of(), null, null);
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
        return new AgentDelta(null, null, List.of(), List.of(new ToolResult(toolId, result)), null, null);
    }

    public static AgentDelta planUpdate(String planId, String chatId, List<PlanTask> plan) {
        return new AgentDelta(null, null, List.of(), List.of(), new PlanUpdate(planId, chatId, plan), null);
    }

    public static AgentDelta finish(String finishReason) {
        return new AgentDelta(null, null, List.of(), List.of(), null, finishReason);
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
}
