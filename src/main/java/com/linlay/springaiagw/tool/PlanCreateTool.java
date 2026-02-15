package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.util.List;
import java.util.Locale;
import java.util.Map;

@Component
public class PlanCreateTool extends AbstractDeterministicTool {

    @Override
    public String name() {
        return "_plan_create_";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        ObjectNode result = OBJECT_MAPPER.createObjectNode();
        result.put("tool", name());

        ArrayNode tasks = OBJECT_MAPPER.createArrayNode();
        Object rawTasks = args == null ? null : args.get("tasks");
        if (rawTasks instanceof List<?> list) {
            int index = 0;
            for (Object item : list) {
                index++;
                if (!(item instanceof Map<?, ?> map)) {
                    continue;
                }
                String taskId = readString(map, "taskId");
                String description = readString(map, "description");
                String status = normalizeStatus(readString(map, "status"));
                if (description == null || description.isBlank()) {
                    continue;
                }
                ObjectNode task = OBJECT_MAPPER.createObjectNode();
                task.put("taskId", taskId == null || taskId.isBlank() ? "task" + index : taskId.trim());
                task.put("description", description.trim());
                task.put("status", status);
                tasks.add(task);
            }
        }

        String singleDescription = args == null ? null : readString(args, "description");
        if ((rawTasks == null || tasks.isEmpty()) && singleDescription != null && !singleDescription.isBlank()) {
            ObjectNode single = OBJECT_MAPPER.createObjectNode();
            String taskId = args == null ? null : readString(args, "taskId");
            single.put("taskId", taskId == null || taskId.isBlank() ? "task1" : taskId.trim());
            single.put("description", singleDescription.trim());
            single.put("status", normalizeStatus(args == null ? null : readString(args, "status")));
            tasks.add(single);
        }

        if (tasks.isEmpty()) {
            result.put("ok", false);
            result.put("error", "Missing tasks");
            return result;
        }

        result.put("ok", true);
        result.set("tasks", tasks);
        return result;
    }

    private String readString(Map<?, ?> map, String key) {
        if (map == null || key == null) {
            return null;
        }
        Object value = map.get(key);
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
