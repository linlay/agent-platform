package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.SkillAppend;
import com.linlay.springaiagw.agent.ToolAppend;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.ControlStrategy;
import com.linlay.springaiagw.agent.runtime.policy.OutputPolicy;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import reactor.core.publisher.FluxSink;

import java.util.List;
import java.util.Map;

public final class ReactMode extends AgentMode {

    private final StageSettings stage;
    private final int maxSteps;

    public ReactMode(StageSettings stage, int maxSteps, SkillAppend skillAppend, ToolAppend toolAppend) {
        super(stage == null ? "" : stage.systemPrompt(), skillAppend, toolAppend);
        this.stage = stage;
        this.maxSteps = maxSteps > 0 ? maxSteps : 6;
    }

    public StageSettings stage() {
        return stage;
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
        Budget budget = config != null && config.getBudget() != null ? config.getBudget().toBudget() : Budget.DEFAULT;
        if (budget.maxSteps() < maxSteps) {
            budget = new Budget(budget.maxModelCalls(), budget.maxToolCalls(), maxSteps, budget.timeoutMs());
        }
        return new RunSpec(
                ControlStrategy.REACT_LOOP,
                config != null && config.getOutput() != null ? config.getOutput() : OutputPolicy.PLAIN,
                config != null && config.getToolPolicy() != null ? config.getToolPolicy() : ToolPolicy.ALLOW,
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
        Map<String, BaseTool> stageTools = services.selectTools(enabledToolsByName, stage.tools());
        int effectiveMaxSteps = context.budget().maxSteps();
        if (context.definition().runSpec().budget().maxSteps() > 0) {
            effectiveMaxSteps = context.definition().runSpec().budget().maxSteps();
        }
        boolean emitReasoning = stage.reasoningEnabled();

        for (int step = 1; step <= effectiveMaxSteps; step++) {
            OrchestratorServices.ModelTurn turn = null;
            for (int retry = 0; retry <= 1; retry++) {
                turn = services.callModelTurnStreaming(
                        context,
                        stage,
                        context.conversationMessages(),
                        null,
                        stageTools,
                        services.toolExecutionService().enabledFunctionTools(stageTools),
                        services.requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO,
                        retry == 0 ? "agent-react-step-" + step : "agent-react-step-" + step + "-retry-" + retry,
                        false,
                        emitReasoning,
                        true,
                        true,
                        sink
                );
                if (!turn.toolCalls().isEmpty()) {
                    break;
                }
                if (!services.requiresTool(context)) {
                    break;
                }
            }

            if (!turn.toolCalls().isEmpty()) {
                services.executeToolsAndEmit(context, stageTools, turn.toolCalls(), sink);
                continue;
            }

            if (services.requiresTool(context)) {
                continue;
            }

            String finalText = services.normalize(turn.finalText());
            if (finalText.isBlank()) {
                continue;
            }

            services.appendAssistantMessage(context.conversationMessages(), finalText);
            services.emitFinalAnswer(
                    finalText,
                    true,
                    sink
            );
            return;
        }

        OrchestratorServices.ModelTurn finalTurn = services.callModelTurnStreaming(
                context,
                stage,
                context.conversationMessages(),
                null,
                stageTools,
                List.of(),
                ToolChoice.NONE,
                "agent-react-final",
                false,
                emitReasoning,
                true,
                true,
                sink
        );
        String forced = services.normalize(finalTurn.finalText());
        if (forced.isBlank()) {
            services.emit(sink, AgentDelta.content("执行中断：达到最大步骤后仍未生成可用最终答案。"));
            return;
        }
        services.appendAssistantMessage(context.conversationMessages(), forced);
        services.emitFinalAnswer(
                forced,
                true,
                sink
        );
    }
}
