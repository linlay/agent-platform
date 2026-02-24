package com.linlay.agentplatform.service;

import org.springframework.util.StringUtils;

import java.net.URLDecoder;
import java.nio.charset.StandardCharsets;
import java.nio.file.Path;
import java.util.Locale;

public final class DataFilePathNormalizer {

    private static final String DATA_API_PATH = "/api/ap/data";

    private DataFilePathNormalizer() {
    }

    public static String normalizeFileParam(String file) {
        if (file == null) {
            return null;
        }
        String trimmed = file.trim();
        if (trimmed.isBlank() || trimmed.contains("\\") || trimmed.contains("..")) {
            return null;
        }
        String normalized = trimmed;
        while (normalized.startsWith("/")) {
            normalized = normalized.substring(1);
        }
        if (normalized.isBlank()) {
            return null;
        }
        try {
            Path relativePath = Path.of(normalized).normalize();
            if (relativePath.isAbsolute()) {
                return null;
            }
            return relativePath.toString();
        } catch (Exception ex) {
            return null;
        }
    }

    public static String normalizeAssetReference(String raw) {
        if (!StringUtils.hasText(raw)) {
            return null;
        }
        String value = stripWrapping(raw.trim());
        if (isAbsoluteAssetPath(value)) {
            return null;
        }

        String fromDataApi = extractFileFromDataApi(value);
        if (StringUtils.hasText(fromDataApi)) {
            return normalizeFileParam(fromDataApi);
        }

        int hashIndex = value.indexOf('#');
        if (hashIndex >= 0) {
            value = value.substring(0, hashIndex);
        }
        int queryIndex = value.indexOf('?');
        if (queryIndex >= 0) {
            value = value.substring(0, queryIndex);
        }

        return normalizeFileParam(value);
    }

    public static boolean isAbsoluteAssetPath(String value) {
        if (!StringUtils.hasText(value)) {
            return false;
        }
        String normalized = value.trim().toLowerCase(Locale.ROOT);
        return normalized.startsWith("http://")
                || normalized.startsWith("https://")
                || normalized.startsWith("data:")
                || normalized.startsWith("blob:");
    }

    private static String extractFileFromDataApi(String value) {
        if (!StringUtils.hasText(value)) {
            return null;
        }
        String candidate = value.trim();
        int hashIndex = candidate.indexOf('#');
        if (hashIndex >= 0) {
            candidate = candidate.substring(0, hashIndex);
        }
        int pathIndex = candidate.indexOf(DATA_API_PATH);
        if (pathIndex < 0) {
            return null;
        }
        String pathAndQuery = candidate.substring(pathIndex);
        int queryIndex = pathAndQuery.indexOf('?');
        if (queryIndex < 0) {
            return null;
        }
        String query = pathAndQuery.substring(queryIndex + 1);
        for (String segment : query.split("&")) {
            if (!StringUtils.hasText(segment)) {
                continue;
            }
            int separator = segment.indexOf('=');
            String rawKey = separator >= 0 ? segment.substring(0, separator) : segment;
            String rawValue = separator >= 0 ? segment.substring(separator + 1) : "";
            String key = decode(rawKey);
            if (!"file".equals(key)) {
                continue;
            }
            return decode(rawValue);
        }
        return null;
    }

    private static String decode(String raw) {
        try {
            return URLDecoder.decode(raw, StandardCharsets.UTF_8);
        } catch (Exception ex) {
            return raw;
        }
    }

    private static String stripWrapping(String raw) {
        String value = raw;
        if (value.length() >= 2 && value.startsWith("<") && value.endsWith(">")) {
            value = value.substring(1, value.length() - 1).trim();
        }
        if (value.length() >= 2 && value.startsWith("\"") && value.endsWith("\"")) {
            value = value.substring(1, value.length() - 1).trim();
        }
        if (value.length() >= 2 && value.startsWith("'") && value.endsWith("'")) {
            value = value.substring(1, value.length() - 1).trim();
        }
        return value;
    }
}
