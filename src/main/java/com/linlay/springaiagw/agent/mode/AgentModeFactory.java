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
        AgentConfigFile.ReasoningConfig topReasoning = config == null ? null : config.getReasoning();
        AgentConfigFile.ReasoningConfig stageReasoning = stage == null ? null : stage.getReasoning();

        Boolean stageEnabled = stageReasoning == null ? null : stageReasoning.getEnabled();
        boolean reasoningEnabled = stageEnabled != null
                ? stageEnabled
                : (topReasoning != null && Boolean.TRUE.equals(topReasoning.getEnabled()));
        ComputePolicy reasoningEffort = stageReasoning != null && stageReasoning.getEffort() != null
                ? stageReasoning.getEffort()
                : (topReasoning != null && topReasoning.getEffort() != null ? topReasoning.getEffort() : ComputePolicy.MEDIUM);

        List<String> tools = normalizeTools(stage != null && stage.getTools() != null ? stage.getTools() : config == null ? null : config.getTools());

        return new StageSettings(
                normalize(stage == null ? null : stage.getSystemPrompt()),
                normalize(stage == null ? null : stage.getProviderKey()),
                normalize(stage == null ? null : stage.getModel()),
                tools,
                reasoningEnabled,
                reasoningEffort
        );
    }

    private static List<String> normalizeTools(List<String> rawTools) {
        if (rawTools == null || rawTools.isEmpty()) {
            return List.of();
        }
        List<String> tools = new ArrayList<>();
        for (String raw : rawTools) {
            String normalized = normalize(raw);
            if (!isBlank(normalized)) {
                tools.add(normalized.toLowerCase(Locale.ROOT));
            }
        }
        return List.copyOf(tools);
    }

    private static boolean isBlank(String value) {
        return value == null || value.isBlank();
    }

    private static String normalize(String value) {
        return isBlank(value) ? null : value.trim();
    }
}
