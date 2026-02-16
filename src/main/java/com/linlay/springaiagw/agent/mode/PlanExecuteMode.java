package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.RuntimePromptTemplates;
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
        this(planStage, executeStage, summaryStage, RuntimePromptTemplates.defaults());
    }

    public PlanExecuteMode(
            StageSettings planStage,
            StageSettings executeStage,
            StageSettings summaryStage,
            RuntimePromptTemplates runtimePrompts
    ) {
        super(executeStage == null ? "" : executeStage.systemPrompt(), runtimePrompts);
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
        RuntimePromptTemplates.PlanExecute prompts = runtimePrompts().planExecute();
        Map<String, BaseTool> planPromptTools = services.selectTools(enabledToolsByName, planStage.tools());
        Map<String, BaseTool> planCallableTools = selectPlanCallableTools(planPromptTools);
        if (!planCallableTools.containsKey(PLAN_ADD_TASK_TOOL)) {
            throw new PlanExecutionStalledException("计划任务执行中断：缺少必需工具 _plan_add_tasks_，无法创建计划任务。");
        }
        Map<String, BaseTool> executeTools = services.selectTools(enabledToolsByName, executeStage.tools());
        Map<String, BaseTool> summaryTools = services.selectTools(enabledToolsByName, summary.tools());
        String planSharedPrompt = buildPlanSharedPrompt(services, executeTools, planCallableTools);

        if (planStage.deepThinking()) {
            OrchestratorServices.ModelTurn draftTurn = services.callModelTurnStreaming(
                    context,
                    withSystemPrompt(
                            withReasoning(planStage, true),
                            buildPlanDraftPrompt(planSharedPrompt)
                    ),
                    context.planMessages(),
                    null,
                    Map.of(),
                    List.of(),
                    ToolChoice.NONE,
                    "agent-plan-draft",
                    false,
                    true,
                    true,
                    true,
                    false,
                    sink
            );
            services.appendAssistantMessage(context.planMessages(), services.normalize(draftTurn.finalText()));
        }

        OrchestratorServices.ModelTurn planTurn = services.callModelTurnStreaming(
                context,
                withSystemPrompt(
                        withReasoning(planStage, false),
                        buildPlanGeneratePrompt(planSharedPrompt, planStage.deepThinking())
                ),
                context.planMessages(),
                null,
                Map.of(),
                services.toolExecutionService().enabledFunctionTools(planCallableTools),
                ToolChoice.REQUIRED,
                "agent-plan-generate",
                false,
                false,
                true,
                true,
                false,
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
            String taskPrompt = runtimePrompts().render(
                    prompts.taskExecutionPromptTemplate(),
                    Map.of(
                            "task_list", formatTaskList(beforeSnapshot.tasks()),
                            "task_id", normalize(step.taskId(), "unknown"),
                            "task_description", normalize(step.description(), "无描述")
                    )
            );
            context.executeMessages().add(new UserMessage(
                    taskPrompt
            ));
            boolean updated = runTaskWorkRounds(context, services, executeTools, stepNo, step, prompts, sink);
            if (!updated) {
                updated = runUpdateRound(context, services, executeTools, stepNo, step, prompts, false, sink);
            }
            if (!updated) {
                updated = runUpdateRound(context, services, executeTools, stepNo, step, prompts, true, sink);
            }
            if (!updated) {
                throw new PlanExecutionStalledException(
                        "计划任务执行中断：任务 [" + normalize(step.taskId(), "unknown")
                                + "] 更新任务状态失败 2 次，请调用 _plan_update_task_ 并提供有效状态。"
                );
            }
            ensureTaskNotFailed(context, step);
        }

        context.executeMessages().add(new UserMessage(prompts.allStepsCompletedUserPrompt()));
        boolean secondPass = services.verifyService().requiresSecondPass(context.definition().runSpec().verify());

        String finalText = services.forceFinalAnswer(context, summary, summaryTools, context.executeMessages(), "agent-plan-final",
                !secondPass, sink);
        services.appendAssistantMessage(context.executeMessages(), finalText);
        services.emitFinalAnswer(
                context,
                context.executeMessages(),
                finalText,
                !secondPass,
                summary.systemPrompt(),
                sink
        );
    }

    private boolean runTaskWorkRounds(
            ExecutionContext context,
            OrchestratorServices services,
            Map<String, BaseTool> executeTools,
            int stepNo,
            AgentDelta.PlanTask step,
            RuntimePromptTemplates.PlanExecute prompts,
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
                    context.executeMessages().add(new UserMessage(prompts.taskRequireToolUserPrompt()));
                }
                return false;
            }

            boolean multipleTools = stepTurn.toolCalls().size() > 1;
            if (multipleTools) {
                context.executeMessages().add(new UserMessage(prompts.taskMultipleToolsUserPrompt()));
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
                        prompts.taskUpdateNoProgressUserPrompt()
                ));
            }
            if (multipleTools) {
                return false;
            }
            context.executeMessages().add(new UserMessage(prompts.taskContinueUserPrompt()));
        }
        return false;
    }

    private boolean runUpdateRound(
            ExecutionContext context,
            OrchestratorServices services,
            Map<String, BaseTool> executeTools,
            int stepNo,
            AgentDelta.PlanTask step,
            RuntimePromptTemplates.PlanExecute prompts,
            boolean repair,
            FluxSink<AgentDelta> sink
    ) {
        String beforeStatus = statusOfTask(context.planTasks(), step.taskId());
        String updatePrompt = runtimePrompts().render(
                prompts.updateRoundPromptTemplate(),
                Map.of("task_id", normalize(step.taskId(), "unknown"))
        );
        context.executeMessages().add(new UserMessage(
                updatePrompt
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
            context.executeMessages().add(new UserMessage(prompts.updateRoundMultipleToolsUserPrompt()));
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

    private String buildPlanSharedPrompt(
            OrchestratorServices services,
            Map<String, BaseTool> executeTools,
            Map<String, BaseTool> planCallableTools
    ) {
        RuntimePromptTemplates.PlanExecute prompts = runtimePrompts().planExecute();
        List<String> sections = new ArrayList<>();
        sections.add(services.toolExecutionService().backendToolDescriptionSection(
                executeTools,
                prompts.executeToolsTitle()
        ));
        sections.add(services.toolExecutionService().backendToolDescriptionSection(
                planCallableTools,
                prompts.planCallableToolsTitle()
        ));
        return appendPromptSections(planStage.systemPrompt(), sections);
    }

    private String buildPlanDraftPrompt(String sharedPrompt) {
        return appendPromptSections(
                sharedPrompt,
                List.of(runtimePrompts().planExecute().draftInstructionBlock())
        );
    }

    private String buildPlanGeneratePrompt(String sharedPrompt, boolean basedOnDraft) {
        String instructionBlock = basedOnDraft
                ? runtimePrompts().planExecute().generateInstructionBlockFromDraft()
                : runtimePrompts().planExecute().generateInstructionBlockDirect();
        return appendPromptSections(
                sharedPrompt,
                List.of(instructionBlock)
        );
    }

    private String appendPromptSections(String base, List<String> sections) {
        List<String> merged = new ArrayList<>();
        String normalizedBase = normalize(base, "");
        if (!normalizedBase.isBlank()) {
            merged.add(normalizedBase);
        }
        if (sections != null) {
            for (String section : sections) {
                String normalized = normalize(section, "");
                if (!normalized.isBlank()) {
                    merged.add(normalized);
                }
            }
        }
        return String.join("\n\n", merged);
    }

    private StageSettings withSystemPrompt(StageSettings stage, String prompt) {
        if (stage == null) {
            return null;
        }
        return new StageSettings(
                prompt,
                stage.providerKey(),
                stage.model(),
                stage.tools(),
                stage.reasoningEnabled(),
                stage.reasoningEffort(),
                stage.deepThinking()
        );
    }

    private StageSettings withReasoning(StageSettings stage, boolean reasoningEnabled) {
        if (stage == null) {
            return null;
        }
        return new StageSettings(
                stage.systemPrompt(),
                stage.providerKey(),
                stage.model(),
                stage.tools(),
                reasoningEnabled,
                stage.reasoningEffort(),
                stage.deepThinking()
        );
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
