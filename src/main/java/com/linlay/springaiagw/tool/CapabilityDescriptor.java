package com.linlay.springaiagw.tool;

import java.util.Map;

public record CapabilityDescriptor(
        String name,
        String description,
        String afterCallHint,
        Map<String, Object> parameters,
        Boolean strict,
        CapabilityKind kind,
        String toolType,
        String toolApi,
        String viewportKey,
        String sourceFile
) {
    public CapabilityDescriptor {
        if (parameters == null || parameters.isEmpty()) {
            parameters = Map.of(
                    "type", "object",
                    "properties", Map.of(),
                    "additionalProperties", true
            );
        } else {
            parameters = Map.copyOf(parameters);
        }
    }
}
