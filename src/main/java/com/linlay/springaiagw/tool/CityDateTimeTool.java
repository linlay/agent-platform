package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.time.ZoneId;
import java.time.ZonedDateTime;
import java.time.format.DateTimeFormatter;
import java.util.Locale;
import java.util.Map;

@Component
public class CityDateTimeTool extends AbstractDeterministicTool {

    @Override
    public String name() {
        return "city_datetime";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        String city = String.valueOf(args.getOrDefault("city", "Shanghai"));
        ZoneId zoneId = zoneIdOf(city);
        ZonedDateTime dateTime = ZonedDateTime.now(zoneId);

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("tool", name());
        root.put("city", city);
        root.put("timezone", zoneId.getId());
        root.put("date", dateTime.toLocalDate().toString());
        root.put("time", dateTime.toLocalTime().format(DateTimeFormatter.ofPattern("HH:mm:ss")));
        root.put("iso", dateTime.format(DateTimeFormatter.ISO_OFFSET_DATE_TIME));
        root.put("source", "system-clock");
        return root;
    }

    private ZoneId zoneIdOf(String city) {
        String lower = city.toLowerCase(Locale.ROOT);
        if (lower.contains("beijing") || lower.contains("shanghai") || lower.contains("guangzhou") || lower.contains("shenzhen") || lower.contains("hangzhou") || lower.contains("chengdu") || lower.contains("wuhan") || lower.contains("xian") || lower.contains("中国") || lower.contains("上海") || lower.contains("北京")) {
            return ZoneId.of("Asia/Shanghai");
        }
        if (lower.contains("tokyo") || lower.contains("东京")) {
            return ZoneId.of("Asia/Tokyo");
        }
        if (lower.contains("singapore") || lower.contains("新加坡")) {
            return ZoneId.of("Asia/Singapore");
        }
        if (lower.contains("new york") || lower.contains("nyc") || lower.contains("纽约")) {
            return ZoneId.of("America/New_York");
        }
        if (lower.contains("san francisco") || lower.contains("sf")) {
            return ZoneId.of("America/Los_Angeles");
        }
        return ZoneId.of("UTC");
    }
}
