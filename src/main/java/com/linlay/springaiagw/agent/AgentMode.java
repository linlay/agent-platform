package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.annotation.JsonCreator;

public enum AgentMode {
    PLAIN,
    RE_ACT,
    PLAN_EXECUTE;

    @JsonCreator
    public static AgentMode fromJson(String raw) {
        if (raw == null || raw.isBlank()) {
            return null;
        }
        String normalized = raw.trim().replace('-', '_').toUpperCase();
        return switch (normalized) {
            case "PLAIN", "PLAIN_CONTENT" -> PLAIN;
            case "RE_ACT", "THINKING_AND_CONTENT" -> RE_ACT;
            case "PLAN_EXECUTE", "THINKING_AND_CONTENT_WITH_DUAL_TOOL_CALLS" -> PLAN_EXECUTE;
            default -> throw new IllegalArgumentException("Unknown AgentMode: " + raw);
        };
    }
}
