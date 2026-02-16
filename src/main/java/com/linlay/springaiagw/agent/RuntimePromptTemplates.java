package com.linlay.springaiagw.agent;

import org.springframework.util.StringUtils;

import java.util.Map;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

public final class RuntimePromptTemplates {

    private static final Pattern PLACEHOLDER_PATTERN = Pattern.compile("\\{\\{\\s*([a-z0-9_]+)\\s*}}");
    private static final RuntimePromptTemplates DEFAULTS = new RuntimePromptTemplates(
            new Verify(
                    """
                            请仔细审查答案，确保：
                            1. 答案自洽且逻辑连贯
                            2. 不遗漏用户问题中的关键约束或要求
                            3. 事实性陈述无明显错误
                            4. 若发现问题，输出修复后的完整最终答案
                            5. 若答案无误，原样输出
                            """,
                    "{{candidate_final_text}}"
            ),
            new FinalAnswer(
                    """
                            请基于当前信息直接输出最终答案，禁止再次调用工具。
                            禁止输出任何继续动作（例如“先检查/先查看资源/调用工具”）。
                            若信息不足，请按以下结构回答：
                            1) 已确认信息
                            2) 阻塞点
                            3) 最小下一步
                            """,
                    """
                            已确认信息:
                            {{confirmed_info}}

                            阻塞点:
                            当前回合已禁止继续调用工具，现有信息不足以完成目标。

                            最小下一步:
                            请在下一轮允许工具调用（如 _bash_、_skill_run_script_）后重试，我将继续执行并给出最终结果。
                            """
            ),
            new Oneshot(
                    "你必须调用至少一个工具来完成任务。请重新选择工具并发起调用。",
                    "请基于已有信息输出最终答案，不再调用工具。"
            ),
            new React(
                    "你必须调用至少一个工具来继续。请直接发起工具调用。"
            ),
            new PlanExecute(
                    "execute阶段的可用工具说明（供规划任务使用）:",
                    "本回合仅可调用工具说明:",
                    String.join("\n",
                            "规划回合要求:",
                            "- 请先深度思考并给出任务规划正文。",
                            "- 本回合不要调用工具。"
                    ),
                    String.join("\n",
                            "任务创建要求:",
                            "- 请基于上一轮规划正文，输出 2～4 个任务，并在本回合必须调用 _plan_add_tasks_ 创建计划任务。",
                            "- taskId 应保持唯一且可读，description 需清晰可执行。"
                    ),
                    String.join("\n",
                            "任务创建要求:",
                            "- 请直接规划 2～4 个任务，并在本回合必须调用 _plan_add_tasks_ 创建计划任务。",
                            "- taskId 应保持唯一且可读，description 需清晰可执行。"
                    ),
                    """
                            这是任务列表：
                            {{task_list}}
                            当前要执行的 taskId: {{task_id}}
                            当前任务描述: {{task_description}}
                            执行规则:
                            1) 每个执行回合最多调用一个工具；
                            2) 你可按需调用任意可用工具做准备；
                            3) 结束该任务前必须调用 _plan_update_task_ 更新状态。
                            """,
                    "该执行回合必须调用一个工具。",
                    "每个执行回合最多一个工具调用，系统已仅执行第一个调用。",
                    "_plan_update_task_ 已调用但任务状态未变化，请继续执行并确保状态推进。",
                    "继续执行当前任务，结束前必须调用 _plan_update_task_ 更新状态。",
                    """
                            请立即调用 _plan_update_task_ 更新当前任务状态。
                            当前 taskId: {{task_id}}
                            合法状态: init/completed/failed/canceled
                            """,
                    "更新回合只允许一个工具调用，系统已仅执行第一个调用。",
                    "所有步骤已完成，请综合所有步骤的执行结果给出最终答案。"
            ),
            new Skill(
                    "可用 skills（目录摘要，按需使用，不要虚构不存在的 skill 或脚本）:",
                    "以下是你刚刚调用到的 skill 完整说明（仅本轮补充，不要忽略）:",
                    "instructions"
            ),
            new ToolAppendix(
                    "工具说明:",
                    "工具调用后推荐指令:"
            )
    );

    private final Verify verify;
    private final FinalAnswer finalAnswer;
    private final Oneshot oneshot;
    private final React react;
    private final PlanExecute planExecute;
    private final Skill skill;
    private final ToolAppendix toolAppendix;

    public RuntimePromptTemplates(
            Verify verify,
            FinalAnswer finalAnswer,
            Oneshot oneshot,
            React react,
            PlanExecute planExecute,
            Skill skill,
            ToolAppendix toolAppendix
    ) {
        this.verify = verify;
        this.finalAnswer = finalAnswer;
        this.oneshot = oneshot;
        this.react = react;
        this.planExecute = planExecute;
        this.skill = skill;
        this.toolAppendix = toolAppendix;
    }

    public static RuntimePromptTemplates defaults() {
        return DEFAULTS;
    }

    public static RuntimePromptTemplates fromConfig(AgentConfigFile.RuntimePromptsConfig config) {
        if (config == null) {
            return defaults();
        }
        RuntimePromptTemplates defaults = defaults();
        AgentConfigFile.VerifyPromptConfig verifyConfig = config.getVerify();
        AgentConfigFile.FinalAnswerPromptConfig finalAnswerConfig = config.getFinalAnswer();
        AgentConfigFile.OneshotPromptConfig oneshotConfig = config.getOneshot();
        AgentConfigFile.ReactPromptConfig reactConfig = config.getReact();
        AgentConfigFile.PlanExecutePromptConfig planExecuteConfig = config.getPlanExecute();
        AgentConfigFile.SkillPromptConfig skillConfig = config.getSkill();
        AgentConfigFile.ToolAppendixPromptConfig toolAppendixConfig = config.getToolAppendix();
        return new RuntimePromptTemplates(
                new Verify(
                        pick(verifyConfig == null ? null : verifyConfig.getSystemPrompt(), defaults.verify.systemPrompt()),
                        pick(verifyConfig == null ? null : verifyConfig.getUserPromptTemplate(), defaults.verify.userPromptTemplate())
                ),
                new FinalAnswer(
                        pick(finalAnswerConfig == null ? null : finalAnswerConfig.getForceFinalUserPrompt(),
                                defaults.finalAnswer.forceFinalUserPrompt()),
                        pick(finalAnswerConfig == null ? null : finalAnswerConfig.getBlockedAnswerTemplate(),
                                defaults.finalAnswer.blockedAnswerTemplate())
                ),
                new Oneshot(
                        pick(oneshotConfig == null ? null : oneshotConfig.getRequireToolUserPrompt(),
                                defaults.oneshot.requireToolUserPrompt()),
                        pick(oneshotConfig == null ? null : oneshotConfig.getFinalAnswerUserPrompt(),
                                defaults.oneshot.finalAnswerUserPrompt())
                ),
                new React(
                        pick(reactConfig == null ? null : reactConfig.getRequireToolUserPrompt(),
                                defaults.react.requireToolUserPrompt())
                ),
                new PlanExecute(
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getExecuteToolsTitle(),
                                defaults.planExecute.executeToolsTitle()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getPlanCallableToolsTitle(),
                                defaults.planExecute.planCallableToolsTitle()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getDraftInstructionBlock(),
                                defaults.planExecute.draftInstructionBlock()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getGenerateInstructionBlockFromDraft(),
                                defaults.planExecute.generateInstructionBlockFromDraft()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getGenerateInstructionBlockDirect(),
                                defaults.planExecute.generateInstructionBlockDirect()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getTaskExecutionPromptTemplate(),
                                defaults.planExecute.taskExecutionPromptTemplate()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getTaskRequireToolUserPrompt(),
                                defaults.planExecute.taskRequireToolUserPrompt()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getTaskMultipleToolsUserPrompt(),
                                defaults.planExecute.taskMultipleToolsUserPrompt()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getTaskUpdateNoProgressUserPrompt(),
                                defaults.planExecute.taskUpdateNoProgressUserPrompt()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getTaskContinueUserPrompt(),
                                defaults.planExecute.taskContinueUserPrompt()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getUpdateRoundPromptTemplate(),
                                defaults.planExecute.updateRoundPromptTemplate()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getUpdateRoundMultipleToolsUserPrompt(),
                                defaults.planExecute.updateRoundMultipleToolsUserPrompt()),
                        pick(planExecuteConfig == null ? null : planExecuteConfig.getAllStepsCompletedUserPrompt(),
                                defaults.planExecute.allStepsCompletedUserPrompt())
                ),
                new Skill(
                        pick(skillConfig == null ? null : skillConfig.getCatalogHeader(), defaults.skill.catalogHeader()),
                        pick(skillConfig == null ? null : skillConfig.getDisclosureHeader(), defaults.skill.disclosureHeader()),
                        pick(skillConfig == null ? null : skillConfig.getInstructionsLabel(), defaults.skill.instructionsLabel())
                ),
                new ToolAppendix(
                        pick(toolAppendixConfig == null ? null : toolAppendixConfig.getToolDescriptionTitle(),
                                defaults.toolAppendix.toolDescriptionTitle()),
                        pick(toolAppendixConfig == null ? null : toolAppendixConfig.getAfterCallHintTitle(),
                                defaults.toolAppendix.afterCallHintTitle())
                )
        );
    }

    public Verify verify() {
        return verify;
    }

    public FinalAnswer finalAnswer() {
        return finalAnswer;
    }

    public Oneshot oneshot() {
        return oneshot;
    }

    public React react() {
        return react;
    }

    public PlanExecute planExecute() {
        return planExecute;
    }

    public Skill skill() {
        return skill;
    }

    public ToolAppendix toolAppendix() {
        return toolAppendix;
    }

    public String render(String template, Map<String, String> values) {
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

    public static String pick(String configured, String fallback) {
        return StringUtils.hasText(configured) ? configured.trim() : fallback;
    }

    public record Verify(
            String systemPrompt,
            String userPromptTemplate
    ) {
    }

    public record FinalAnswer(
            String forceFinalUserPrompt,
            String blockedAnswerTemplate
    ) {
    }

    public record Oneshot(
            String requireToolUserPrompt,
            String finalAnswerUserPrompt
    ) {
    }

    public record React(
            String requireToolUserPrompt
    ) {
    }

    public record PlanExecute(
            String executeToolsTitle,
            String planCallableToolsTitle,
            String draftInstructionBlock,
            String generateInstructionBlockFromDraft,
            String generateInstructionBlockDirect,
            String taskExecutionPromptTemplate,
            String taskRequireToolUserPrompt,
            String taskMultipleToolsUserPrompt,
            String taskUpdateNoProgressUserPrompt,
            String taskContinueUserPrompt,
            String updateRoundPromptTemplate,
            String updateRoundMultipleToolsUserPrompt,
            String allStepsCompletedUserPrompt
    ) {
    }

    public record Skill(
            String catalogHeader,
            String disclosureHeader,
            String instructionsLabel
    ) {
    }

    public record ToolAppendix(
            String toolDescriptionTitle,
            String afterCallHintTitle
    ) {
    }
}
