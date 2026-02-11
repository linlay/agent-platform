package com.linlay.springaiagw.model.agw;

import jakarta.validation.constraints.NotBlank;

public record AgwSubmitRequest(
        @NotBlank
        String requestId,
        String chatId,
        String runId,
        @NotBlank
        String toolId,
        String viewId,
        Object payload
) {
}
