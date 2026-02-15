package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;

import java.nio.file.Path;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;

public final class AgentModeFactory {

    private AgentModeFactory() {
    }

    public static AgentMode create(AgentRuntimeMode mode, AgentConfigFile config, Path file) {
        return switch (mode) {
            case ONESHOT -> {
                StageSettings stage = stageSettings(config, config == null ? null : config.getPlain());
                if (isBlank(stage.systemPrompt())) {
                    throw new IllegalArgumentException("plain.systemPrompt is required: " + file);
                }
                yield new OneshotMode(stage);
            }
            case REACT -> {
                AgentConfigFile.ReactConfig react = config == null ? null : config.getReact();
                StageSettings stage = stageSettings(config, react);
                if (isBlank(stage.systemPrompt())) {
                    throw new IllegalArgumentException("react.systemPrompt is required: " + file);
                }
                int maxSteps = react != null && react.getMaxSteps() != null ? react.getMaxSteps() : 6;
                yield new ReactMode(stage, maxSteps);
            }
            case PLAN_EXECUTE -> {
                AgentConfigFile.PlanExecuteConfig pe = config == null ? null : config.getPlanExecute();
                StageSettings planStage = stageSettings(config, pe == null ? null : pe.getPlan());
                StageSettings executeStage = stageSettings(config, pe == null ? null : pe.getExecute());
                StageSettings summaryStage = stageSettings(config, pe == null ? null : pe.getSummary());
                if (isBlank(planStage.systemPrompt()) || isBlank(executeStage.systemPrompt())) {
                    throw new IllegalArgumentException(
                            "planExecute.plan.systemPrompt and planExecute.execute.systemPrompt are required: " + file);
                }
                if (isBlank(summaryStage.systemPrompt())) {
                    summaryStage = new StageSettings(
                            executeStage.systemPrompt(),
                            summaryStage.providerKey(),
                            summaryStage.model(),
                            summaryStage.tools(),
                            summaryStage.reasoningEnabled(),
                            summaryStage.reasoningEffort()
                    );
                }
                yield new PlanExecuteMode(planStage, executeStage, summaryStage);
            }
        };
    }

    private static StageSettings stageSettings(AgentConfigFile config, AgentConfigFile.StageConfig stage) {
        AgentConfigFile.ModelConfig resolvedModelConfig = resolveModelConfig(config, stage);
        AgentConfigFile.ReasoningConfig resolvedReasoning = resolvedModelConfig == null ? null : resolvedModelConfig.getReasoning();
        boolean reasoningEnabled = resolvedReasoning != null && Boolean.TRUE.equals(resolvedReasoning.getEnabled());
        ComputePolicy reasoningEffort = resolvedReasoning != null && resolvedReasoning.getEffort() != null
                ? resolvedReasoning.getEffort()
                : ComputePolicy.MEDIUM;
        List<String> tools = resolveTools(config, stage);

        return new StageSettings(
                normalize(stage == null ? null : stage.getSystemPrompt()),
                normalize(resolvedModelConfig == null ? null : resolvedModelConfig.getProviderKey()),
                normalize(resolvedModelConfig == null ? null : resolvedModelConfig.getModel()),
                tools,
                reasoningEnabled,
                reasoningEffort
        );
    }

    private static AgentConfigFile.ModelConfig resolveModelConfig(AgentConfigFile config, AgentConfigFile.StageConfig stage) {
        AgentConfigFile.ModelConfig top = config == null ? null : config.getModelConfig();
        if (stage == null || !stage.isModelConfigProvided() || stage.getModelConfig() == null) {
            return top;
        }
        return stage.getModelConfig();
    }

    private static List<String> resolveTools(AgentConfigFile config, AgentConfigFile.StageConfig stage) {
        AgentConfigFile.ToolConfig top = config == null ? null : config.getToolConfig();
        if (stage == null || !stage.isToolConfigProvided()) {
            return normalizeTools(top);
        }
        if (stage.isToolConfigExplicitNull()) {
            return List.of();
        }
        return normalizeTools(stage.getToolConfig());
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
