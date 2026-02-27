package com.linlay.agentplatform.model.api;

import jakarta.validation.constraints.NotBlank;

public record MarkAgentReadRequest(
        @NotBlank
        String agentKey
) {
}
