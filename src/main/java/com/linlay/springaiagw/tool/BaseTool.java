package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;

import java.util.Map;

public interface BaseTool {

    String name();

    default String description() {
        return "";
    }

    default String prompt() {
        return "";
    }

    default Map<String, Object> parametersSchema() {
        return Map.of(
                "type", "object",
                "properties", Map.of(),
                "additionalProperties", true
        );
    }

    JsonNode invoke(Map<String, Object> args);
}
