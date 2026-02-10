package com.linlay.springaiagw.agent;

import com.linlay.springaiagw.model.ProviderType;

import java.util.List;

public record AgentDefinition(
        String id,
        String description,
        ProviderType providerType,
        String model,
        String systemPrompt,
        AgentMode mode,
        List<String> tools
) {
}
