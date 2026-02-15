package com.linlay.springaiagw.agent;

import com.linlay.springaiagw.agent.mode.AgentMode;
import com.linlay.springaiagw.agent.mode.PlanExecuteMode;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;

import java.util.List;

public record AgentDefinition(
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
                    pe.executeStage() == null ? null : pe.executeStage().systemPrompt(),
                    pe.planStage() == null ? null : pe.planStage().systemPrompt(),
                    pe.executeStage() == null ? null : pe.executeStage().systemPrompt(),
                    pe.summaryStage() == null ? null : pe.summaryStage().systemPrompt()
            );
        }
        return new AgentPromptSet(agentMode.systemPrompt(), null, null, null);
    }
}
