package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import org.springframework.stereotype.Component;

import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.UUID;

@Component
public class SystemPlanAddTasks extends AbstractDeterministicTool {

    @Override
    public String name() {
        return "_plan_add_tasks_";
    }

    @Override
    public String description() {
        return "创建计划任务（追加模式），支持一次添加一个或多个任务。成功返回多行文本：taskId | status | description。";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        List<TaskItem> tasks = new ArrayList<>();
        Object rawTasks = args == null ? null : args.get("tasks");
        if (rawTasks instanceof List<?> list) {
            for (Object item : list) {
                if (!(item instanceof Map<?, ?> map)) {
                    continue;
                }
                String description = readString(map, "description");
                if (description == null || description.isBlank()) {
                    continue;
                }
                String status = normalizeStatusStrict(readString(map, "status"));
                if (status == null) {
                    return OBJECT_MAPPER.getNodeFactory().textNode("失败: 非法状态，仅支持 init/completed/failed/canceled");
                }
                tasks.add(new TaskItem(shortId(), description.trim(), status));
            }
        }

        String singleDescription = args == null ? null : readString(args, "description");
        if (tasks.isEmpty() && singleDescription != null && !singleDescription.isBlank()) {
            String status = normalizeStatusStrict(args == null ? null : readString(args, "status"));
            if (status == null) {
                return OBJECT_MAPPER.getNodeFactory().textNode("失败: 非法状态，仅支持 init/completed/failed/canceled");
            }
            tasks.add(new TaskItem(shortId(), singleDescription.trim(), status));
        }

        if (tasks.isEmpty()) {
            return OBJECT_MAPPER.getNodeFactory().textNode("失败: 缺少任务描述");
        }

        StringBuilder text = new StringBuilder();
        for (TaskItem task : tasks) {
            if (text.length() > 0) {
                text.append('\n');
            }
            text.append(task.taskId())
                    .append(" | ")
                    .append(task.status())
                    .append(" | ")
                    .append(task.description());
        }
        return OBJECT_MAPPER.getNodeFactory().textNode(text.toString());
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

    private String normalizeStatusStrict(String raw) {
        if (raw == null || raw.isBlank()) {
            return "init";
        }
        String normalized = raw.trim().toLowerCase(Locale.ROOT);
        return switch (normalized) {
            case "init", "completed", "failed", "canceled" -> normalized;
            default -> null;
        };
    }

    private String shortId() {
        Set<Character> allowed = Set.of('0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
                'a', 'b', 'c', 'd', 'e', 'f');
        String raw = UUID.randomUUID().toString().replace("-", "").toLowerCase(Locale.ROOT);
        StringBuilder id = new StringBuilder(8);
        for (char c : raw.toCharArray()) {
            if (allowed.contains(c)) {
                id.append(c);
                if (id.length() == 8) {
                    break;
                }
            }
        }
        if (id.length() < 8) {
            return (raw + "00000000").substring(0, 8);
        }
        return id.toString();
    }

    private record TaskItem(
            String taskId,
            String description,
            String status
    ) {
    }
}
