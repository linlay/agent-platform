package com.linlay.springaiagw.agent;

import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;

import java.util.List;

public record AgentDefinition(
        String id,
        String description,
        String providerKey,
        String model,
        AgentRuntimeMode mode,
        RunSpec runSpec,
        AgentPromptSet promptSet,
        List<String> tools
) {
    public AgentDefinition {
        if (tools == null) {
            tools = List.of();
        } else {
            tools = List.copyOf(tools);
        }
        if (promptSet == null) {
            promptSet = new AgentPromptSet("", null, null, null);
        }
    }

    public String systemPrompt() {
        return promptSet.primarySystemPrompt();
    }
}
