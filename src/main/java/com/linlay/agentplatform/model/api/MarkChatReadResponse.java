package com.linlay.agentplatform.model.api;

public record MarkChatReadResponse(
        String chatId,
        int readStatus,
        Long readAt
) {
}
