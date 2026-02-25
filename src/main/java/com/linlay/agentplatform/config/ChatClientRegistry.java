package com.linlay.agentplatform.config;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.ai.chat.client.ChatClient;
import org.springframework.ai.chat.model.ChatModel;
import org.springframework.ai.openai.OpenAiChatModel;
import org.springframework.ai.openai.OpenAiChatOptions;
import org.springframework.ai.openai.api.OpenAiApi;
import org.springframework.stereotype.Component;
import org.springframework.util.StringUtils;
import org.springframework.web.client.RestClient;
import org.springframework.web.reactive.function.client.WebClient;

import java.util.LinkedHashMap;
import java.util.Map;

@Component
public class ChatClientRegistry {

    private static final Logger log = LoggerFactory.getLogger(ChatClientRegistry.class);
    private static final String SAFE_DEFAULT_MODEL = "qwen3-max";

    private final Map<String, ChatClient> clients = new LinkedHashMap<>();
    private final AgentProviderProperties properties;

    public ChatClientRegistry(
            AgentProviderProperties properties,
            RestClient.Builder loggingRestClientBuilder,
            WebClient.Builder loggingWebClientBuilder
    ) {
        this.properties = properties;
        for (var entry : properties.getProviders().entrySet()) {
            String key = entry.getKey();
            AgentProviderProperties.ProviderConfig config = entry.getValue();
            if (!StringUtils.hasText(key) || config == null) {
                log.warn("Skip invalid provider config entry: key='{}'", key);
                continue;
            }
            try {
                ChatModel model = buildChatModel(key, config,
                        loggingRestClientBuilder, loggingWebClientBuilder);
                clients.put(key, ChatClient.create(model));
                log.info("Registered ChatClient for provider '{}'", key);
            } catch (Exception ex) {
                log.warn("Failed to create ChatClient for provider '{}': {}",
                        key, ex.getMessage());
            }
        }
    }

    public ChatClient getClient(String providerKey) {
        return clients.get(providerKey);
    }

    public AgentProviderProperties.ProviderConfig getConfig(String providerKey) {
        return properties.getProvider(providerKey);
    }

    public String defaultModel(String providerKey) {
        AgentProviderProperties.ProviderConfig config = properties.getProvider(providerKey);
        if (config == null || !StringUtils.hasText(config.getModel())) {
            return SAFE_DEFAULT_MODEL;
        }
        return config.getModel();
    }

    private ChatModel buildChatModel(
            String providerName,
            AgentProviderProperties.ProviderConfig config,
            RestClient.Builder restClientBuilder,
            WebClient.Builder webClientBuilder
    ) {
        assertOpenAiCompatibleConfig(providerName, config);

        OpenAiApi api = OpenAiApi.builder()
                .baseUrl(config.getBaseUrl())
                .apiKey(config.getApiKey())
                .restClientBuilder(restClientBuilder)
                .webClientBuilder(webClientBuilder)
                .build();

        OpenAiChatOptions options = OpenAiChatOptions.builder()
                .model(StringUtils.hasText(config.getModel()) ? config.getModel() : SAFE_DEFAULT_MODEL)
                .temperature(0.2)
                .build();

        return OpenAiChatModel.builder()
                .openAiApi(api)
                .defaultOptions(options)
                .build();
    }

    private void assertOpenAiCompatibleConfig(String providerName, AgentProviderProperties.ProviderConfig config) {
        if (!StringUtils.hasText(config.getBaseUrl())) {
            throw new IllegalStateException("Missing base-url for provider '" + providerName + "'");
        }
        if (!StringUtils.hasText(config.getApiKey())) {
            throw new IllegalStateException("Missing api-key for provider '" + providerName + "'");
        }
    }
}
