package com.linlay.agentplatform.stream.adapter.openai;

import com.linlay.agentplatform.stream.model.StreamInput;
import com.linlay.agentplatform.stream.model.LlmDelta;
import org.junit.jupiter.api.Test;

import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;

class OpenAiDeltaToStreamInputMapperCompatibilityTest {

    @Test
    void shouldEmitReasoningAndContentWithStableIds() {
        OpenAiDeltaToStreamInputMapper mapper = new OpenAiDeltaToStreamInputMapper();

        List<StreamInput> first = mapper.mapOrEmpty(new LlmDelta("r1", "c1", null, null));
        List<StreamInput> second = mapper.mapOrEmpty(new LlmDelta("r2", "c2", null, null));

        assertThat(first).hasSize(2);
        assertThat(second).hasSize(2);

        StreamInput.ReasoningDelta firstReasoning = (StreamInput.ReasoningDelta) first.get(0);
        StreamInput.ContentDelta firstContent = (StreamInput.ContentDelta) first.get(1);
        StreamInput.ReasoningDelta secondReasoning = (StreamInput.ReasoningDelta) second.get(0);
        StreamInput.ContentDelta secondContent = (StreamInput.ContentDelta) second.get(1);

        assertThat(firstReasoning.reasoningId()).isEqualTo("reasoning_1");
        assertThat(secondReasoning.reasoningId()).isEqualTo("reasoning_1");
        assertThat(firstContent.contentId()).isEqualTo("content_2");
        assertThat(secondContent.contentId()).isEqualTo("content_2");
    }

    @Test
    void shouldStillEmitRunComplete() {
        OpenAiDeltaToStreamInputMapper mapper = new OpenAiDeltaToStreamInputMapper();

        List<StreamInput> inputs = mapper.mapOrEmpty(new LlmDelta("done", null, "stop"));

        assertThat(inputs).hasSize(2);
        assertThat(inputs.get(0)).isInstanceOf(StreamInput.ContentDelta.class);
        assertThat(inputs.get(1)).isInstanceOf(StreamInput.RunComplete.class);
        assertThat(((StreamInput.RunComplete) inputs.get(1)).finishReason()).isEqualTo("stop");
    }
}
