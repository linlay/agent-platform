package com.linlay.agentplatform.model;

import com.fasterxml.jackson.annotation.JsonCreator;

import java.util.Locale;

public enum ModelProtocol {
    OPENAI,
    ANTHROPIC,
    NEWAPI_OPENAI_COMPATIBLE;

    @JsonCreator
    public static ModelProtocol fromJson(String raw) {
        if (raw == null || raw.isBlank()) {
            return OPENAI;
        }
        String normalized = raw.trim().replace('-', '_').toUpperCase(Locale.ROOT);
        return switch (normalized) {
            case "OPENAI" -> OPENAI;
            case "ANTHROPIC" -> ANTHROPIC;
            case "NEWAPI_OPENAI_COMPATIBLE" -> NEWAPI_OPENAI_COMPATIBLE;
            default -> throw new IllegalArgumentException("Unsupported model protocol: " + raw);
        };
    }
}
