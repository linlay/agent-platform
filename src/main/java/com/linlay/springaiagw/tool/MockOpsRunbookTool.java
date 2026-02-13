package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ArrayNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.util.Map;
import java.util.Random;

@Component
public class MockOpsRunbookTool extends AbstractDeterministicTool {

    private static final String[] RISK_LEVELS = {"low", "medium", "high"};

    @Override
    public String name() {
        return "mock_ops_runbook";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        String message = String.valueOf(args.getOrDefault("message", args.getOrDefault("query", "")));
        String city = String.valueOf(args.getOrDefault("city", "Shanghai"));

        Random random = randomByArgs(args);
        String risk = RISK_LEVELS[random.nextInt(RISK_LEVELS.length)];
        String command = random.nextBoolean() ? "df -h" : "ls -la";

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("tool", name());
        root.put("message", message);
        root.put("city", city);
        root.put("riskLevel", risk);
        root.put("recommendedCommand", command);

        ArrayNode steps = root.putArray("steps");
        steps.add("检查系统负载与磁盘利用率");
        steps.add("确认业务实例状态");
        steps.add("输出巡检摘要");

        root.put("mockTag", "idempotent-random-json");
        return root;
    }
}
