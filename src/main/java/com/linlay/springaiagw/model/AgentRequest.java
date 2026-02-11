package com.linlay.springaiagw.model;

import jakarta.validation.constraints.NotBlank;

public record AgentRequest(
        @NotBlank
        String message,
        String city,
        String date,
        String chatId,
        String chatName,
        String requestId,
        String runId
) {
}
