package com.linlay.springaiagw.agent;

import com.aiagent.agw.sdk.model.AgwDelta;
import com.linlay.springaiagw.model.AgentRequest;
import reactor.core.publisher.Flux;

import java.util.List;

public interface Agent {

    String id();

    default String description() {
        return id();
    }

    String providerKey();

    String model();

    String systemPrompt();

    default AgentMode mode() {
        return AgentMode.PLAIN;
    }

    default List<String> tools() {
        return List.of();
    }

    Flux<AgwDelta> stream(AgentRequest request);
}
