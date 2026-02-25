package com.linlay.agentplatform.service;

import com.aiagent.agw.sdk.model.LlmDelta;
import com.linlay.agentplatform.agent.runtime.policy.ComputePolicy;
import com.linlay.agentplatform.agent.runtime.policy.ToolChoice;
import com.linlay.agentplatform.config.AgentProviderProperties;
import org.springframework.ai.chat.messages.Message;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

import java.util.List;

class NewApiOpenAiCompatibleSseClient {

    private final AgentProviderProperties providerProperties;
    private final OpenAiCompatibleSseClient delegate;

    NewApiOpenAiCompatibleSseClient(
            AgentProviderProperties providerProperties,
            OpenAiCompatibleSseClient delegate
    ) {
        this.providerProperties = providerProperties;
        this.delegate = delegate;
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
        return delegate.streamDeltasRawSse(
                providerKey,
                model,
                systemPrompt,
                historyMessages,
                userPrompt,
                tools,
                parallelToolCalls,
                toolChoice,
                jsonSchema,
                computePolicy,
                reasoningEnabled,
                maxTokens,
                traceId,
                stage,
                resolveEndpointPath(providerKey)
        );
    }

    Flux<String> streamContentRawSse(
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> historyMessages,
            String userPrompt,
            String stage
    ) {
        return delegate.streamContentRawSse(
                providerKey,
                model,
                systemPrompt,
                historyMessages,
                userPrompt,
                stage,
                resolveEndpointPath(providerKey)
        );
    }

    private String resolveEndpointPath(String providerKey) {
        if (providerProperties == null) {
            throw new IllegalStateException("Provider properties not configured");
        }
        AgentProviderProperties.ProviderConfig config = providerProperties.getProvider(providerKey);
        if (config == null) {
            throw new IllegalStateException("No provider config found for key: " + providerKey);
        }
        if (!StringUtils.hasText(config.getNewApiPath())) {
            throw new IllegalStateException("Missing new-api-path for provider key: " + providerKey);
        }
        return config.getNewApiPath().trim();
    }
}
