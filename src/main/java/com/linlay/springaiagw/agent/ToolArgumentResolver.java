package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.time.LocalDate;
import java.time.format.DateTimeParseException;
import java.util.*;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * 解析和替换工具参数中的模板引用（如 {{tool_name.field+Nd}}）和相对日期关键词。
 */
public class ToolArgumentResolver {

    private static final Pattern ARG_TEMPLATE_PATTERN =
            Pattern.compile("^\\{\\{\\s*([a-zA-Z0-9_]+)\\.([a-zA-Z0-9_]+)([+-]\\d+d)?\\s*}}$");
    private static final Set<String> DATE_KEYWORDS_TODAY = Set.of("today", "今天");
    private static final Set<String> DATE_KEYWORDS_TOMORROW = Set.of("tomorrow", "明天");
    private static final Set<String> DATE_KEYWORDS_YESTERDAY = Set.of("yesterday", "昨天");
    private static final Set<String> DATE_KEYWORDS_DAY_AFTER_TOMORROW =
            Set.of("day_after_tomorrow", "day after tomorrow", "后天");
    private static final Set<String> DATE_KEYWORDS_DAY_BEFORE_YESTERDAY =
            Set.of("day_before_yesterday", "day before yesterday", "前天");

    private final ObjectMapper objectMapper;

    public ToolArgumentResolver(ObjectMapper objectMapper) {
        this.objectMapper = objectMapper;
    }

    public Map<String, Object> resolveToolArguments(
            String toolName,
            Map<String, Object> plannedArgs,
            List<Map<String, Object>> records
    ) {
        Map<String, Object> resolved = new LinkedHashMap<>();
        for (Map.Entry<String, Object> entry : plannedArgs.entrySet()) {
            Object value = resolveArgumentValue(entry.getKey(), entry.getValue(), resolved, records);
            resolved.put(entry.getKey(), value);
        }
        if (resolved.isEmpty()) {
            return resolved;
        }
        if ("mock_city_weather".equals(DecisionChunkHandler.normalizeToolName(toolName))) {
            Object rawDate = resolved.get("date");
            if (rawDate instanceof String dateText) {
                String normalizedDate = resolveRelativeDate(
                        dateText,
                        String.valueOf(resolved.getOrDefault("city", "")),
                        records
                );
                resolved.put("date", normalizedDate);
            }
        }
        return resolved;
    }

    private Object resolveArgumentValue(
            String key,
            Object rawValue,
            Map<String, Object> partialResolvedArgs,
            List<Map<String, Object>> records
    ) {
        if (rawValue instanceof String text) {
            String trimmed = text.trim();
            Object templateResolved = resolveTemplateValue(trimmed, records);
            if (templateResolved != null) {
                return templateResolved;
            }
            if ("date".equalsIgnoreCase(DecisionChunkHandler.normalize(key, ""))) {
                String city = String.valueOf(partialResolvedArgs.getOrDefault("city", ""));
                return resolveRelativeDate(trimmed, city, records);
            }
            return rawValue;
        }
        if (rawValue instanceof Map<?, ?> rawMap) {
            Map<String, Object> resolvedMap = new LinkedHashMap<>();
            for (Map.Entry<?, ?> entry : rawMap.entrySet()) {
                String childKey = String.valueOf(entry.getKey());
                Object childValue = resolveArgumentValue(childKey, entry.getValue(), resolvedMap, records);
                resolvedMap.put(childKey, childValue);
            }
            return resolvedMap;
        }
        if (rawValue instanceof List<?> rawList) {
            List<Object> resolvedList = new ArrayList<>();
            for (Object item : rawList) {
                resolvedList.add(resolveArgumentValue(key, item, partialResolvedArgs, records));
            }
            return resolvedList;
        }
        return rawValue;
    }

    private Object resolveTemplateValue(String value, List<Map<String, Object>> records) {
        Matcher matcher = ARG_TEMPLATE_PATTERN.matcher(value);
        if (!matcher.matches()) {
            return null;
        }
        String toolName = DecisionChunkHandler.normalizeToolName(matcher.group(1));
        String fieldName = matcher.group(2);
        String dayOffsetText = matcher.group(3);
        Integer dayOffset = parseDayOffset(dayOffsetText);

        JsonNode fieldNode = latestToolResultField(records, toolName, fieldName);
        if (fieldNode == null || fieldNode.isNull()) {
            return null;
        }
        if (dayOffset != null) {
            if (!fieldNode.isTextual()) {
                return null;
            }
            LocalDate parsed = parseLocalDate(fieldNode.asText());
            if (parsed == null) {
                return null;
            }
            return parsed.plusDays(dayOffset).toString();
        }
        return objectMapper.convertValue(fieldNode, Object.class);
    }

    private Integer parseDayOffset(String dayOffsetText) {
        if (dayOffsetText == null || dayOffsetText.isBlank()) {
            return null;
        }
        String normalized = dayOffsetText.trim().toLowerCase(Locale.ROOT);
        if (!normalized.endsWith("d")) {
            return null;
        }
        try {
            return Integer.parseInt(normalized.substring(0, normalized.length() - 1));
        } catch (NumberFormatException ex) {
            return null;
        }
    }

    JsonNode latestToolResultField(List<Map<String, Object>> records, String toolName, String fieldName) {
        if (records == null || records.isEmpty()) {
            return null;
        }
        for (int i = records.size() - 1; i >= 0; i--) {
            Map<String, Object> record = records.get(i);
            String recordToolName = DecisionChunkHandler.normalizeToolName(
                    String.valueOf(record.getOrDefault("toolName", ""))
            );
            if (!toolName.equals(recordToolName)) {
                continue;
            }
            Object result = record.get("result");
            if (!(result instanceof JsonNode resultNode) || !resultNode.isObject()) {
                continue;
            }
            JsonNode field = resultNode.path(fieldName);
            if (!field.isMissingNode()) {
                return field;
            }
        }
        return null;
    }

    String resolveRelativeDate(String value, String city, List<Map<String, Object>> records) {
        Integer dayOffset = relativeDayOffset(value);
        if (dayOffset == null) {
            return value;
        }
        LocalDate baseDate = latestCityDate(records, city);
        if (baseDate == null) {
            return value;
        }
        return baseDate.plusDays(dayOffset).toString();
    }

    Integer relativeDayOffset(String value) {
        if (value == null || value.isBlank()) {
            return null;
        }
        String trimmed = value.trim();
        String lower = trimmed.toLowerCase(Locale.ROOT);
        if (DATE_KEYWORDS_TODAY.contains(lower) || DATE_KEYWORDS_TODAY.contains(trimmed)) {
            return 0;
        }
        if (DATE_KEYWORDS_TOMORROW.contains(lower) || DATE_KEYWORDS_TOMORROW.contains(trimmed)) {
            return 1;
        }
        if (DATE_KEYWORDS_YESTERDAY.contains(lower) || DATE_KEYWORDS_YESTERDAY.contains(trimmed)) {
            return -1;
        }
        if (DATE_KEYWORDS_DAY_AFTER_TOMORROW.contains(lower) || DATE_KEYWORDS_DAY_AFTER_TOMORROW.contains(trimmed)) {
            return 2;
        }
        if (DATE_KEYWORDS_DAY_BEFORE_YESTERDAY.contains(lower) || DATE_KEYWORDS_DAY_BEFORE_YESTERDAY.contains(trimmed)) {
            return -2;
        }
        return null;
    }

    LocalDate latestCityDate(List<Map<String, Object>> records, String city) {
        if (records == null || records.isEmpty()) {
            return null;
        }
        String normalizedTargetCity = normalizeCity(city);
        for (int i = records.size() - 1; i >= 0; i--) {
            Map<String, Object> record = records.get(i);
            String toolName = DecisionChunkHandler.normalizeToolName(
                    String.valueOf(record.getOrDefault("toolName", ""))
            );
            if (!"city_datetime".equals(toolName)) {
                continue;
            }
            Object result = record.get("result");
            if (!(result instanceof JsonNode resultNode) || !resultNode.isObject()) {
                continue;
            }
            if (!normalizedTargetCity.isBlank()) {
                String recordCity = normalizeCity(resultNode.path("city").asText(""));
                if (!recordCity.isBlank() && !recordCity.equals(normalizedTargetCity)) {
                    continue;
                }
            }
            LocalDate parsed = parseLocalDate(resultNode.path("date").asText(""));
            if (parsed != null) {
                return parsed;
            }
        }
        return null;
    }

    LocalDate parseLocalDate(String text) {
        if (text == null || text.isBlank()) {
            return null;
        }
        try {
            return LocalDate.parse(text.trim());
        } catch (DateTimeParseException ex) {
            return null;
        }
    }

    String normalizeCity(String city) {
        if (city == null) {
            return "";
        }
        String normalized = city.trim().toLowerCase(Locale.ROOT).replace(" ", "");
        Set<String> aliases = new HashSet<>(Set.of("shanghai", "shanghaishi", "上海", "上海市"));
        if (aliases.contains(normalized)) {
            return "shanghai";
        }
        return normalized;
    }
}
