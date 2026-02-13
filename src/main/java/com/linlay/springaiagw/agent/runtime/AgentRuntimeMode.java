package com.linlay.springaiagw.agent.runtime;

import com.fasterxml.jackson.annotation.JsonCreator;

import java.util.Locale;

public enum AgentRuntimeMode {
    PLAIN,
    THINKING,
    PLAIN_TOOLING,
    THINKING_TOOLING,
    REACT,
    PLAN_EXECUTE;

    @JsonCreator
    public static AgentRuntimeMode fromJson(String raw) {
        if (raw == null || raw.isBlank()) {
            return null;
        }
        String normalized = raw.trim().replace('-', '_').toUpperCase(Locale.ROOT);
        return switch (normalized) {
            case "PLAIN" -> PLAIN;
            case "THINKING" -> THINKING;
            case "PLAIN_TOOLING" -> PLAIN_TOOLING;
            case "THINKING_TOOLING" -> THINKING_TOOLING;
            case "REACT", "RE_ACT" -> REACT;
            case "PLAN_EXECUTE" -> PLAN_EXECUTE;
            default -> throw new IllegalArgumentException("Unknown AgentRuntimeMode: " + raw);
        };
    }
}
