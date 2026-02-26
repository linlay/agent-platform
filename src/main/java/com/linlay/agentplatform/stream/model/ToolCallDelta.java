package com.linlay.agentplatform.stream.model;

public record ToolCallDelta(
        String id,
        Integer index,
        String type,
        String name,
        String arguments
) {

    public ToolCallDelta {
        if (type == null || type.isBlank()) {
            type = "function";
        }
    }

    public ToolCallDelta(String id, String type, String name, String arguments) {
        this(id, null, type, name, arguments);
    }
}
