package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.node.ObjectNode;
import org.springframework.stereotype.Component;

import java.time.LocalDate;
import java.util.Map;
import java.util.Random;

@Component
public class MockTransportScheduleTool extends AbstractDeterministicTool {

    private static final String[] FLIGHT_NUMBERS = {"MU5137", "CA1502", "CZ3948", "HO1256", "ZH2871"};
    private static final String[] TRAIN_NUMBERS = {"G102", "G356", "D2285", "G7311", "C2610"};
    private static final String[] FLIGHT_STATUSES = {"计划中", "值机中", "正点", "延误"};
    private static final String[] TRAIN_STATUSES = {"计划中", "检票中", "正点", "晚点"};

    @Override
    public String name() {
        return "mock_transport_schedule";
    }

    @Override
    public JsonNode invoke(Map<String, Object> args) {
        Random random = randomByArgs(args);

        String rawType = readText(args, "type");
        String travelType = "train".equalsIgnoreCase(rawType) || "高铁".equals(rawType) ? "高铁" : "航班";

        String fromCity = CityNameTranslator.translate(readTextOrDefault(args, "fromCity", "shanghai"));
        String toCity = CityNameTranslator.translate(readTextOrDefault(args, "toCity", "beijing"));
        String date = readTextOrDefault(args, "date", LocalDate.of(2026, 2, 13).toString());

        int departureHour = 6 + random.nextInt(14);
        int departureMinute = random.nextBoolean() ? 0 : 30;
        int durationMinutes = travelType.equals("航班") ? 90 + random.nextInt(150) : 180 + random.nextInt(240);

        int arrivalTotal = departureHour * 60 + departureMinute + durationMinutes;
        int arrivalHour = (arrivalTotal / 60) % 24;
        int arrivalMinute = arrivalTotal % 60;

        String number = travelType.equals("航班")
                ? FLIGHT_NUMBERS[random.nextInt(FLIGHT_NUMBERS.length)]
                : TRAIN_NUMBERS[random.nextInt(TRAIN_NUMBERS.length)];
        String status = travelType.equals("航班")
                ? FLIGHT_STATUSES[random.nextInt(FLIGHT_STATUSES.length)]
                : TRAIN_STATUSES[random.nextInt(TRAIN_STATUSES.length)];

        String gateOrPlatform = travelType.equals("航班")
                ? "T" + (1 + random.nextInt(2)) + "-" + (10 + random.nextInt(20))
                : (1 + random.nextInt(16)) + " 站台";

        ObjectNode root = OBJECT_MAPPER.createObjectNode();
        root.put("travelType", travelType);
        root.put("number", number);
        root.put("fromCity", fromCity);
        root.put("toCity", toCity);
        root.put("date", date);
        root.put("departureTime", formatHm(departureHour, departureMinute));
        root.put("arrivalTime", formatHm(arrivalHour, arrivalMinute));
        root.put("status", status);
        root.put("gateOrPlatform", gateOrPlatform);
        root.put("mockTag", "幂等随机数据");
        return root;
    }

    private String formatHm(int hour, int minute) {
        return String.format("%02d:%02d", hour, minute);
    }

    private String readText(Map<String, Object> args, String key) {
        Object raw = args.get(key);
        if (raw == null) {
            return "";
        }
        return String.valueOf(raw).trim();
    }

    private String readTextOrDefault(Map<String, Object> args, String key, String fallback) {
        String value = readText(args, key);
        return value.isBlank() ? fallback : value;
    }
}
