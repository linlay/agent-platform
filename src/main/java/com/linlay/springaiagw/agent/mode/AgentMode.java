package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.RuntimePromptTemplates;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import reactor.core.publisher.FluxSink;

import java.util.Map;

public sealed abstract class AgentMode
        permits OneshotMode, ReactMode, PlanExecuteMode {

    protected final String systemPrompt;
    protected final RuntimePromptTemplates runtimePrompts;

    protected AgentMode(String systemPrompt) {
        this(systemPrompt, RuntimePromptTemplates.defaults());
    }

    protected AgentMode(String systemPrompt, RuntimePromptTemplates runtimePrompts) {
        this.systemPrompt = systemPrompt;
        this.runtimePrompts = runtimePrompts == null ? RuntimePromptTemplates.defaults() : runtimePrompts;
    }

    public abstract AgentRuntimeMode runtimeMode();

    public String systemPrompt() {
        return systemPrompt;
    }

    public String primarySystemPrompt() {
        return systemPrompt;
    }

    public RuntimePromptTemplates runtimePrompts() {
        return runtimePrompts;
    }

    public abstract RunSpec defaultRunSpec(AgentConfigFile config);

    public abstract void run(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            OrchestratorServices services,
            FluxSink<AgentDelta> sink
    );
}
