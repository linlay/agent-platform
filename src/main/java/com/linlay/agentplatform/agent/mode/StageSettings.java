package com.linlay.agentplatform.agent.mode;

import com.linlay.agentplatform.agent.runtime.policy.ComputePolicy;
import com.linlay.agentplatform.model.ModelProtocol;

import java.util.List;

public record StageSettings(
        String systemPrompt,
        String modelKey,
        String providerKey,
        String model,
        ModelProtocol protocol,
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
        this(systemPrompt, null, providerKey, model, null, tools, reasoningEnabled, reasoningEffort, false);
    }

    public StageSettings(
            String systemPrompt,
            String providerKey,
            String model,
            List<String> tools,
            boolean reasoningEnabled,
            ComputePolicy reasoningEffort,
            boolean deepThinking
    ) {
        this(systemPrompt, null, providerKey, model, null, tools, reasoningEnabled, reasoningEffort, deepThinking);
    }

    public StageSettings(
            String systemPrompt,
            String modelKey,
            String providerKey,
            String model,
            ModelProtocol protocol,
            List<String> tools,
            boolean reasoningEnabled,
            ComputePolicy reasoningEffort
    ) {
        this(systemPrompt, modelKey, providerKey, model, protocol, tools, reasoningEnabled, reasoningEffort, false);
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
        if (protocol == null) {
            protocol = ModelProtocol.OPENAI;
        }
    }
}
