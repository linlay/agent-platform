package com.linlay.springaiagw.agent;

public record AgentPromptSet(
        String systemPrompt,
        String planSystemPrompt,
        String executeSystemPrompt,
        String summarySystemPrompt
) {
    public String primarySystemPrompt() {
        if (systemPrompt != null && !systemPrompt.isBlank()) {
            return systemPrompt;
        }
        if (executeSystemPrompt != null && !executeSystemPrompt.isBlank()) {
            return executeSystemPrompt;
        }
        if (planSystemPrompt != null && !planSystemPrompt.isBlank()) {
            return planSystemPrompt;
        }
        return "";
    }
}
