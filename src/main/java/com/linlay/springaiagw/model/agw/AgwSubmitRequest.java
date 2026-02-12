package com.linlay.springaiagw.model.agw;

import jakarta.validation.constraints.NotBlank;
import jakarta.validation.constraints.NotNull;

public record AgwSubmitRequest(
        @NotBlank
        String requestId,
        @NotBlank
        String chatId,
        @NotBlank
        String runId,
        @NotBlank
        String toolId,
        String viewId,
        @NotNull
        Object payload
) {
}
