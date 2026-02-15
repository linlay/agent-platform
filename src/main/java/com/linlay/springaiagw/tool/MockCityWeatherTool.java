package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.util.Map;
import java.util.Random;

@Component
public class MockCityWeatherTool extends AbstractDeterministicTool {

    private static final String[] CONDITIONS = {
            "Sunny", "Cloudy", "Partly Cloudy", "Rain", "Thunderstorm", "Fog", "Snow"
    };

    @Override
    public String name() {
        return "mock_city_weather";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        String city = String.valueOf(args.getOrDefault("city", "Shanghai"));
        String date = String.valueOf(args.getOrDefault("date", "1970-01-01"));

        Random random = randomByArgs(args);
        int tempC = random.nextInt(28) + 5;
        int humidity = 35 + random.nextInt(55);
        int windLevel = 1 + random.nextInt(7);
        String condition = CONDITIONS[random.nextInt(CONDITIONS.length)];

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        // root.put("tool", name()); // 不需要返回tool name
        root.put("city", city);
        root.put("date", date);
        root.put("temperatureC", tempC);
        root.put("humidity", humidity);
        root.put("windLevel", windLevel);
        root.put("condition", condition);
        root.put("mockTag", "idempotent-random-json");
        return root;
    }
}
