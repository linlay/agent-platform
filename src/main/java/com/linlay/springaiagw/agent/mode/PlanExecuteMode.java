package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.ControlStrategy;
import com.linlay.springaiagw.agent.runtime.policy.OutputPolicy;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import org.springframework.ai.chat.messages.UserMessage;
import reactor.core.publisher.FluxSink;

import java.util.List;
import java.util.Map;

public final class PlanExecuteMode extends AgentMode {

    private final StageSettings planStage;
    private final StageSettings executeStage;
    private final StageSettings summaryStage;

    public PlanExecuteMode(StageSettings planStage, StageSettings executeStage, StageSettings summaryStage) {
        super(executeStage == null ? "" : executeStage.systemPrompt());
        this.planStage = planStage;
        this.executeStage = executeStage;
        this.summaryStage = summaryStage;
    }

    public StageSettings planStage() {
        return planStage;
    }

    public StageSettings executeStage() {
        return executeStage;
    }

    public StageSettings summaryStage() {
        return summaryStage;
    }

    @Override
    public String primarySystemPrompt() {
        if (executeStage != null && executeStage.systemPrompt() != null && !executeStage.systemPrompt().isBlank()) {
            return executeStage.systemPrompt();
        }
        if (summaryStage != null && summaryStage.systemPrompt() != null && !summaryStage.systemPrompt().isBlank()) {
            return summaryStage.systemPrompt();
        }
        if (planStage != null && planStage.systemPrompt() != null && !planStage.systemPrompt().isBlank()) {
            return planStage.systemPrompt();
        }
        return "";
    }

    @Override
    public AgentRuntimeMode runtimeMode() {
        return AgentRuntimeMode.PLAN_EXECUTE;
    }

    @Override
    public RunSpec defaultRunSpec(AgentConfigFile config) {
        return new RunSpec(
                ControlStrategy.PLAN_EXECUTE,
                config != null && config.getOutput() != null ? config.getOutput() : OutputPolicy.PLAIN,
                config != null && config.getToolPolicy() != null ? config.getToolPolicy() : ToolPolicy.ALLOW,
                config != null && config.getVerify() != null ? config.getVerify() : VerifyPolicy.SECOND_PASS_FIX,
                config != null && config.getBudget() != null ? config.getBudget().toBudget() : Budget.DEFAULT
        );
    }

    @Override
    public void run(
            ExecutionContext context,
            Map<String, BaseTool> enabledToolsByName,
            OrchestratorServices services,
            FluxSink<AgentDelta> sink
    ) {
        StageSettings summary = summaryStage == null ? executeStage : summaryStage;
        Map<String, BaseTool> executeTools = services.selectTools(enabledToolsByName, executeStage.tools());

        OrchestratorServices.ModelTurn planTurn = services.callModelTurnStreaming(
                context,
                planStage,
                context.planMessages(),
                "请输出结构化计划（JSON），包含 steps 字段，每个 step 含 title、goal、successCriteria。",
                List.of(),
                ToolChoice.NONE,
                "agent-plan-generate",
                false,
                planStage.reasoningEnabled(),
                true,
                true,
                sink
        );
        List<OrchestratorServices.PlanStep> steps = services.parsePlanSteps(planTurn.finalText());
        if (steps.isEmpty()) {
            steps = List.of(new OrchestratorServices.PlanStep("step-1", "执行任务", context.request().message(), "输出可执行结果"));
        }

        int stepNo = 0;
        for (OrchestratorServices.PlanStep step : steps) {
            stepNo++;
            if (stepNo > context.budget().maxSteps()) {
                break;
            }
            context.executeMessages().add(new UserMessage(
                    "当前执行步骤 [" + stepNo + "/" + steps.size() + "]: " + step.title()
                            + "\n目标: " + step.goal()
                            + "\n成功标准: " + step.successCriteria()
            ));

            OrchestratorServices.ModelTurn stepTurn = services.callModelTurnStreaming(
                    context,
                    executeStage,
                    context.executeMessages(),
                    null,
                    services.toolExecutionService().enabledFunctionTools(executeTools),
                    services.requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO,
                    "agent-plan-execute-step-" + stepNo,
                    true,
                    executeStage.reasoningEnabled(),
                    true,
                    true,
                    sink
            );

            if (stepTurn.toolCalls().isEmpty() && services.requiresTool(context)) {
                context.executeMessages().add(new UserMessage(
                        "你必须在该步骤中使用工具。请重新调用至少一个工具。"
                ));
                stepTurn = services.callModelTurnStreaming(
                        context,
                        executeStage,
                        context.executeMessages(),
                        null,
                        services.toolExecutionService().enabledFunctionTools(executeTools),
                        ToolChoice.REQUIRED,
                        "agent-plan-execute-step-" + stepNo + "-repair",
                        true,
                        executeStage.reasoningEnabled(),
                        true,
                        true,
                        sink
                );
            }

            if (!stepTurn.toolCalls().isEmpty()) {
                services.executeToolsAndEmit(context, executeTools, stepTurn.toolCalls(), sink);

                OrchestratorServices.ModelTurn stepSummary = services.callModelTurnStreaming(
                        context,
                        executeStage,
                        context.executeMessages(),
                        "请总结当前步骤执行结果。",
                        List.of(),
                        ToolChoice.NONE,
                        "agent-plan-step-summary-" + stepNo,
                        false,
                        executeStage.reasoningEnabled(),
                        true,
                        true,
                        sink
                );
                String summaryText = services.normalize(stepSummary.finalText());
                services.appendAssistantMessage(context.executeMessages(), summaryText);
                if (!summaryText.isBlank()) {
                    context.toolRecords().add(Map.of(
                            "stepId", step.id(),
                            "stepTitle", step.title(),
                            "summary", summaryText
                    ));
                }
            } else if (!services.normalize(stepTurn.finalText()).isBlank()) {
                services.appendAssistantMessage(context.executeMessages(), services.normalize(stepTurn.finalText()));
                context.toolRecords().add(Map.of(
                        "stepId", step.id(),
                        "stepTitle", step.title(),
                        "summary", services.normalize(stepTurn.finalText())
                ));
            }
        }

        context.executeMessages().add(new UserMessage("所有步骤已完成，请综合所有步骤的执行结果给出最终答案。"));
        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());

        String finalText = services.forceFinalAnswer(context, summary, context.executeMessages(), "agent-plan-final",
                !secondPass, sink);
        services.appendAssistantMessage(context.executeMessages(), finalText);
        services.emitFinalAnswer(context, context.executeMessages(), finalText, !secondPass, sink);
    }
}
