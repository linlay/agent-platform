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

import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.Objects;

public final class PlanExecuteMode extends AgentMode {

    private static final String PLAN_ADD_TASK_TOOL = "_plan_add_tasks_";
    private static final String PLAN_UPDATE_TASK_TOOL = "_plan_update_task_";
    private static final int MAX_WORK_ROUNDS_PER_TASK = 6;

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
        Map<String, BaseTool> planPromptTools = services.selectTools(enabledToolsByName, planStage.tools());
        Map<String, BaseTool> planCallableTools = selectPlanCallableTools(planPromptTools);
        if (!planCallableTools.containsKey(PLAN_ADD_TASK_TOOL)) {
            throw new PlanExecutionStalledException("计划任务执行中断：缺少必需工具 _plan_add_tasks_，无法创建计划任务。");
        }
        Map<String, BaseTool> executeTools = services.selectTools(enabledToolsByName, executeStage.tools());
        Map<String, BaseTool> summaryTools = services.selectTools(enabledToolsByName, summary.tools());

        OrchestratorServices.ModelTurn planTurn = services.callModelTurnStreaming(
                context,
                planStage,
                context.planMessages(),
                "请先深度思考并给出任务规划正文，然后在本回合必须调用 _plan_add_tasks_ 创建计划任务。",
                planPromptTools,
                services.toolExecutionService().enabledFunctionTools(planCallableTools),
                ToolChoice.REQUIRED,
                "agent-plan-generate",
                false,
                planStage.reasoningEnabled(),
                true,
                true,
                sink
        );
        if (!containsPlanAddCall(planTurn.toolCalls())) {
            throw new PlanExecutionStalledException("计划任务执行中断：规划阶段必须调用 _plan_add_tasks_ 创建计划任务。");
        }

        if (!planTurn.toolCalls().isEmpty()) {
            services.executeToolsAndEmit(context, planCallableTools, planTurn.toolCalls(), sink);
        }
        services.appendAssistantMessage(context.planMessages(), services.normalize(planTurn.finalText()));

        if (!context.hasPlan()) {
            throw new PlanExecutionStalledException("计划任务执行中断：_plan_add_tasks_ 未生成有效计划任务。");
        }

        int stepNo = 0;

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
                            + "\n执行规则:"
                            + "\n1) 每个执行回合最多调用一个工具；"
                            + "\n2) 你可按需调用任意可用工具做准备；"
                            + "\n3) 结束该任务前必须调用 _plan_update_task_ 更新状态。"
            ));
            boolean updated = runTaskWorkRounds(context, services, executeTools, stepNo, step, sink);
            if (!updated) {
                updated = runUpdateRound(context, services, executeTools, stepNo, step, false, sink);
            }
            if (!updated) {
                updated = runUpdateRound(context, services, executeTools, stepNo, step, true, sink);
            }
            if (!updated) {
                throw new PlanExecutionStalledException(
                        "计划任务执行中断：任务 [" + normalize(step.taskId(), "unknown")
                                + "] 更新任务状态失败 2 次，请调用 _plan_update_task_ 并提供有效状态。"
                );
            }
            ensureTaskNotFailed(context, step);
        }

        context.executeMessages().add(new UserMessage("所有步骤已完成，请综合所有步骤的执行结果给出最终答案。"));
        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());

        String finalText = services.forceFinalAnswer(context, summary, summaryTools, context.executeMessages(), "agent-plan-final",
                !secondPass, sink);
        services.appendAssistantMessage(context.executeMessages(), finalText);
        services.emitFinalAnswer(context, context.executeMessages(), finalText, !secondPass, sink);
    }

    private boolean runTaskWorkRounds(
            ExecutionContext context,
            OrchestratorServices services,
            Map<String, BaseTool> executeTools,
            int stepNo,
            AgentDelta.PlanTask step,
            FluxSink<AgentDelta> sink
    ) {
        for (int round = 1; round <= MAX_WORK_ROUNDS_PER_TASK; round++) {
            OrchestratorServices.ModelTurn stepTurn = services.callModelTurnStreaming(
                    context,
                    executeStage,
                    context.executeMessages(),
                    null,
                    executeTools,
                    services.toolExecutionService().enabledFunctionTools(executeTools),
                    services.requiresTool(context) ? ToolChoice.REQUIRED : ToolChoice.AUTO,
                    round == 1
                            ? "agent-plan-execute-step-" + stepNo
                            : "agent-plan-execute-step-" + stepNo + "-work-" + round,
                    false,
                    executeStage.reasoningEnabled(),
                    true,
                    true,
                    sink
            );

            String finalText = services.normalize(stepTurn.finalText());
            if (!finalText.isBlank()) {
                services.appendAssistantMessage(context.executeMessages(), finalText);
            }

            if (stepTurn.toolCalls().isEmpty()) {
                if (services.requiresTool(context)) {
                    context.executeMessages().add(new UserMessage("该执行回合必须调用一个工具。"));
                }
                return false;
            }

            boolean multipleTools = stepTurn.toolCalls().size() > 1;
            if (multipleTools) {
                context.executeMessages().add(new UserMessage("每个执行回合最多一个工具调用，系统已仅执行第一个调用。"));
            }
            String beforeStatus = statusOfTask(context.planTasks(), step.taskId());
            var first = stepTurn.toolCalls().getFirst();
            services.executeToolsAndEmit(context, executeTools, List.of(first), sink);

            if (isUpdateToolCall(first, step.taskId())) {
                String afterStatus = statusOfTask(context.planTasks(), step.taskId());
                if (afterStatus != null && !Objects.equals(beforeStatus, afterStatus)) {
                    return true;
                }
                context.executeMessages().add(new UserMessage(
                        "_plan_update_task_ 已调用但任务状态未变化，请继续执行并确保状态推进。"
                ));
            }
            if (multipleTools) {
                return false;
            }
            context.executeMessages().add(new UserMessage("继续执行当前任务，结束前必须调用 _plan_update_task_ 更新状态。"));
        }
        return false;
    }

    private boolean runUpdateRound(
            ExecutionContext context,
            OrchestratorServices services,
            Map<String, BaseTool> executeTools,
            int stepNo,
            AgentDelta.PlanTask step,
            boolean repair,
            FluxSink<AgentDelta> sink
    ) {
        String beforeStatus = statusOfTask(context.planTasks(), step.taskId());
        context.executeMessages().add(new UserMessage(
                "请立即调用 _plan_update_task_ 更新当前任务状态。"
                        + "\n当前 taskId: " + normalize(step.taskId(), "unknown")
                        + "\n合法状态: init/completed/failed/canceled"
        ));

        OrchestratorServices.ModelTurn updateTurn = services.callModelTurnStreaming(
                context,
                executeStage,
                context.executeMessages(),
                null,
                executeTools,
                services.toolExecutionService().enabledFunctionTools(executeTools),
                ToolChoice.REQUIRED,
                repair
                        ? "agent-plan-execute-step-" + stepNo + "-update-repair"
                        : "agent-plan-execute-step-" + stepNo + "-update",
                false,
                executeStage.reasoningEnabled(),
                true,
                true,
                sink
        );

        if (updateTurn.toolCalls().isEmpty()) {
            return false;
        }
        if (updateTurn.toolCalls().size() > 1) {
            context.executeMessages().add(new UserMessage("更新回合只允许一个工具调用，系统已仅执行第一个调用。"));
        }

        var first = updateTurn.toolCalls().getFirst();
        services.executeToolsAndEmit(context, executeTools, List.of(first), sink);
        if (!isUpdateToolCall(first, step.taskId())) {
            return false;
        }

        String afterStatus = statusOfTask(context.planTasks(), step.taskId());
        return afterStatus != null && !Objects.equals(beforeStatus, afterStatus);
    }

    private boolean isUpdateToolCall(com.linlay.springaiagw.agent.PlannedToolCall call, String taskId) {
        if (call == null) {
            return false;
        }
        if (!PLAN_UPDATE_TASK_TOOL.equals(normalize(call.name(), "").toLowerCase())) {
            return false;
        }
        if (call.arguments() == null || call.arguments().isEmpty()) {
            return false;
        }
        Object value = call.arguments().get("taskId");
        if (value == null) {
            return false;
        }
        return normalize(taskId, "").equals(normalize(value.toString(), ""));
    }

    private boolean containsPlanAddCall(List<com.linlay.springaiagw.agent.PlannedToolCall> calls) {
        if (calls == null || calls.isEmpty()) {
            return false;
        }
        return calls.stream()
                .filter(Objects::nonNull)
                .map(call -> normalize(call.name(), "").toLowerCase())
                .anyMatch(name -> PLAN_ADD_TASK_TOOL.equals(name));
    }

    private Map<String, BaseTool> selectPlanCallableTools(Map<String, BaseTool> planTools) {
        if (planTools == null || planTools.isEmpty()) {
            return Map.of();
        }
        BaseTool addTaskTool = planTools.get(PLAN_ADD_TASK_TOOL);
        if (addTaskTool == null) {
            return Map.of();
        }
        Map<String, BaseTool> selected = new LinkedHashMap<>();
        selected.put(PLAN_ADD_TASK_TOOL, addTaskTool);
        return Map.copyOf(selected);
    }

    private void ensureTaskNotFailed(ExecutionContext context, AgentDelta.PlanTask step) {
        String status = statusOfTask(context.planTasks(), step.taskId());
        if ("failed".equals(status)) {
            throw new PlanExecutionStalledException(
                    "计划任务执行失败：任务 [" + normalize(step.taskId(), "unknown")
                            + "] 已被标记为 failed，流程已中断。"
            );
        }
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
            if (!"completed".equals(status) && !"canceled".equals(status) && !"failed".equals(status)) {
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
        String normalized = status.trim().toLowerCase();
        return switch (normalized) {
            case "in_progress" -> "init";
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
    }

    private String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value.trim();
    }
}
