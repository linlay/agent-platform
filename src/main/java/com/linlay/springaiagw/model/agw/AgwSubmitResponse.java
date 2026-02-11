package com.linlay.springaiagw.model.agw;

public record AgwSubmitResponse(
        String requestId,
        boolean accepted,
        String runId,
        String toolId
) {
}
