package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;

import java.time.LocalDate;
import java.time.format.DateTimeParseException;
import java.util.*;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

/**
 * 解析和替换工具参数中的模板引用（如 {{tool_name.field+Nd}}）。
 */
public class ToolArgumentResolver {

    private static final Pattern ARG_TEMPLATE_PATTERN =
            Pattern.compile("^\\{\\{\\s*([a-zA-Z0-9_]+)\\.([a-zA-Z0-9_]+)([+-]\\d+d)?\\s*}}$");

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
        String toolName = normalizeToolName(matcher.group(1));
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
            String recordToolName = normalizeToolName(
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

    static String normalizeToolName(String raw) {
        return normalize(raw, "").trim().toLowerCase(Locale.ROOT);
    }

    static String normalize(String value, String fallback) {
        return value == null || value.isBlank() ? fallback : value;
    }
}
