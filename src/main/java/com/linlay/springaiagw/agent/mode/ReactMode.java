package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.SkillAppend;
import com.linlay.springaiagw.agent.ToolAppend;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.model.AgentDelta;
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
        return new RunSpec(
                config != null && config.getToolChoice() != null ? config.getToolChoice() : ToolChoice.AUTO,
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
        Map<String, BaseTool> configuredTools = services.selectTools(enabledToolsByName, stage.tools());
        Map<String, BaseTool> stageTools = services.allowsTool(context) ? configuredTools : Map.of();
        int effectiveMaxSteps = maxSteps;
        int retries = services.modelRetryCount(context, 1);
        boolean emitReasoning = stage.reasoningEnabled();

        for (int step = 1; step <= effectiveMaxSteps; step++) {
            services.emit(sink, AgentDelta.stageMarker("react-step-" + step));
            OrchestratorServices.ModelTurn turn = null;
            for (int retry = 0; retry <= retries; retry++) {
                turn = services.callModelTurnStreaming(
                        context,
                        stage,
                        context.conversationMessages(),
                        null,
                        stageTools,
                        services.toolExecutionService().enabledFunctionTools(stageTools),
                        context.definition().runSpec().toolChoice(),
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

        services.emit(sink, AgentDelta.stageMarker("react-step-" + (effectiveMaxSteps + 1)));
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
