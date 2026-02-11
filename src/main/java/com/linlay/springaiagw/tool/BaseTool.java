package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;

import java.util.Map;

public interface BaseTool {

    String name();

    String description();

    default Map<String, Object> parametersSchema() {
        return Map.of(
                "type", "object",
                "properties", Map.of(),
                "additionalProperties", true
        );
    }

    JsonNode invoke(Map<String, Object> args);
}
