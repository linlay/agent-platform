package com.linlay.springaiagw.model.agw;

import java.util.List;
import java.util.Map;

public record AgwAgentsResponse(
        List<AgentSummary> agents
) {
    public record AgentSummary(
            String key,
            String name,
            String description,
            List<String> capabilities,
            Map<String, Object> meta
    ) {
    }
}
