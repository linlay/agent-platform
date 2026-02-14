package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.*;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import reactor.core.publisher.FluxSink;

import java.util.List;
import java.util.Map;

public final class PlainMode extends AgentMode {

    public PlainMode(String systemPrompt) {
        super(systemPrompt);
    }

    @Override
    public AgentRuntimeMode runtimeMode() {
        return AgentRuntimeMode.PLAIN;
    }

    @Override
    public RunSpec defaultRunSpec(AgentConfigFile config) {
        return new RunSpec(
                ControlStrategy.ONESHOT,
                OutputPolicy.PLAIN,
                ToolPolicy.DISALLOW,
                VerifyPolicy.NONE,
                chooseCompute(config, ComputePolicy.LOW),
                false,
                chooseBudget(config)
        );
    }

    @Override
    public void run(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            OrchestratorServices services,
            FluxSink<AgentDelta> sink
    ) {
        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());

        OrchestratorServices.ModelTurn turn = services.callModelTurnStreaming(
                context,
                systemPrompt,
                context.conversationMessages(),
                null,
                List.of(),
                ToolChoice.NONE,
                "agent-plain-oneshot",
                false,
                false,
                !secondPass,
                true,
                sink
        );
        String finalText = services.normalize(turn.finalText());
        services.appendAssistantMessage(context.conversationMessages(), finalText);
        services.emitFinalAnswer(context, context.conversationMessages(), finalText, !secondPass, sink);
    }

    static ComputePolicy chooseCompute(AgentConfigFile config, ComputePolicy fallback) {
        if (config == null || config.getCompute() == null) {
            return fallback;
        }
        return config.getCompute();
    }

    static VerifyPolicy chooseVerify(AgentConfigFile config, VerifyPolicy fallback) {
        if (config == null || config.getVerify() == null) {
            return fallback;
        }
        return config.getVerify();
    }

    static ToolPolicy chooseToolPolicy(AgentConfigFile config, ToolPolicy fallback) {
        if (config == null || config.getToolPolicy() == null) {
            return fallback;
        }
        return config.getToolPolicy();
    }

    static Budget chooseBudget(AgentConfigFile config) {
        if (config != null && config.getBudget() != null) {
            return config.getBudget().toBudget();
        }
        return Budget.DEFAULT;
    }
}
