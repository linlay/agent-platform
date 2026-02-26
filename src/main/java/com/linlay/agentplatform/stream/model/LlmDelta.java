package com.linlay.agentplatform.stream.model;

import java.util.List;
import java.util.Map;

public record LlmDelta(
        String reasoning,
        String content,
        List<ToolCallDelta> toolCalls,
        String finishReason,
        Map<String, Object> usage
) {

    public LlmDelta(
            String content,
            List<ToolCallDelta> toolCalls,
            String finishReason
    ) {
        this(null, content, toolCalls, finishReason, null);
    }

    public LlmDelta(
            String reasoning,
            String content,
            List<ToolCallDelta> toolCalls,
            String finishReason
    ) {
        this(reasoning, content, toolCalls, finishReason, null);
    }
}
