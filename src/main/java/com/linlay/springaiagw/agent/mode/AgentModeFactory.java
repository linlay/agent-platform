package com.linlay.springaiagw.agent.mode;

import com.linlay.springaiagw.agent.AgentConfigFile;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;

import java.nio.file.Path;

public final class AgentModeFactory {

    private AgentModeFactory() {
    }

    public static AgentMode create(AgentRuntimeMode mode, AgentConfigFile config, Path file) {
        return switch (mode) {
            case PLAIN -> {
                String prompt = normalize(config.getPlain() == null ? null : config.getPlain().getSystemPrompt());
                if (prompt.isBlank()) {
                    throw new IllegalArgumentException("plain.systemPrompt is required: " + file);
                }
                yield new PlainMode(prompt);
            }
            case THINKING -> {
                String prompt = normalize(config.getThinking() == null ? null : config.getThinking().getSystemPrompt());
                if (prompt.isBlank()) {
                    throw new IllegalArgumentException("thinking.systemPrompt is required: " + file);
                }
                boolean expose = config.getThinking() != null
                        && Boolean.TRUE.equals(config.getThinking().getExposeReasoningToUser());
                yield new ThinkingMode(prompt, expose);
            }
            case PLAIN_TOOLING -> {
                String prompt = normalize(config.getPlainTooling() == null ? null : config.getPlainTooling().getSystemPrompt());
                if (prompt.isBlank()) {
                    throw new IllegalArgumentException("plainTooling.systemPrompt is required: " + file);
                }
                yield new PlainToolingMode(prompt);
            }
            case THINKING_TOOLING -> {
                String prompt = normalize(config.getThinkingTooling() == null ? null : config.getThinkingTooling().getSystemPrompt());
                if (prompt.isBlank()) {
                    throw new IllegalArgumentException("thinkingTooling.systemPrompt is required: " + file);
                }
                boolean expose = config.getThinkingTooling() != null
                        && Boolean.TRUE.equals(config.getThinkingTooling().getExposeReasoningToUser());
                yield new ThinkingToolingMode(prompt, expose);
            }
            case REACT -> {
                String prompt = normalize(config.getReact() == null ? null : config.getReact().getSystemPrompt());
                if (prompt.isBlank()) {
                    throw new IllegalArgumentException("react.systemPrompt is required: " + file);
                }
                int maxSteps = config.getReact() != null && config.getReact().getMaxSteps() != null
                        ? config.getReact().getMaxSteps() : 6;
                yield new ReactMode(prompt, maxSteps);
            }
            case PLAN_EXECUTE -> {
                String planPrompt = normalize(config.getPlanExecute() == null ? null : config.getPlanExecute().getPlanSystemPrompt());
                String executePrompt = normalize(config.getPlanExecute() == null ? null : config.getPlanExecute().getExecuteSystemPrompt());
                String summaryPrompt = normalize(config.getPlanExecute() == null ? null : config.getPlanExecute().getSummarySystemPrompt());
                if (planPrompt.isBlank() || executePrompt.isBlank()) {
                    throw new IllegalArgumentException("planExecute.planSystemPrompt and planExecute.executeSystemPrompt are required: " + file);
                }
                yield new PlanExecuteMode(planPrompt, executePrompt, summaryPrompt.isBlank() ? null : summaryPrompt);
            }
        };
    }

    private static String normalize(String value) {
        return value == null || value.isBlank() ? "" : value;
    }
}
