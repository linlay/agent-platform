package com.linlay.springaiagw.model.agw;

import com.fasterxml.jackson.annotation.JsonInclude;

import java.util.List;
import java.util.Map;

@JsonInclude(JsonInclude.Include.NON_NULL)
public record AgwChatDetailResponse(
        String chatId,
        String chatName,
        List<Map<String, Object>> messages,
        List<Map<String, Object>> events,
        List<AgwQueryRequest.Reference> references
) {
}
