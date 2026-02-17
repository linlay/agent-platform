package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.SkillAppend;
import com.linlay.springaiagw.agent.ToolAppend;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.ExecutionContext;
import com.linlay.springaiagw.agent.runtime.ToolExecutionService;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.tool.BaseTool;
import org.springframework.ai.chat.messages.UserMessage;
import reactor.core.publisher.FluxSink;

import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.StringJoiner;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public final class PlanExecuteMode extends AgentMode {

    private static final String PLAN_ADD_TASK_TOOL = "_plan_add_tasks_";
    private static final String PLAN_UPDATE_TASK_TOOL = "_plan_update_task_";
    private static final int MAX_WORK_ROUNDS_PER_TASK = 6;
    private static final Pattern PLACEHOLDER_PATTERN = Pattern.compile("\\{\\{\\s*([a-z0-9_]+)\\s*}}");

    private static final String DEFAULT_TASK_EXECUTION_PROMPT_TEMPLATE = """
            Task list:
            {{task_list}}
            Current task ID: {{task_id}}
            Current task description: {{task_description}}
            Execution rules:
            1) Call at most one tool per round.
            2) You may call any available tool as needed.
            3) Before finishing this task, you MUST call _plan_update_task_ to update its status.
            """;

    private final StageSettings planStage;
    private final StageSettings executeStage;
    private final StageSettings summaryStage;
    private final String taskExecutionPromptTemplate;
    private final int maxSteps;

    public PlanExecuteMode(
            StageSettings planStage,
            StageSettings executeStage,
            StageSettings summaryStage,
            SkillAppend skillAppend,
            ToolAppend toolAppend
    ) {
        this(planStage, executeStage, summaryStage, skillAppend, toolAppend, null, 15);
    }

    public PlanExecuteMode(
            StageSettings planStage,
            StageSettings executeStage,
            StageSettings summaryStage,
            SkillAppend skillAppend,
            ToolAppend toolAppend,
            String taskExecutionPromptTemplate
    ) {
        this(planStage, executeStage, summaryStage, skillAppend, toolAppend, taskExecutionPromptTemplate, 15);
    }

    public PlanExecuteMode(
            StageSettings planStage,
            StageSettings executeStage,
            StageSettings summaryStage,
            SkillAppend skillAppend,
            ToolAppend toolAppend,
            String taskExecutionPromptTemplate,
            int maxSteps
    ) {
        super(executeStage == null ? "" : executeStage.systemPrompt(), skillAppend, toolAppend);
        this.planStage = planStage;
        this.executeStage = executeStage;
        this.summaryStage = summaryStage;
        this.taskExecutionPromptTemplate = taskExecutionPromptTemplate != null && !taskExecutionPromptTemplate.isBlank()
                ? taskExecutionPromptTemplate
                : DEFAULT_TASK_EXECUTION_PROMPT_TEMPLATE;
        this.maxSteps = maxSteps > 0 ? maxSteps : 15;
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

    public int maxSteps() {
        return maxSteps;
    }

    @Override
    public String primarySystemPrompt() {
        for (StageSettings stage : new StageSettings[]{executeStage, summaryStage, planStage}) {
            if (stage != null && stage.systemPrompt() != null && !stage.systemPrompt().isBlank()) {
                return stage.systemPrompt();
            }
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
                config != null && config.getToolChoice() != null ? config.getToolChoice() : ToolChoice.AUTO,
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
        try {
            if (context.definition().runSpec().toolChoice() == ToolChoice.NONE) {
                throw new PlanExecutionStalledException("计划任务执行中断：PLAN_EXECUTE 不支持 ToolChoice.NONE。");
            }

            StageSettings summary = summaryStage == null ? executeStage : summaryStage;
            Map<String, BaseTool> planPromptTools = services.selectTools(enabledToolsByName, planStage.tools());
            Map<String, BaseTool> planCallableTools = selectPlanCallableTools(planPromptTools);
            if (!planCallableTools.containsKey(PLAN_ADD_TASK_TOOL)) {
                throw new PlanExecutionStalledException("计划任务执行中断：缺少必需工具 _plan_add_tasks_，无法创建计划任务。");
            }
            Map<String, BaseTool> executeTools = services.selectTools(enabledToolsByName, executeStage.tools());
            Map<String, BaseTool> summaryTools = services.selectTools(enabledToolsByName, summary.tools());

            StageSettings augmentedPlanStage = augmentPlanStageWithToolPrompts(
                    planStage, executeTools, planCallableTools, services
            );

            if (augmentedPlanStage.deepThinking()) {
                services.emit(sink, AgentDelta.stageMarker("plan-draft"));
                OrchestratorServices.ModelTurn draftTurn = services.callModelTurnStreaming(
                        context,
                        withReasoning(augmentedPlanStage, true),
                        context.planMessages(),
                        null,
                        planPromptTools,
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

            services.emit(sink, AgentDelta.stageMarker("plan-generate"));
            OrchestratorServices.ModelTurn planTurn = services.callModelTurnStreaming(
                    context,
                    withReasoning(augmentedPlanStage, false),
                    context.planMessages(),
                    null,
                    planPromptTools,
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

            while (stepNo < maxSteps) {
                ToolExecutionService.PlanSnapshot beforeSnapshot = services.toolExecutionService().planSnapshot(context);
                AgentDelta.PlanTask step = firstUnfinishedTask(beforeSnapshot.tasks());
                if (step == null) {
                    break;
                }

                stepNo++;
                services.emit(sink, AgentDelta.stageMarker("execute-task-" + stepNo));
                String taskPrompt = renderTemplate(
                        taskExecutionPromptTemplate,
                        Map.of(
                                "task_list", formatTaskList(beforeSnapshot.tasks()),
                                "task_id", str(step.taskId(), "unknown"),
                                "task_description", str(step.description(), "no description")
                        )
                );
                context.executeMessages().add(new UserMessage(taskPrompt));

                executeTaskRounds(context, services, executeTools, stepNo, step, sink);
                ensureTaskNotFailed(context, step);
            }

            services.emit(sink, AgentDelta.stageMarker("summary"));
            OrchestratorServices.ModelTurn finalTurn = services.callModelTurnStreaming(
                    context,
                    summary,
                    context.executeMessages(),
                    null,
                    summaryTools,
                    List.of(),
                    ToolChoice.NONE,
                    "agent-plan-final",
                    false,
                    summary.reasoningEnabled(),
                    true,
                    true,
                    sink
            );
            String finalText = services.normalize(finalTurn.finalText());
            if (finalText.isBlank()) {
                services.emit(sink, AgentDelta.content("执行中断：计划执行完成后未生成可用最终总结。"));
                return;
            }
            services.appendAssistantMessage(context.executeMessages(), finalText);
            services.emitFinalAnswer(finalText, true, sink);
        } catch (PlanExecutionStalledException ex) {
            services.emit(sink, AgentDelta.content(ex.getMessage()));
        }
    }

    /**
     * Execute a single task: work rounds (phase 1) then forced update rounds (phase 2).
     * Throws PlanExecutionStalledException if both phases fail to update task status.
     */
    private void executeTaskRounds(
            ExecutionContext context,
            OrchestratorServices services,
            Map<String, BaseTool> executeTools,
            int stepNo,
            AgentDelta.PlanTask step,
            FluxSink<AgentDelta> sink
    ) {
        // Phase 1: work rounds — model may produce text or call tools freely
        for (int round = 1; round <= MAX_WORK_ROUNDS_PER_TASK; round++) {
            OrchestratorServices.ModelTurn stepTurn = services.callModelTurnStreaming(
                    context,
                    executeStage,
                    context.executeMessages(),
                    null,
                    executeTools,
                    services.toolExecutionService().enabledFunctionTools(executeTools),
                    context.definition().runSpec().toolChoice(),
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
                continue; // allow text-only rounds to continue
            }

            String beforeStatus = statusOfTask(context.planTasks(), step.taskId());
            var first = stepTurn.toolCalls().getFirst();
            services.executeToolsAndEmit(context, executeTools, List.of(first), sink);

            if (isUpdateToolCall(first, step.taskId())) {
                String afterStatus = statusOfTask(context.planTasks(), step.taskId());
                if (afterStatus != null && !Objects.equals(beforeStatus, afterStatus)) {
                    return;
                }
            }
        }

        // Phase 2: forced update — require _plan_update_task_ call
        int forcedUpdateAttempts = services.retryCount(context, 2);
        for (int attempt = 1; attempt <= forcedUpdateAttempts; attempt++) {
            String beforeStatus = statusOfTask(context.planTasks(), step.taskId());

            OrchestratorServices.ModelTurn updateTurn = services.callModelTurnStreaming(
                    context,
                    executeStage,
                    context.executeMessages(),
                    null,
                    executeTools,
                    services.toolExecutionService().enabledFunctionTools(executeTools),
                    ToolChoice.REQUIRED,
                    "agent-plan-execute-step-" + stepNo + (attempt == 1 ? "-update" : "-update-repair"),
                    false,
                    executeStage.reasoningEnabled(),
                    true,
                    true,
                    sink
            );

            if (updateTurn.toolCalls().isEmpty()) {
                continue;
            }

            var first = updateTurn.toolCalls().getFirst();
            services.executeToolsAndEmit(context, executeTools, List.of(first), sink);

            if (isUpdateToolCall(first, step.taskId())) {
                String afterStatus = statusOfTask(context.planTasks(), step.taskId());
                if (afterStatus != null && !Objects.equals(beforeStatus, afterStatus)) {
                    return;
                }
            }
        }

        throw new PlanExecutionStalledException(
                "计划任务执行中断：任务 [" + str(step.taskId(), "unknown")
                        + "] 更新任务状态失败 2 次，请调用 _plan_update_task_ 并提供有效状态。"
        );
    }

    private boolean isUpdateToolCall(com.linlay.springaiagw.agent.PlannedToolCall call, String taskId) {
        if (call == null || call.arguments() == null || call.arguments().isEmpty()) {
            return false;
        }
        if (!PLAN_UPDATE_TASK_TOOL.equals(str(call.name(), "").toLowerCase())) {
            return false;
        }
        Object value = call.arguments().get("taskId");
        return value != null && str(taskId, "").equals(str(value.toString(), ""));
    }

    private boolean containsPlanAddCall(List<com.linlay.springaiagw.agent.PlannedToolCall> calls) {
        if (calls == null || calls.isEmpty()) {
            return false;
        }
        return calls.stream().anyMatch(c -> c != null && PLAN_ADD_TASK_TOOL.equals(str(c.name(), "").toLowerCase()));
    }

    private Map<String, BaseTool> selectPlanCallableTools(Map<String, BaseTool> planTools) {
        BaseTool addTaskTool = planTools == null ? null : planTools.get(PLAN_ADD_TASK_TOOL);
        return addTaskTool == null ? Map.of() : Map.of(PLAN_ADD_TASK_TOOL, addTaskTool);
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
        if ("failed".equals(statusOfTask(context.planTasks(), step.taskId()))) {
            throw new PlanExecutionStalledException(
                    "计划任务执行失败：任务 [" + str(step.taskId(), "unknown") + "] 已被标记为 failed，流程已中断。"
            );
        }
    }

    private AgentDelta.PlanTask firstUnfinishedTask(List<AgentDelta.PlanTask> tasks) {
        if (tasks == null) return null;
        return tasks.stream()
                .filter(t -> t != null && t.taskId() != null && !t.taskId().isBlank())
                .filter(t -> {
                    String s = normalizeStatus(t.status());
                    return !"completed".equals(s) && !"canceled".equals(s) && !"failed".equals(s);
                })
                .findFirst().orElse(null);
    }

    private String formatTaskList(List<AgentDelta.PlanTask> tasks) {
        if (tasks == null || tasks.isEmpty()) {
            return "- (empty)";
        }
        StringJoiner joiner = new StringJoiner("\n");
        for (AgentDelta.PlanTask task : tasks) {
            if (task == null) continue;
            joiner.add("- " + str(task.taskId(), "unknown") + " | "
                    + normalizeStatus(task.status()) + " | "
                    + str(task.description(), "no description"));
        }
        return joiner.length() == 0 ? "- (empty)" : joiner.toString();
    }

    private String statusOfTask(List<AgentDelta.PlanTask> tasks, String taskId) {
        if (tasks == null || taskId == null || taskId.isBlank()) return null;
        return tasks.stream()
                .filter(t -> t != null && t.taskId() != null && taskId.trim().equals(t.taskId().trim()))
                .map(t -> normalizeStatus(t.status()))
                .findFirst().orElse(null);
    }

    private String normalizeStatus(String status) {
        if (status == null || status.isBlank()) {
            return "init";
        }
        String normalized = status.trim().toLowerCase();
        return switch (normalized) {
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
    }

    private String str(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value.trim();
    }

    private StageSettings augmentPlanStageWithToolPrompts(
            StageSettings planStage,
            Map<String, BaseTool> executeTools,
            Map<String, BaseTool> planCallableTools,
            OrchestratorServices services
    ) {
        StringBuilder extraPrompt = new StringBuilder();

        String executeToolDesc = services.toolExecutionService()
                .backendToolDescriptionSection(executeTools, "以下是执行阶段可用工具说明（当前是规划阶段，仅供参考，不允许调用）:");
        if (executeToolDesc != null && !executeToolDesc.isBlank()) {
            extraPrompt.append("\n\n").append(executeToolDesc);
        }

        String planToolDesc = services.toolExecutionService()
                .backendToolDescriptionSection(planCallableTools, "当前规划阶段可调用工具（必须调用 _plan_add_tasks_ 创建计划）:");
        if (planToolDesc != null && !planToolDesc.isBlank()) {
            extraPrompt.append("\n\n").append(planToolDesc);
        }

        if (extraPrompt.isEmpty()) {
            return planStage;
        }

        String augmentedPrompt = (planStage.systemPrompt() == null ? "" : planStage.systemPrompt()) + extraPrompt;
        return new StageSettings(
                augmentedPrompt,
                planStage.providerKey(),
                planStage.model(),
                planStage.tools(),
                planStage.reasoningEnabled(),
                planStage.reasoningEffort(),
                planStage.deepThinking()
        );
    }

    static String renderTemplate(String template, Map<String, String> values) {
        String source = template == null ? "" : template;
        if (values == null || values.isEmpty()) {
            return source;
        }
        Matcher matcher = PLACEHOLDER_PATTERN.matcher(source);
        StringBuffer sb = new StringBuffer();
        while (matcher.find()) {
            String key = matcher.group(1);
            if (!values.containsKey(key)) {
                matcher.appendReplacement(sb, Matcher.quoteReplacement(matcher.group(0)));
                continue;
            }
            String replacement = values.get(key);
            matcher.appendReplacement(sb, Matcher.quoteReplacement(replacement == null ? "" : replacement));
        }
        matcher.appendTail(sb);
        return sb.toString();
    }
}
