package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.*;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import org.springframework.ai.chat.messages.UserMessage;
import reactor.core.publisher.FluxSink;

import java.util.Map;

public final class ReactMode extends AgentMode {

    private final int maxSteps;

    public ReactMode(String systemPrompt, int maxSteps) {
        super(systemPrompt);
        this.maxSteps = maxSteps > 0 ? maxSteps : 6;
    }

    public int maxSteps() {
        return maxSteps;
    }

    @Override
    public AgentRuntimeMode runtimeMode() {
        return AgentRuntimeMode.REACT;
    }

    @Override
    public RunSpec defaultRunSpec(AgentConfigFile config) {
        Budget budget = PlainMode.chooseBudget(config);
        if (budget.maxSteps() < maxSteps) {
            budget = new Budget(budget.maxModelCalls(), budget.maxToolCalls(), maxSteps, budget.timeoutMs());
        }
        return new RunSpec(
                ControlStrategy.REACT_LOOP,
                OutputPolicy.PLAIN,
                PlainMode.chooseToolPolicy(config, ToolPolicy.ALLOW),
                PlainMode.chooseVerify(config, VerifyPolicy.NONE),
                PlainMode.chooseCompute(config, ComputePolicy.MEDIUM),
                false,
                budget
        );
    }

    @Override
    public void run(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            OrchestratorServices services,
            FluxSink<AgentDelta> sink
    ) {
        int effectiveMaxSteps = context.budget().maxSteps();
        if (context.definition().runSpec().budget().maxSteps() > 0) {
            effectiveMaxSteps = context.definition().runSpec().budget().maxSteps();
        }

        for (int step = 1; step <= effectiveMaxSteps; step++) {
            OrchestratorServices.ModelTurn turn = services.callModelTurnStreaming(
                    context,
                    systemPrompt,
                    context.conversationMessages(),
                    null,
                    services.toolExecutionService().enabledFunctionTools(enabledToolsByName),
                    services.requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO,
                    "agent-react-step-" + step,
                    false,
                    false,
                    true,
                    true,
                    sink
            );

            if (!turn.toolCalls().isEmpty()) {
                services.executeToolsAndEmit(context, enabledToolsByName, turn.toolCalls(), sink);
                continue;
            }

            if (services.requiresTool(context)) {
                context.conversationMessages().add(new UserMessage(
                        "你必须调用至少一个工具来继续。请直接发起工具调用。"
                ));
                continue;
            }

            String finalText = services.normalize(turn.finalText());
            if (finalText.isBlank()) {
                context.conversationMessages().add(new UserMessage("请基于已有信息给出最终答案，或调用工具获取更多信息。"));
                continue;
            }

            services.appendAssistantMessage(context.conversationMessages(), finalText);
            services.emitFinalAnswer(context, context.conversationMessages(), finalText, true, sink);
            return;
        }

        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());
        String forced = services.forceFinalAnswer(
                context,
                systemPrompt,
                context.conversationMessages(),
                "agent-react-force-final",
                !secondPass,
                sink
        );
        services.appendAssistantMessage(context.conversationMessages(), forced);
        services.emitFinalAnswer(context, context.conversationMessages(), forced, !secondPass, sink);
    }
}
