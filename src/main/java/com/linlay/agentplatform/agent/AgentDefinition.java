package com.linlay.agentplatform.agent;

import com.linlay.agentplatform.agent.mode.AgentMode;
import com.linlay.agentplatform.agent.runtime.AgentRuntimeMode;
import com.linlay.agentplatform.agent.runtime.policy.RunSpec;
import com.linlay.agentplatform.model.ModelProtocol;

import java.util.List;

public record AgentDefinition(
        String id,
        String name,
        String icon,
        String description,
        String modelKey,
        String providerKey,
        String model,
        ModelProtocol protocol,
        AgentRuntimeMode mode,
        RunSpec runSpec,
        AgentMode agentMode,
        List<String> tools,
        List<String> skills
) {
    public AgentDefinition(
            String id,
            String name,
            String icon,
            String description,
            String providerKey,
            String model,
            AgentRuntimeMode mode,
            RunSpec runSpec,
            AgentMode agentMode,
            List<String> tools,
            List<String> skills
    ) {
        this(id, name, icon, description, null, providerKey, model, null, mode, runSpec, agentMode, tools, skills);
    }

    public AgentDefinition {
        if (tools == null) {
            tools = List.of();
        } else {
            tools = List.copyOf(tools);
        }
        if (skills == null) {
            skills = List.of();
        } else {
            skills = List.copyOf(skills);
        }
        if (protocol == null) {
            protocol = ModelProtocol.OPENAI;
        }
    }

    public String systemPrompt() {
        return agentMode.primarySystemPrompt();
    }
}
