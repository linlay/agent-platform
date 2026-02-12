package com.linlay.springaiagw.model.agw;

import jakarta.validation.constraints.NotBlank;

import java.util.List;
import java.util.Map;

public record AgwQueryRequest(
        String requestId,
        String chatId,
        String agentKey,
        String role,
        @NotBlank
        String message,
        List<Reference> references,
        Map<String, Object> params,
        Scene scene,
        Boolean stream
) {
    public record Scene(
            String url,
            String title
    ) {
    }

    public record Reference(
            String id,
            String type,
            String name,
            String mimeType,
            Long sizeBytes,
            String url,
            String sha256,
            Map<String, Object> meta
    ) {
    }
}
