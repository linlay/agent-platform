package com.linlay.agentplatform.model.api;

public record MarkAgentReadResponse(
        String agentKey,
        long ackedEvents,
        long ackedChats,
        long unreadChatCount
) {
}
