package com.linlay.agentplatform.agent.mode;

import com.linlay.agentplatform.agent.AgentConfigFile;
import com.linlay.agentplatform.agent.SkillAppend;
import com.linlay.agentplatform.agent.ToolAppend;
import com.linlay.agentplatform.agent.runtime.AgentRuntimeMode;
import com.linlay.agentplatform.agent.runtime.policy.ComputePolicy;
import com.linlay.agentplatform.model.ModelDefinition;
import org.springframework.util.StringUtils;

import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.function.Function;

public final class AgentModeFactory {

    private static final String PLAN_ADD_TASK_TOOL = "_plan_add_tasks_";
    private static final String PLAN_UPDATE_TASK_TOOL = "_plan_update_task_";

    private AgentModeFactory() {
    }

    public static AgentMode create(
            AgentRuntimeMode mode,
            AgentConfigFile config,
            Path file,
            Function<String, ModelDefinition> modelResolver
    ) {
        AgentConfigFile.RuntimePromptsConfig runtimePromptsConfig = config == null ? null : config.getRuntimePrompts();
        SkillAppend skillAppend = buildSkillAppend(runtimePromptsConfig);
        ToolAppend toolAppend = buildToolAppend(runtimePromptsConfig);
        String taskExecutionPromptTemplate = buildTaskExecutionPromptTemplate(runtimePromptsConfig);

        return switch (mode) {
            case ONESHOT -> {
                StageSettings stage = stageSettings(config, config == null ? null : config.getPlain(), List.of(), modelResolver);
                if (isBlank(stage.systemPrompt())) {
                    throw new IllegalArgumentException("plain.systemPrompt is required: " + file);
                }
                yield new OneshotMode(stage, skillAppend, toolAppend);
            }
            case REACT -> {
                AgentConfigFile.ReactConfig react = config == null ? null : config.getReact();
                StageSettings stage = stageSettings(config, react, List.of(), modelResolver);
                if (isBlank(stage.systemPrompt())) {
                    throw new IllegalArgumentException("react.systemPrompt is required: " + file);
                }
                int maxSteps = react != null && react.getMaxSteps() != null ? react.getMaxSteps() : 6;
                yield new ReactMode(stage, maxSteps, skillAppend, toolAppend);
            }
            case PLAN_EXECUTE -> {
                AgentConfigFile.PlanExecuteConfig pe = config == null ? null : config.getPlanExecute();
                validatePlanExecuteDeepThinking(pe == null ? null : pe.getExecute(), "planExecute.execute.deepThinking", file);
                validatePlanExecuteDeepThinking(pe == null ? null : pe.getSummary(), "planExecute.summary.deepThinking", file);
                StageSettings planStage = stageSettings(
                        config,
                        pe == null ? null : pe.getPlan(),
                        List.of(PLAN_ADD_TASK_TOOL),
                        modelResolver
                );
                StageSettings executeStage = stageSettings(
                        config,
                        pe == null ? null : pe.getExecute(),
                        List.of(PLAN_UPDATE_TASK_TOOL),
                        modelResolver
                );
                StageSettings summaryStage = stageSettings(
                        config,
                        pe == null ? null : pe.getSummary(),
                        List.of(),
                        modelResolver
                );
                if (isBlank(planStage.systemPrompt()) || isBlank(executeStage.systemPrompt())) {
                    throw new IllegalArgumentException(
                            "planExecute.plan.systemPrompt and planExecute.execute.systemPrompt are required: " + file);
                }
                if (isBlank(summaryStage.systemPrompt())) {
                    summaryStage = new StageSettings(
                            executeStage.systemPrompt(),
                            summaryStage.modelKey(),
                            summaryStage.providerKey(),
                            summaryStage.model(),
                            summaryStage.protocol(),
                            summaryStage.tools(),
                            summaryStage.reasoningEnabled(),
                            summaryStage.reasoningEffort(),
                            summaryStage.deepThinking()
                    );
                }
                int maxSteps = pe != null && pe.getMaxSteps() != null && pe.getMaxSteps() > 0
                        ? pe.getMaxSteps()
                        : 15;
                yield new PlanExecuteMode(
                        planStage,
                        executeStage,
                        summaryStage,
                        skillAppend,
                        toolAppend,
                        taskExecutionPromptTemplate,
                        maxSteps
                );
            }
        };
    }

    private static SkillAppend buildSkillAppend(AgentConfigFile.RuntimePromptsConfig config) {
        if (config == null) {
            return SkillAppend.DEFAULTS;
        }
        AgentConfigFile.SkillPromptConfig skillConfig = config.getSkill();
        if (skillConfig == null) {
            return SkillAppend.DEFAULTS;
        }
        return new SkillAppend(
                pick(skillConfig.getCatalogHeader(), SkillAppend.DEFAULTS.catalogHeader()),
                pick(skillConfig.getDisclosureHeader(), SkillAppend.DEFAULTS.disclosureHeader()),
                pick(skillConfig.getInstructionsLabel(), SkillAppend.DEFAULTS.instructionsLabel())
        );
    }

    private static ToolAppend buildToolAppend(AgentConfigFile.RuntimePromptsConfig config) {
        if (config == null) {
            return ToolAppend.DEFAULTS;
        }
        AgentConfigFile.ToolAppendixPromptConfig toolAppendixConfig = config.getToolAppendix();
        if (toolAppendixConfig == null) {
            return ToolAppend.DEFAULTS;
        }
        return new ToolAppend(
                pick(toolAppendixConfig.getToolDescriptionTitle(), ToolAppend.DEFAULTS.toolDescriptionTitle()),
                pick(toolAppendixConfig.getAfterCallHintTitle(), ToolAppend.DEFAULTS.afterCallHintTitle())
        );
    }

    private static String buildTaskExecutionPromptTemplate(AgentConfigFile.RuntimePromptsConfig config) {
        if (config == null) {
            return null;
        }
        AgentConfigFile.PlanExecutePromptConfig peConfig = config.getPlanExecute();
        if (peConfig == null) {
            return null;
        }
        String template = peConfig.getTaskExecutionPromptTemplate();
        return StringUtils.hasText(template) ? template.trim() : null;
    }

    private static String pick(String configured, String fallback) {
        return StringUtils.hasText(configured) ? configured.trim() : fallback;
    }

    private static void validatePlanExecuteDeepThinking(
            AgentConfigFile.StageConfig stage,
            String fieldPath,
            Path file
    ) {
        if (stage != null && stage.isDeepThinkingProvided()) {
            throw new IllegalArgumentException(fieldPath + " is not allowed: " + file);
        }
    }

    private static StageSettings stageSettings(
            AgentConfigFile config,
            AgentConfigFile.StageConfig stage,
            List<String> requiredTools,
            Function<String, ModelDefinition> modelResolver
    ) {
        AgentConfigFile.ModelConfig resolvedModelConfig = resolveModelConfig(config, stage);
        if (resolvedModelConfig == null || !StringUtils.hasText(resolvedModelConfig.getModelKey())) {
            throw new IllegalArgumentException("modelConfig.modelKey is required");
        }
        if (modelResolver == null) {
            throw new IllegalArgumentException("modelResolver is required");
        }
        String modelKey = normalize(resolvedModelConfig.getModelKey());
        ModelDefinition resolvedModel = modelResolver.apply(modelKey);
        if (resolvedModel == null) {
            throw new IllegalArgumentException("Unknown modelKey: " + modelKey);
        }
        AgentConfigFile.ReasoningConfig resolvedReasoning = resolvedModelConfig == null ? null : resolvedModelConfig.getReasoning();
        boolean reasoningEnabled = resolvedReasoning != null && Boolean.TRUE.equals(resolvedReasoning.getEnabled());
        ComputePolicy reasoningEffort = resolvedReasoning != null && resolvedReasoning.getEffort() != null
                ? resolvedReasoning.getEffort()
                : ComputePolicy.MEDIUM;
        List<String> tools = resolveTools(config, stage, requiredTools);

        return new StageSettings(
                normalize(stage == null ? null : stage.getSystemPrompt()),
                normalize(resolvedModel.key()),
                normalize(resolvedModel.provider()),
                normalize(resolvedModel.modelId()),
                resolvedModel.protocol(),
                tools,
                reasoningEnabled,
                reasoningEffort,
                stage != null && stage.isDeepThinking()
        );
    }

    private static AgentConfigFile.ModelConfig resolveModelConfig(AgentConfigFile config, AgentConfigFile.StageConfig stage) {
        AgentConfigFile.ModelConfig top = config == null ? null : config.getModelConfig();
        if (stage == null || !stage.isModelConfigProvided() || stage.getModelConfig() == null) {
            return top;
        }
        return stage.getModelConfig();
    }

    private static List<String> resolveTools(
            AgentConfigFile config,
            AgentConfigFile.StageConfig stage,
            List<String> requiredTools
    ) {
        AgentConfigFile.ToolConfig top = config == null ? null : config.getToolConfig();
        List<String> resolved;
        if (stage == null || !stage.isToolConfigProvided()) {
            resolved = normalizeTools(top);
            return mergeRequiredTools(resolved, requiredTools);
        }
        if (stage.isToolConfigExplicitNull()) {
            resolved = List.of();
            return mergeRequiredTools(resolved, requiredTools);
        }
        resolved = normalizeTools(stage.getToolConfig());
        return mergeRequiredTools(resolved, requiredTools);
    }

    private static List<String> normalizeTools(AgentConfigFile.ToolConfig toolConfig) {
        if (toolConfig == null) {
            return List.of();
        }
        List<String> tools = new ArrayList<>();
        addTools(tools, toolConfig.getBackends());
        addTools(tools, toolConfig.getFrontends());
        addTools(tools, toolConfig.getActions());
        return tools.stream().distinct().toList();
    }

    private static List<String> mergeRequiredTools(List<String> tools, List<String> requiredTools) {
        if (requiredTools == null || requiredTools.isEmpty()) {
            return tools == null ? List.of() : List.copyOf(tools);
        }
        List<String> merged = new ArrayList<>(tools == null ? List.of() : tools);
        addTools(merged, requiredTools);
        return merged.stream().distinct().toList();
    }

    private static void addTools(List<String> tools, List<String> rawTools) {
        if (rawTools == null || rawTools.isEmpty()) {
            return;
        }
        for (String raw : rawTools) {
            String normalized = normalize(raw);
            if (isBlank(normalized)) {
                continue;
            }
            tools.add(normalized.toLowerCase(Locale.ROOT));
        }
    }

    private static boolean isBlank(String value) {
        return value == null || value.isBlank();
    }

    private static String normalize(String value) {
        return isBlank(value) ? null : value.trim();
    }
}
