package com.linlay.agentplatform.stream.model;

import java.util.LinkedHashMap;
import java.util.Map;
import java.util.Set;

public record StreamEvent(
        long seq,
        String type,
        long timestamp,
        Map<String, Object> payload,
        Object rawEvent
) {

    private static final Set<String> RESERVED_KEYS = Set.of("seq", "type", "timestamp", "rawEvent");

    public StreamEvent {
        if (type == null || type.isBlank()) {
            throw new IllegalArgumentException("type must not be null or blank");
        }
        if (seq < 0) {
            throw new IllegalArgumentException("seq must not be negative");
        }
        if (payload == null) {
            payload = Map.of();
        }
    }

    public StreamEvent(long seq, String type, long timestamp, Map<String, Object> payload) {
        this(seq, type, timestamp, payload, null);
    }

    public Map<String, Object> toData() {
        Map<String, Object> data = new LinkedHashMap<>();
        data.put("seq", seq);
        data.put("type", type);
        data.put("timestamp", timestamp);
        if (payload != null && !payload.isEmpty()) {
            payload.forEach((k, v) -> {
                if (!RESERVED_KEYS.contains(k)) {
                    data.put(k, v);
                }
            });
        }
        return data;
    }
}
