package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;

import java.util.List;

public record StageSettings(
        String systemPrompt,
        String providerKey,
        String model,
        List<String> tools,
        boolean reasoningEnabled,
        ComputePolicy reasoningEffort,
        boolean deepThinking
) {

    public StageSettings(
            String systemPrompt,
            String providerKey,
            String model,
            List<String> tools,
            boolean reasoningEnabled,
            ComputePolicy reasoningEffort
    ) {
        this(systemPrompt, providerKey, model, tools, reasoningEnabled, reasoningEffort, false);
    }

    public StageSettings {
        if (tools == null) {
            tools = List.of();
        } else {
            tools = List.copyOf(tools);
        }
        if (reasoningEffort == null) {
            reasoningEffort = ComputePolicy.MEDIUM;
        }
    }
}
