package com.linlay.agentplatform.model;

import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.agent.AgentCatalogProperties;
import com.linlay.agentplatform.config.AgentProviderProperties;
import com.linlay.agentplatform.config.CapabilityCatalogProperties;
import com.linlay.agentplatform.config.ViewportCatalogProperties;
import com.linlay.agentplatform.service.RuntimeResourceSyncService;
import com.linlay.agentplatform.skill.SkillCatalogProperties;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.io.TempDir;

import java.nio.file.Files;
import java.nio.file.Path;
import java.util.LinkedHashMap;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class ModelRegistryServiceTest {

    @TempDir
    Path tempDir;

    @Test
    void shouldLoadValidModelWithPricingTiers() throws Exception {
        Path modelsDir = tempDir.resolve("models");
        Files.createDirectories(modelsDir);
        Files.writeString(modelsDir.resolve("bailian-qwen3-max.json"), """
                {
                  "key": "bailian-qwen3-max",
                  "provider": "bailian",
                  "protocol": "OPENAI",
                  "modelId": "qwen3-max",
                  "isReasoner": true,
                  "isFunction": true,
                  "pricing": {
                    "promptPointsPer1k": 10,
                    "completionPointsPer1k": 30,
                    "perCallPoints": 1,
                    "priceRatio": 1.2,
                    "tiers": [
                      {
                        "minInputTokens": 0,
                        "maxInputTokens": 8000,
                        "promptPointsPer1k": 8,
                        "completionPointsPer1k": 24,
                        "perCallPoints": 1,
                        "priceRatio": 1.0
                      }
                    ]
                  }
                }
                """);

        ModelRegistryService service = new ModelRegistryService(
                new ObjectMapper(),
                modelProperties(modelsDir),
                providerProperties(Map.of("bailian", provider())),
                runtimeSync(modelsDir)
        );

        ModelDefinition model = service.find("bailian-qwen3-max").orElseThrow();
        assertThat(model.provider()).isEqualTo("bailian");
        assertThat(model.protocol()).isEqualTo(ModelProtocol.OPENAI);
        assertThat(model.modelId()).isEqualTo("qwen3-max");
        assertThat(model.pricing().promptPointsPer1k()).isEqualTo(10);
        assertThat(model.pricing().completionPointsPer1k()).isEqualTo(30);
        assertThat(model.pricing().tiers()).hasSize(1);
        assertThat(model.pricing().tiers().getFirst().maxInputTokens()).isEqualTo(8000);
    }

    @Test
    void shouldRejectModelWhenProviderIsMissing() throws Exception {
        Path modelsDir = tempDir.resolve("models");
        Files.createDirectories(modelsDir);
        Files.writeString(modelsDir.resolve("unknown-provider.json"), """
                {
                  "key": "unknown-provider",
                  "provider": "missing",
                  "protocol": "OPENAI",
                  "modelId": "qwen3-max"
                }
                """);

        ModelRegistryService service = new ModelRegistryService(
                new ObjectMapper(),
                modelProperties(modelsDir),
                providerProperties(Map.of("bailian", provider())),
                runtimeSync(modelsDir)
        );

        assertThat(service.find("unknown-provider")).isEmpty();
    }

    @Test
    void shouldKeepFirstOnDuplicateModelKey() throws Exception {
        Path modelsDir = tempDir.resolve("models");
        Files.createDirectories(modelsDir);
        Files.writeString(modelsDir.resolve("a.json"), """
                {
                  "key": "dup-key",
                  "provider": "bailian",
                  "protocol": "OPENAI",
                  "modelId": "first-model"
                }
                """);
        Files.writeString(modelsDir.resolve("b.json"), """
                {
                  "key": "dup-key",
                  "provider": "bailian",
                  "protocol": "OPENAI",
                  "modelId": "second-model"
                }
                """);

        ModelRegistryService service = new ModelRegistryService(
                new ObjectMapper(),
                modelProperties(modelsDir),
                providerProperties(Map.of("bailian", provider())),
                runtimeSync(modelsDir)
        );

        assertThat(service.find("dup-key").orElseThrow().modelId()).isEqualTo("first-model");
    }

    @Test
    void shouldRejectInvalidProtocol() throws Exception {
        Path modelsDir = tempDir.resolve("models");
        Files.createDirectories(modelsDir);
        Files.writeString(modelsDir.resolve("bad-protocol.json"), """
                {
                  "key": "bad-protocol",
                  "provider": "bailian",
                  "protocol": "FOO_PROTOCOL",
                  "modelId": "qwen3-max"
                }
                """);

        ModelRegistryService service = new ModelRegistryService(
                new ObjectMapper(),
                modelProperties(modelsDir),
                providerProperties(Map.of("bailian", provider())),
                runtimeSync(modelsDir)
        );

        assertThat(service.find("bad-protocol")).isEmpty();
    }

    private ModelCatalogProperties modelProperties(Path modelsDir) {
        ModelCatalogProperties properties = new ModelCatalogProperties();
        properties.setExternalDir(modelsDir.toString());
        return properties;
    }

    private AgentProviderProperties providerProperties(Map<String, AgentProviderProperties.ProviderConfig> providers) {
        AgentProviderProperties properties = new AgentProviderProperties();
        properties.setProviders(new LinkedHashMap<>(providers));
        return properties;
    }

    private AgentProviderProperties.ProviderConfig provider() {
        AgentProviderProperties.ProviderConfig config = new AgentProviderProperties.ProviderConfig();
        config.setBaseUrl("https://example.com");
        config.setApiKey("dummy");
        config.setModel("dummy-model");
        return config;
    }

    private RuntimeResourceSyncService runtimeSync(Path modelsDir) {
        AgentCatalogProperties agentProperties = new AgentCatalogProperties();
        agentProperties.setExternalDir(tempDir.resolve("agents").toString());
        ViewportCatalogProperties viewportProperties = new ViewportCatalogProperties();
        viewportProperties.setExternalDir(tempDir.resolve("viewports").toString());
        CapabilityCatalogProperties capabilityProperties = new CapabilityCatalogProperties();
        capabilityProperties.setToolsExternalDir(tempDir.resolve("tools").toString());
        SkillCatalogProperties skillProperties = new SkillCatalogProperties();
        skillProperties.setExternalDir(tempDir.resolve("skills").toString());
        ModelCatalogProperties modelProperties = new ModelCatalogProperties();
        modelProperties.setExternalDir(modelsDir.toString());
        return new RuntimeResourceSyncService(
                agentProperties,
                viewportProperties,
                capabilityProperties,
                skillProperties,
                modelProperties
        );
    }
}
