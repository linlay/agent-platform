package com.linlay.springaiagw.agent;

import com.linlay.springaiagw.model.AgentDelta;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.ProviderType;
import reactor.core.publisher.Flux;

import java.util.List;

public interface Agent {

    String id();

    default String description() {
        return id();
    }

    ProviderType providerType();

    String model();

    String systemPrompt();

    default AgentMode mode() {
        return AgentMode.PLAIN;
    }

    default List<String> tools() {
        return List.of();
    }

    Flux<AgentDelta> stream(AgentRequest request);
}
