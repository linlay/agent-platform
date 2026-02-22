package com.linlay.agentplatform.service;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.config.ChatEventCallbackProperties;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

import javax.crypto.Mac;
import javax.crypto.spec.SecretKeySpec;
import java.net.URI;
import java.net.http.HttpClient;
import java.net.http.HttpRequest;
import java.net.http.HttpResponse;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.time.Instant;
import java.util.LinkedHashMap;
import java.util.Map;

@Service
public class ChatEventCallbackService {

    private static final Logger log = LoggerFactory.getLogger(ChatEventCallbackService.class);

    private final ChatEventCallbackProperties properties;
    private final ObjectMapper objectMapper;
    private final HttpClient httpClient;

    public ChatEventCallbackService(ChatEventCallbackProperties properties, ObjectMapper objectMapper) {
        this.properties = properties;
        this.objectMapper = objectMapper;
        this.httpClient = HttpClient.newBuilder()
            .connectTimeout(Duration.ofMillis(Math.max(100L, properties.getConnectTimeoutMs())))
            .build();
    }

    public void notifyNewContent(String chatId, String runId, String chatName) {
        if (!properties.isEnabled()) {
            return;
        }
        if (!StringUtils.hasText(chatId) || !StringUtils.hasText(runId)) {
            return;
        }
        if (!StringUtils.hasText(properties.getUrl()) || !StringUtils.hasText(properties.getSecret())) {
            return;
        }

        String body;
        String timestamp = String.valueOf(Instant.now().getEpochSecond());
        try {
            body = objectMapper.writeValueAsString(buildPayload(chatId, runId, chatName));
        } catch (Exception ex) {
            log.warn(
                "Failed to build chat event callback payload chatId={}, runId={}, error={}",
                chatId,
                runId,
                summarizeThrowable(ex)
            );
            return;
        }

        String signature;
        try {
            signature = sign(properties.getSecret(), timestamp, body);
        } catch (Exception ex) {
            log.warn(
                "Failed to sign chat event callback payload chatId={}, runId={}, error={}",
                chatId,
                runId,
                summarizeThrowable(ex)
            );
            return;
        }

        HttpRequest request;
        try {
            request = HttpRequest.newBuilder(URI.create(properties.getUrl().trim()))
                .timeout(Duration.ofMillis(Math.max(200L, properties.getRequestTimeoutMs())))
                .header("Content-Type", "application/json")
                .header("X-AGENT-Timestamp", timestamp)
                .header("X-AGENT-Signature", signature)
                .POST(HttpRequest.BodyPublishers.ofString(body, StandardCharsets.UTF_8))
                .build();
        } catch (Exception ex) {
            log.warn(
                "Invalid callback URL configured url={}, error={}",
                properties.getUrl(),
                summarizeThrowable(ex)
            );
            return;
        }

        httpClient.sendAsync(request, HttpResponse.BodyHandlers.discarding())
            .whenComplete((response, throwable) -> {
                if (throwable != null) {
                    log.warn(
                        "chat event callback failed chatId={}, runId={}, url={}, error={}",
                        chatId,
                        runId,
                        properties.getUrl(),
                        summarizeThrowable(throwable)
                    );
                    return;
                }
                if (response == null || response.statusCode() >= 300) {
                    log.warn(
                        "chat event callback rejected status={} chatId={}, runId={}, url={}",
                        response == null ? -1 : response.statusCode(),
                        chatId,
                        runId,
                        properties.getUrl()
                    );
                }
            });
    }

    private String summarizeThrowable(Throwable throwable) {
        if (throwable == null) {
            return "unknown";
        }
        Throwable cursor = throwable;
        while (cursor.getCause() != null && cursor.getCause() != cursor) {
            cursor = cursor.getCause();
        }
        String message = StringUtils.hasText(cursor.getMessage()) ? cursor.getMessage().trim() : "no message";
        return cursor.getClass().getSimpleName() + ": " + message;
    }

    private Map<String, Object> buildPayload(String chatId, String runId, String chatName) {
        Map<String, Object> payload = new LinkedHashMap<>();
        payload.put("chatId", chatId.trim());
        payload.put("runId", runId.trim());
        payload.put("chatName", StringUtils.hasText(chatName) ? chatName.trim() : "");
        payload.put("updatedAt", Instant.now().toEpochMilli());
        return payload;
    }

    private String sign(String secret, String timestamp, String body) throws Exception {
        Mac mac = Mac.getInstance("HmacSHA256");
        mac.init(new SecretKeySpec(secret.getBytes(StandardCharsets.UTF_8), "HmacSHA256"));
        byte[] digest = mac.doFinal((timestamp + "." + body).getBytes(StandardCharsets.UTF_8));
        StringBuilder hex = new StringBuilder(digest.length * 2);
        for (byte b : digest) {
            hex.append(String.format("%02x", b));
        }
        return hex.toString();
    }
}
