package com.linlay.agentplatform.stream.model;

import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class LlmDeltaCompatibilityTest {

    @Test
    void shouldSupportThreeArgConstructor() {
        LlmDelta delta = new LlmDelta("content", List.of(), "stop");

        assertThat(delta.reasoning()).isNull();
        assertThat(delta.content()).isEqualTo("content");
        assertThat(delta.toolCalls()).isEmpty();
        assertThat(delta.finishReason()).isEqualTo("stop");
        assertThat(delta.usage()).isNull();
    }

    @Test
    void shouldSupportFourArgConstructor() {
        LlmDelta delta = new LlmDelta("reasoning", "content", null, "stop");

        assertThat(delta.reasoning()).isEqualTo("reasoning");
        assertThat(delta.content()).isEqualTo("content");
        assertThat(delta.toolCalls()).isNull();
        assertThat(delta.finishReason()).isEqualTo("stop");
        assertThat(delta.usage()).isNull();
    }

    @Test
    void shouldSupportFiveArgConstructor() {
        Map<String, Object> usage = Map.of("prompt_tokens", 12L, "completion_tokens", 34L);
        LlmDelta delta = new LlmDelta("reasoning", "content", null, "stop", usage);

        assertThat(delta.reasoning()).isEqualTo("reasoning");
        assertThat(delta.content()).isEqualTo("content");
        assertThat(delta.finishReason()).isEqualTo("stop");
        assertThat(delta.usage()).isEqualTo(usage);
    }
}
