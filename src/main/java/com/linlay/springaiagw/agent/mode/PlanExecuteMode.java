package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.policy.*;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import org.springframework.ai.chat.messages.UserMessage;
import reactor.core.publisher.FluxSink;

import java.util.List;
import java.util.Map;

public final class PlanExecuteMode extends AgentMode {

    private final String planSystemPrompt;
    private final String executeSystemPrompt;
    private final String summarySystemPrompt;

    public PlanExecuteMode(String planSystemPrompt, String executeSystemPrompt, String summarySystemPrompt) {
        super(executeSystemPrompt);
        this.planSystemPrompt = planSystemPrompt;
        this.executeSystemPrompt = executeSystemPrompt;
        this.summarySystemPrompt = summarySystemPrompt;
    }

    public String planSystemPrompt() {
        return planSystemPrompt;
    }

    public String executeSystemPrompt() {
        return executeSystemPrompt;
    }

    public String summarySystemPrompt() {
        return summarySystemPrompt;
    }

    @Override
    public String primarySystemPrompt() {
        if (executeSystemPrompt != null && !executeSystemPrompt.isBlank()) {
            return executeSystemPrompt;
        }
        if (planSystemPrompt != null && !planSystemPrompt.isBlank()) {
            return planSystemPrompt;
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
                OutputPolicy.PLAIN,
                PlainMode.chooseToolPolicy(config, ToolPolicy.ALLOW),
                PlainMode.chooseVerify(config, VerifyPolicy.SECOND_PASS_FIX),
                PlainMode.chooseCompute(config, ComputePolicy.HIGH),
                false,
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
        String effectiveSummaryPrompt = summarySystemPrompt;
        if (effectiveSummaryPrompt == null || effectiveSummaryPrompt.isBlank()) {
            effectiveSummaryPrompt = executeSystemPrompt;
        }

        OrchestratorServices.ModelTurn planTurn = services.callModelTurnStreaming(
                context,
                planSystemPrompt,
                context.planMessages(),
                "请输出结构化计划（JSON），包含 steps 字段，每个 step 含 title、goal、successCriteria。",
                List.of(),
                ToolChoice.NONE,
                "agent-plan-generate",
                false,
                false,
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
                    executeSystemPrompt,
                    context.executeMessages(),
                    null,
                    services.toolExecutionService().enabledFunctionTools(enabledToolsByName),
                    services.requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO,
                    "agent-plan-execute-step-" + stepNo,
                    true,
                    false,
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
                        executeSystemPrompt,
                        context.executeMessages(),
                        null,
                        services.toolExecutionService().enabledFunctionTools(enabledToolsByName),
                        ToolChoice.REQUIRED,
                        "agent-plan-execute-step-" + stepNo + "-repair",
                        true,
                        false,
                        true,
                        true,
                        sink
                );
            }

            if (!stepTurn.toolCalls().isEmpty()) {
                services.executeToolsAndEmit(context, enabledToolsByName, stepTurn.toolCalls(), sink);

                OrchestratorServices.ModelTurn stepSummary = services.callModelTurnStreaming(
                        context,
                        executeSystemPrompt,
                        context.executeMessages(),
                        "请总结当前步骤执行结果。",
                        List.of(),
                        ToolChoice.NONE,
                        "agent-plan-step-summary-" + stepNo,
                        false,
                        false,
                        true,
                        true,
                        sink
                );
                String summary = services.normalize(stepSummary.finalText());
                services.appendAssistantMessage(context.executeMessages(), summary);
                if (!summary.isBlank()) {
                    context.toolRecords().add(Map.of(
                            "stepId", step.id(),
                            "stepTitle", step.title(),
                            "summary", summary
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

        String finalText = services.forceFinalAnswer(context, effectiveSummaryPrompt, context.executeMessages(), "agent-plan-final",
                !secondPass, sink);
        services.appendAssistantMessage(context.executeMessages(), finalText);
        services.emitFinalAnswer(context, context.executeMessages(), finalText, !secondPass, sink);
    }
}
