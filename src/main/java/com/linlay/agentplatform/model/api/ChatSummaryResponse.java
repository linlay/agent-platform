package com.linlay.agentplatform.model.api;

public record ChatSummaryResponse(
        String chatId,
        String chatName,
        String firstAgentKey,
        String firstAgentName,
        long createdAt,
        long updatedAt,
        String agentAvatar
) {
}
