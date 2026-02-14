package com.linlay.springaiagw.agent;

import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.model.AgentRequest;
import reactor.core.publisher.Flux;

import java.util.List;

public interface Agent {

    String id();

    default String name() {
        return id();
    }

    default String icon() {
        return null;
    }

    default String description() {
        return id();
    }

    String providerKey();

    String model();

    String systemPrompt();

    default AgentRuntimeMode mode() {
        return AgentRuntimeMode.ONESHOT;
    }

    default List<String> tools() {
        return List.of();
    }

    Flux<AgentDelta> stream(AgentRequest request);
}
