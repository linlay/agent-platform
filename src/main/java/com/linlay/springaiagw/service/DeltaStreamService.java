package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.AgwDelta;
import org.springframework.stereotype.Service;
import reactor.core.publisher.Flux;

import java.time.Duration;

@Service
public class DeltaStreamService {

    public Flux<AgwDelta> toThinkingDeltas(String fullThinking) {
        return splitToChars(fullThinking).map(AgwDelta::thinking);
    }

    public Flux<AgwDelta> toContentDeltas(String fullContent) {
        return splitToChars(fullContent).map(AgwDelta::content);
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
