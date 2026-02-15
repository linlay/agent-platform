package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.PlanExecutionStalledException;
import com.linlay.springaiagw.agent.runtime.ToolExecutionService;
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

import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.Objects;

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
                config != null && config.getBudget() != null ? config.getBudget().toBudget() : Budget.HEAVY
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
        Map<String, BaseTool> planTools = services.selectTools(enabledToolsByName, planStage.tools());
        Map<String, BaseTool> executeTools = services.selectTools(enabledToolsByName, executeStage.tools());

        OrchestratorServices.ModelTurn planTurn = services.callModelTurnStreaming(
                context,
                planStage,
                context.planMessages(),
                "请先规划任务。优先调用 _plan_add_tasks_ 添加计划任务；如无法调用工具，请输出可解析的步骤列表。",
                services.toolExecutionService().enabledFunctionTools(planTools),
                planTools.isEmpty() ? ToolChoice.NONE : ToolChoice.AUTO,
                "agent-plan-generate",
                false,
                planStage.reasoningEnabled(),
                true,
                true,
                sink
        );
        if (!planTurn.toolCalls().isEmpty()) {
            services.executeToolsAndEmit(context, planTools, planTurn.toolCalls(), sink);
        }

        if (!context.hasPlan()) {
            List<OrchestratorServices.PlanStep> fallbackSteps = services.parsePlanSteps(planTurn.finalText());
            if (fallbackSteps.isEmpty()) {
                fallbackSteps = List.of(new OrchestratorServices.PlanStep("step-1", "执行任务", context.request().message(), "输出可执行结果"));
            }
            context.initializePlan(context.planId(), toPlanTasks(fallbackSteps));
            services.emit(sink, AgentDelta.planUpdate(context.planId(), context.request().chatId(), context.planTasks()));
        }

        int stepNo = 0;
        int stalledCount = 0;
        String stalledTaskId = null;

        while (stepNo < context.budget().maxSteps()) {
            ToolExecutionService.PlanSnapshot beforeSnapshot = services.toolExecutionService().planSnapshot(context);
            AgentDelta.PlanTask step = firstUnfinishedTask(beforeSnapshot.tasks());
            if (step == null) {
                break;
            }

            stepNo++;
            context.executeMessages().add(new UserMessage(
                    "这是任务列表：\n"
                            + formatTaskList(beforeSnapshot.tasks())
                            + "\n当前要执行的 taskId: " + normalize(step.taskId(), "unknown")
                            + "\n当前任务描述: " + normalize(step.description(), "无描述")
                            + "\n要求: 完成后必须调用 _plan_update_task_ 更新该 task 状态。"
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
                            "stepId", normalize(step.taskId(), "unknown"),
                            "stepTitle", normalize(step.description(), "执行任务"),
                            "summary", summaryText
                    ));
                }
            } else if (!services.normalize(stepTurn.finalText()).isBlank()) {
                services.appendAssistantMessage(context.executeMessages(), services.normalize(stepTurn.finalText()));
                context.toolRecords().add(Map.of(
                        "stepId", normalize(step.taskId(), "unknown"),
                        "stepTitle", normalize(step.description(), "执行任务"),
                        "summary", services.normalize(stepTurn.finalText())
                ));
            }

            ToolExecutionService.PlanSnapshot afterSnapshot = services.toolExecutionService().planSnapshot(context);
            String beforeStatus = normalizeStatus(step.status());
            String afterStatus = statusOfTask(afterSnapshot.tasks(), step.taskId());
            boolean progressed = afterStatus == null || !Objects.equals(beforeStatus, afterStatus);
            if (progressed) {
                stalledTaskId = null;
                stalledCount = 0;
                continue;
            }

            if (Objects.equals(stalledTaskId, step.taskId())) {
                stalledCount++;
            } else {
                stalledTaskId = step.taskId();
                stalledCount = 1;
            }
            if (stalledCount >= 2) {
                throw new PlanExecutionStalledException(
                        "计划任务执行中断：任务 [" + normalize(step.taskId(), "unknown")
                                + "] 连续 2 次无状态推进，请在任务完成后调用 _plan_update_task_ 更新状态。"
                );
            }
        }

        context.executeMessages().add(new UserMessage("所有步骤已完成，请综合所有步骤的执行结果给出最终答案。"));
        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());

        String finalText = services.forceFinalAnswer(context, summary, context.executeMessages(), "agent-plan-final",
                !secondPass, sink);
        services.appendAssistantMessage(context.executeMessages(), finalText);
        services.emitFinalAnswer(context, context.executeMessages(), finalText, !secondPass, sink);
    }

    private List<AgentDelta.PlanTask> toPlanTasks(List<OrchestratorServices.PlanStep> steps) {
        if (steps == null || steps.isEmpty()) {
            return List.of();
        }
        List<AgentDelta.PlanTask> tasks = new ArrayList<>();
        int index = 1;
        for (OrchestratorServices.PlanStep step : steps) {
            if (step == null || step.title() == null || step.title().isBlank()) {
                continue;
            }
            String taskId = normalize(step.id(), "task" + index);
            String description = normalize(step.title(), normalize(step.goal(), "执行任务"));
            tasks.add(new AgentDelta.PlanTask(
                    taskId,
                    description,
                    "init"
            ));
            index++;
        }
        return List.copyOf(tasks);
    }

    private AgentDelta.PlanTask firstUnfinishedTask(List<AgentDelta.PlanTask> tasks) {
        if (tasks == null || tasks.isEmpty()) {
            return null;
        }
        for (AgentDelta.PlanTask task : tasks) {
            if (task == null || task.taskId() == null || task.taskId().isBlank()) {
                continue;
            }
            String status = normalizeStatus(task.status());
            if (!"completed".equals(status) && !"canceled".equals(status)) {
                return task;
            }
        }
        return null;
    }

    private String formatTaskList(List<AgentDelta.PlanTask> tasks) {
        if (tasks == null || tasks.isEmpty()) {
            return "- (空)";
        }
        StringBuilder builder = new StringBuilder();
        boolean first = true;
        for (AgentDelta.PlanTask task : tasks) {
            if (task == null) {
                continue;
            }
            if (!first) {
                builder.append('\n');
            }
            first = false;
            builder.append("- ")
                    .append(normalize(task.taskId(), "unknown"))
                    .append(" | ")
                    .append(normalizeStatus(task.status()))
                    .append(" | ")
                    .append(normalize(task.description(), "无描述"));
        }
        if (builder.isEmpty()) {
            return "- (空)";
        }
        return builder.toString();
    }

    private String statusOfTask(List<AgentDelta.PlanTask> tasks, String taskId) {
        if (tasks == null || tasks.isEmpty() || taskId == null || taskId.isBlank()) {
            return null;
        }
        for (AgentDelta.PlanTask task : tasks) {
            if (task == null || task.taskId() == null) {
                continue;
            }
            if (taskId.trim().equals(task.taskId().trim())) {
                return normalizeStatus(task.status());
            }
        }
        return null;
    }

    private String normalizeStatus(String status) {
        if (status == null || status.isBlank()) {
            return "init";
        }
        return status.trim().toLowerCase();
    }

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value.trim();
    }
}
