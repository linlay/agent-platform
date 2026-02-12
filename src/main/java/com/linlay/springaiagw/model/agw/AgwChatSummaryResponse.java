package com.linlay.springaiagw.model.agw;

public record AgwChatSummaryResponse(
        String chatId,
        String chatName,
        String firstAgentKey,
        long createdAt,
        long updatedAt
) {
}
