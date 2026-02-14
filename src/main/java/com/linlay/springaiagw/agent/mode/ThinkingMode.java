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

public final class ThinkingMode extends AgentMode {

    private final boolean exposeReasoningToUser;

    public ThinkingMode(String systemPrompt, boolean exposeReasoningToUser) {
        super(systemPrompt);
        this.exposeReasoningToUser = exposeReasoningToUser;
    }

    public boolean exposeReasoningToUser() {
        return exposeReasoningToUser;
    }

    @Override
    public AgentRuntimeMode runtimeMode() {
        return AgentRuntimeMode.THINKING;
    }

    @Override
    public RunSpec defaultRunSpec(AgentConfigFile config) {
        return new RunSpec(
                ControlStrategy.ONESHOT,
                OutputPolicy.PLAIN,
                ToolPolicy.DISALLOW,
                PlainMode.chooseVerify(config, VerifyPolicy.NONE),
                PlainMode.chooseCompute(config, ComputePolicy.HIGH),
                exposeReasoningToUser,
                PlainMode.chooseBudget(config)
        );
    }

    @Override
    public void run(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            OrchestratorServices services,
            FluxSink<AgentDelta> sink
    ) {
        boolean emitReasoning = exposeReasoningToUser;
        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());

        OrchestratorServices.ModelTurn turn = services.callModelTurnStreaming(
                context,
                systemPrompt,
                context.conversationMessages(),
                null,
                List.of(),
                ToolChoice.NONE,
                "agent-thinking-oneshot",
                false,
                emitReasoning,
                !secondPass,
                true,
                sink
        );
        String finalText = services.normalize(turn.finalText());
        services.appendAssistantMessage(context.conversationMessages(), finalText);
        services.emitFinalAnswer(context, context.conversationMessages(), finalText, !secondPass, sink);
    }
}
