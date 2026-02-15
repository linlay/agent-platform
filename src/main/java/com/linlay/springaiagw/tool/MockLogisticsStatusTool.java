package com.linlay.springaiagw.tool;

import java.time.LocalDate;
import java.time.LocalDateTime;
import java.time.format.DateTimeFormatter;
import java.util.Map;
import java.util.Random;

import org.springframework.stereotype.Component;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;

@Component
public class MockLogisticsStatusTool extends AbstractDeterministicTool {

    private static final String[] CARRIERS = {
            "SF Express", "YTO Express", "ZTO Express", "JD Logistics", "EMS"
    };
    private static final String[] STATUS_FLOW = {
            "ORDER_CONFIRMED", "IN_TRANSIT", "OUT_FOR_DELIVERY", "DELIVERED"
    };
    private static final String[] NODE_FLOW = {
            "Parcel information received",
            "Departed sorting center",
            "Arrived destination city",
            "Courier is delivering",
            "Delivered and signed"
    };
    private static final DateTimeFormatter UPDATED_AT_FORMATTER = DateTimeFormatter.ofPattern("yyyy-MM-dd HH:mm:ss");

    @Override
    public String name() {
        return "mock_logistics_status";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        Random random = randomByArgs(args);

        String trackingNo = readText(args, "trackingNo");
        if (trackingNo.isBlank()) {
            trackingNo = "MOCK" + (100_000 + random.nextInt(900_000));
        }

        String carrier = readText(args, "carrier");
        if (carrier.isBlank()) {
            carrier = CARRIERS[random.nextInt(CARRIERS.length)];
        }

        int statusIndex = random.nextInt(STATUS_FLOW.length);
        String status = STATUS_FLOW[statusIndex];
        String currentNode = NODE_FLOW[Math.min(statusIndex + 1, NODE_FLOW.length - 1)];
        String etaDate = LocalDate.of(2026, 1, 1)
                .plusDays(1 + random.nextInt(5))
                .toString();
        String updatedAt = LocalDateTime.of(2026, 1, 1, 8, 0, 0)
                .plusHours(random.nextInt(120))
                .plusMinutes(random.nextInt(60))
                .format(UPDATED_AT_FORMATTER);

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        // root.put("tool", name()); // 不需要返回tool name
        root.put("trackingNo", trackingNo);
        root.put("carrier", carrier);
        root.put("status", status);
        root.put("currentNode", currentNode);
        root.put("etaDate", etaDate);
        root.put("updatedAt", updatedAt);
        root.put("mockTag", "idempotent-random-json");
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
