package com.linlay.agentplatform.service;

import com.linlay.agentplatform.stream.model.LlmDelta;
import com.linlay.agentplatform.agent.runtime.policy.ComputePolicy;
import com.linlay.agentplatform.agent.runtime.policy.ToolChoice;
import org.springframework.ai.chat.messages.Message;
import reactor.core.publisher.Flux;

import java.util.List;

class AnthropicSseClient {

    AnthropicSseClient() {
    }

    Flux<LlmDelta> streamDeltasRawSse(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            List<LlmService.LlmFunctionTool> tools,
            boolean parallelToolCalls,
            ToolChoice toolChoice,
            String jsonSchema,
            ComputePolicy computePolicy,
            boolean reasoningEnabled,
            Integer maxTokens,
            String traceId,
            String stage
    ) {
        return Flux.error(notImplemented(providerKey));
    }

    Flux<String> streamContentRawSse(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            String stage
    ) {
        return Flux.error(notImplemented(providerKey));
    }

    private RuntimeException notImplemented(String providerKey) {
        return new UnsupportedOperationException(
                "Anthropic protocol is not implemented yet for provider: " + providerKey
        );
    }
}
