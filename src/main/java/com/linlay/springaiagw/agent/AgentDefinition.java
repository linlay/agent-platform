package com.linlay.springaiagw.agent;

import com.linlay.springaiagw.agent.mode.AgentMode;
import com.linlay.springaiagw.agent.mode.PlanExecuteMode;
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
        AgentMode agentMode,
        List<String> tools
) {
    public AgentDefinition {
        if (tools == null) {
            tools = List.of();
        } else {
            tools = List.copyOf(tools);
        }
    }

    public String systemPrompt() {
        return agentMode.primarySystemPrompt();
    }

    /**
     * Backward-compatible prompt access. Returns an AgentPromptSet-like view
     * from the AgentMode for code that still accesses promptSet().
     */
    public AgentPromptSet promptSet() {
        if (agentMode instanceof PlanExecuteMode pe) {
            return new AgentPromptSet(
                    pe.executeSystemPrompt(),
                    pe.planSystemPrompt(),
                    pe.executeSystemPrompt(),
                    pe.summarySystemPrompt()
            );
        }
        return new AgentPromptSet(agentMode.systemPrompt(), null, null, null);
    }
}
