package com.linlay.agentplatform.model.api;

import jakarta.validation.constraints.NotBlank;

public record MarkChatReadRequest(
        @NotBlank
        String chatId
) {
}
