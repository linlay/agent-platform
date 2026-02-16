package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import org.springframework.stereotype.Component;

import java.util.Locale;
import java.util.Map;

@Component
public class SystemPlanUpdateTask extends AbstractDeterministicTool {

    @Override
    public String name() {
        return "_plan_update_task_";
    }

    @Override
    public String description() {
        return "更新计划中的任务状态。status 仅支持 init/completed/failed/canceled；成功返回 OK，失败返回 失败: 原因。";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        String taskId = readString(args, "taskId");
        if (taskId == null || taskId.isBlank()) {
            return OBJECT_MAPPER.getNodeFactory().textNode("失败: 缺少 taskId");
        }
        String status = normalizeStatusStrict(readString(args, "status"));
        if (status == null) {
            return OBJECT_MAPPER.getNodeFactory().textNode("失败: 非法状态，仅支持 init/completed/failed/canceled");
        }
        return OBJECT_MAPPER.getNodeFactory().textNode("OK");
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
}
