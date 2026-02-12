package com.linlay.springaiagw.service;

import com.linlay.springaiagw.model.agw.AgentDelta;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Flux;

import java.time.Duration;

@Service
public class DeltaStreamService {

    public Flux<AgentDelta> toThinkingDeltas(String fullThinking) {
        return splitToChars(fullThinking).map(AgentDelta::thinking);
    }

    public Flux<AgentDelta> toContentDeltas(String fullContent) {
        return splitToChars(fullContent).map(AgentDelta::content);
    }

    private Flux<String> splitToChars(String text) {
        if (text == null || text.isEmpty()) {
            return Flux.empty();
        }
        return Flux.range(0, text.length())
                .map(i -> String.valueOf(text.charAt(i)))
                .delayElements(Duration.ofMillis(8));
    }
}
