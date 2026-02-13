package com.linlay.springaiagw.agent;

import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.fasterxml.jackson.annotation.JsonCreator;

public enum AgentMode {
    PLAIN(AgentRuntimeMode.PLAIN),
    THINKING(AgentRuntimeMode.THINKING),
    PLAIN_TOOLING(AgentRuntimeMode.PLAIN_TOOLING),
    THINKING_TOOLING(AgentRuntimeMode.THINKING_TOOLING),
    REACT(AgentRuntimeMode.REACT),
    PLAN_EXECUTE(AgentRuntimeMode.PLAN_EXECUTE);

    private final AgentRuntimeMode runtimeMode;

    AgentMode(AgentRuntimeMode runtimeMode) {
        this.runtimeMode = runtimeMode;
    }

    public AgentRuntimeMode runtimeMode() {
        return runtimeMode;
    }

    @JsonCreator
    public static AgentMode fromJson(String raw) {
        if (raw == null || raw.isBlank()) {
            return null;
        }
        String normalized = raw.trim().replace('-', '_').toUpperCase();
        return switch (normalized) {
            case "PLAIN" -> PLAIN;
            case "THINKING" -> THINKING;
            case "PLAIN_TOOLING" -> PLAIN_TOOLING;
            case "THINKING_TOOLING" -> THINKING_TOOLING;
            case "REACT", "RE_ACT" -> REACT;
            case "PLAN_EXECUTE" -> PLAN_EXECUTE;
            default -> throw new IllegalArgumentException("Unknown AgentMode: " + raw);
        };
    }
}
