package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.util.Locale;
import java.util.Map;

@Component
public class PlanTaskUpdateTool extends AbstractDeterministicTool {

    @Override
    public String name() {
        return "_plan_task_update_";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        ObjectNode result = OBJECT_MAPPER.createObjectNode();
        result.put("tool", name());
        String taskId = readString(args, "taskId");
        String status = normalizeStatus(readString(args, "status"));
        if (taskId == null || taskId.isBlank()) {
            result.put("ok", false);
            result.put("error", "Missing taskId");
            return result;
        }
        result.put("ok", true);
        result.put("taskId", taskId.trim());
        result.put("status", status);
        String description = readString(args, "description");
        if (description != null && !description.isBlank()) {
            result.put("description", description.trim());
        }
        return result;
    }

    private String readString(Map<String, Object> args, String key) {
        if (args == null || key == null) {
            return null;
        }
        Object value = args.get(key);
        if (value == null) {
            return null;
        }
        String text = value.toString();
        return text == null || text.isBlank() ? null : text;
    }

    private String normalizeStatus(String raw) {
        if (raw == null || raw.isBlank()) {
            return "init";
        }
        String normalized = raw.trim().toLowerCase(Locale.ROOT);
        return switch (normalized) {
            case "init", "in_progress", "completed", "failed", "canceled" -> normalized;
            default -> "init";
        };
    }
}
