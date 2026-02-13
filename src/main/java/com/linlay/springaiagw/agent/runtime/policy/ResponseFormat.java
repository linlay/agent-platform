package com.linlay.springaiagw.agent.runtime.policy;

public record ResponseFormat(
        OutputShape shape,
        ToolChoice toolChoice,
        String jsonSchema,
        ComputePolicy compute,
        int maxTokens
) {
}
