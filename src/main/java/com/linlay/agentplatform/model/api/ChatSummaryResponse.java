package com.linlay.agentplatform.model.api;

public record ChatSummaryResponse(
        String chatId,
        String chatName,
        String agentKey,
        long createdAt,
        long updatedAt,
        String lastRunId,
        String lastRunContent,
        int readStatus,
        Long readAt
) {
}
