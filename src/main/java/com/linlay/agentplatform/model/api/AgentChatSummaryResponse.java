package com.linlay.agentplatform.model.api;

public record AgentChatSummaryResponse(
        String agentKey,
        String agentName,
        String avatar,
        String latestChatId,
        String latestChatName,
        String latestChatContent,
        long latestChatTime,
        long unreadChatCount
) {
}
