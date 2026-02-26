package com.linlay.agentplatform.stream.adapter.openai;

import com.linlay.agentplatform.stream.model.LlmDelta;
import com.linlay.agentplatform.stream.model.ToolCallDelta;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class OpenAiSseDeltaParserCompatibilityTest {

    private final OpenAiSseDeltaParser parser = new OpenAiSseDeltaParser(new ObjectMapper());

    @Test
    void shouldParseReasoningContentToolCallsFinishReasonAndUsage() {
        String chunk = """
                data: {"usage":{"prompt_tokens":11,"completion_tokens":7},"choices":[{"delta":{"reasoning_content":"思考","content":"答案","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\\"command\\":\\"ls\\"}"}}]},"finish_reason":"stop"}]}
                """;

        LlmDelta delta = parser.parseOrNull(chunk);

        assertThat(delta).isNotNull();
        assertThat(delta.reasoning()).isEqualTo("思考");
        assertThat(delta.content()).isEqualTo("答案");
        assertThat(delta.finishReason()).isEqualTo("stop");
        assertThat(delta.usage()).containsEntry("prompt_tokens", 11L).containsEntry("completion_tokens", 7L);
        assertThat(delta.toolCalls()).hasSize(1);

        ToolCallDelta toolCall = delta.toolCalls().getFirst();
        assertThat(toolCall.id()).isEqualTo("call_1");
        assertThat(toolCall.index()).isEqualTo(0);
        assertThat(toolCall.name()).isEqualTo("bash");
        assertThat(toolCall.arguments()).isEqualTo("{\"command\":\"ls\"}");
    }

    @Test
    void shouldReturnDeltaWhenOnlyUsageExistsWithoutChoices() {
        String chunk = """
                data: {"usage":{"prompt_tokens":3,"completion_tokens":2,"details":{"cached":1},"sources":["a","b"]}}
                """;

        LlmDelta delta = parser.parseOrNull(chunk);

        assertThat(delta).isNotNull();
        assertThat(delta.reasoning()).isNull();
        assertThat(delta.content()).isNull();
        assertThat(delta.toolCalls()).isNull();
        assertThat(delta.finishReason()).isNull();
        assertThat(delta.usage()).containsEntry("prompt_tokens", 3L);
        assertThat(delta.usage().get("details")).isEqualTo(Map.of("cached", 1L));
        assertThat(delta.usage().get("sources")).isEqualTo(List.of("a", "b"));
    }
}
