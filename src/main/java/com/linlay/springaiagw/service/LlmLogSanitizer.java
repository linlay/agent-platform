package com.linlay.springaiagw.service;

import org.springframework.http.HttpHeaders;

import java.util.List;
import java.util.regex.Pattern;

/**
 * LLM 交互日志脱敏工具。
 * <p>
 * 提供请求头与文本内容的敏感信息掩码能力，避免 Authorization、Token、API Key、
 * Password 等字段在日志中明文输出。该类为纯静态工具类，无状态。
 */
public final class LlmLogSanitizer {

    private static final Pattern JSON_SECRET_VALUE_PATTERN = Pattern.compile(
            "(?i)(\"(?:authorization|api[_-]?key|access[_-]?token|refresh[_-]?token|token|secret|password)\"\\s*:\\s*)\"([^\"]*)\""
    );
    private static final Pattern BEARER_TOKEN_PATTERN = Pattern.compile("(?i)(Bearer\\s+)[A-Za-z0-9._\\-+/=]+");

    private LlmLogSanitizer() {
    }

    public static HttpHeaders maskHeaders(HttpHeaders headers, boolean maskSensitive) {
        HttpHeaders safeHeaders = new HttpHeaders();
        if (headers == null) {
            return safeHeaders;
        }
        safeHeaders.putAll(headers);
        if (!maskSensitive) {
            return safeHeaders;
        }
        for (String key : List.of(
                HttpHeaders.AUTHORIZATION,
                "X-API-Key",
                "Api-Key",
                "X-Access-Token",
                "Access-Token"
        )) {
            if (safeHeaders.containsKey(key)) {
                safeHeaders.set(key, "***");
            }
        }
        return safeHeaders;
    }

    public static String maskText(String text, boolean maskSensitive) {
        if (text == null || text.isEmpty() || !maskSensitive) {
            return text == null ? "" : text;
        }
        String masked = JSON_SECRET_VALUE_PATTERN.matcher(text).replaceAll("$1\"***\"");
        return BEARER_TOKEN_PATTERN.matcher(masked).replaceAll("$1***");
    }
}
