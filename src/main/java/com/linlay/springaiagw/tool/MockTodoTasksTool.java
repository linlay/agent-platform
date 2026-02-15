package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.time.LocalDate;
import java.util.Map;
import java.util.Random;

@Component
public class MockTodoTasksTool extends AbstractDeterministicTool {

    private static final String[] TASK_POOL = {
            "整理需求文档", "回复客户邮件", "同步项目进展", "代码评审", "准备周会汇报",
            "修复线上缺陷", "更新测试用例", "部署预发环境", "跟进物流异常", "预订差旅行程"
    };

    private static final String[] PRIORITIES = {"高", "中", "低"};
    private static final String[] STATUSES = {"待处理", "进行中", "已完成"};

    @Override
    public String name() {
        return "mock_todo_tasks";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        Random random = randomByArgs(args);

        String owner = readText(args, "owner");
        if (owner.isBlank()) {
            owner = "当前用户";
        }

        int total = 3 + random.nextInt(4);
        ArrayNode tasks = OBJECT_MAPPER.createArrayNode();
        for (int i = 0; i < total; i++) {
            ObjectNode task = OBJECT_MAPPER.createObjectNode();
            task.put("id", "TASK-" + (100 + i));
            task.put("title", TASK_POOL[(i + random.nextInt(TASK_POOL.length)) % TASK_POOL.length]);
            task.put("priority", PRIORITIES[random.nextInt(PRIORITIES.length)]);
            task.put("status", STATUSES[random.nextInt(STATUSES.length)]);
            task.put("dueDate", LocalDate.of(2026, 2, 13).plusDays(1 + random.nextInt(7)).toString());
            tasks.add(task);
        }

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("owner", owner);
        root.put("total", total);
        root.set("tasks", tasks);
        root.put("mockTag", "幂等随机数据");
        return root;
    }

    private String readText(Map<String, Object> args, String key) {
        Object raw = args.get(key);
        if (raw == null) {
            return "";
        }
        return String.valueOf(raw).trim();
    }
}
