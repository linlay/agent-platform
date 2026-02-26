package com.linlay.agentplatform.stream.model;

import java.util.List;
import java.util.Map;

public sealed interface StreamRequest permits StreamRequest.Query, StreamRequest.Upload, StreamRequest.Submit {

    String requestId();

    record Query(
            String requestId,
            String chatId,
            String role,
            String message,
            String agentKey,
            List<Object> references,
            Map<String, Object> params,
            String scene,
            Boolean stream,
            String chatName,
            String runId
    ) implements StreamRequest {

        public Query {
            requireNonBlank(requestId, "requestId");
            requireNonBlank(chatId, "chatId");
            requireNonBlank(role, "role");
            requireNonNull(message, "message");
        }

        public Query(
                String requestId,
                String chatId,
                String role,
                String message,
                String agentKey,
                List<Object> references,
                Map<String, Object> params,
                String scene,
                Boolean stream,
                String chatName
        ) {
            this(requestId, chatId, role, message, agentKey, references, params, scene, stream, chatName, null);
        }
    }

    record Upload(
            String requestId,
            String chatId,
            String uploadType,
            String uploadName,
            long sizeBytes,
            String mimeType,
            String sha256
    ) implements StreamRequest {

        public Upload {
            requireNonBlank(requestId, "requestId");
            requireNonBlank(uploadType, "upload.type");
            requireNonBlank(uploadName, "upload.name");
            if (sizeBytes < 0) {
                throw new IllegalArgumentException("upload.sizeBytes must be non-negative");
            }
            requireNonBlank(mimeType, "upload.mimeType");
        }
    }

    record Submit(
            String requestId,
            String chatId,
            String runId,
            String toolId,
            Object payload,
            String viewId
    ) implements StreamRequest {

        public Submit {
            requireNonBlank(requestId, "requestId");
            requireNonBlank(chatId, "chatId");
            requireNonBlank(runId, "runId");
            requireNonBlank(toolId, "toolId");
            requireNonNull(payload, "payload");
        }
    }

    private static void requireNonBlank(String value, String fieldName) {
        if (value == null || value.isBlank()) {
            throw new IllegalArgumentException(fieldName + " must not be null or blank");
        }
    }

    private static void requireNonNull(Object value, String fieldName) {
        if (value == null) {
            throw new IllegalArgumentException(fieldName + " must not be null");
        }
    }
}
