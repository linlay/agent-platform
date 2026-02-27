package com.linlay.agentplatform.controller;

import com.linlay.agentplatform.config.DataCatalogProperties;
import com.linlay.agentplatform.service.LlmService;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.crypto.RSASSASigner;
import com.nimbusds.jose.jwk.JWKSet;
import com.nimbusds.jose.jwk.RSAKey;
import com.nimbusds.jose.jwk.gen.RSAKeyGenerator;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import org.junit.jupiter.api.AfterAll;
import org.junit.jupiter.api.BeforeAll;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.reactive.AutoConfigureWebTestClient;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Import;
import org.springframework.context.annotation.Primary;
import org.springframework.http.HttpHeaders;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.reactive.server.WebTestClient;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.Date;

@SpringBootTest(
        webEnvironment = SpringBootTest.WebEnvironment.MOCK,
        properties = {
                "agent.providers.bailian.base-url=https://example.com/v1",
                "agent.providers.bailian.api-key=test-bailian-key",
                "agent.providers.bailian.model=test-bailian-model",
                "agent.providers.siliconflow.base-url=https://example.com/v1",
                "agent.providers.siliconflow.api-key=test-siliconflow-key",
                "agent.providers.siliconflow.model=test-siliconflow-model",
                "agent.auth.enabled=true",
                "agent.auth.issuer=https://auth.example.local",
                "agent.chat-image-token.secret=chat-image-token-secret-for-tests",
                "agent.chat-image-token.data-token-validation-enabled=false",
                "memory.chat.dir=${java.io.tmpdir}/springai-agent-platform-data-token-toggle-chats-${random.uuid}",
                "memory.chat.index.sqlite-file=${java.io.tmpdir}/springai-agent-platform-data-token-toggle-chats-db-${random.uuid}/chats.db",
                "agent.viewport.external-dir=${java.io.tmpdir}/springai-agent-platform-data-token-toggle-viewports-${random.uuid}",
                "agent.capability.tools-external-dir=${java.io.tmpdir}/springai-agent-platform-data-token-toggle-tools-${random.uuid}",
                "agent.skill.external-dir=${java.io.tmpdir}/springai-agent-platform-data-token-toggle-skills-${random.uuid}",
                "agent.data.external-dir=${java.io.tmpdir}/springai-agent-platform-data-token-toggle-data-${random.uuid}"
        }
)
@AutoConfigureWebTestClient
@Import(DataApiTokenValidationToggleIntegrationTest.TestLlmServiceConfig.class)
class DataApiTokenValidationToggleIntegrationTest {

    private static RSAKey rsaKey;
    private static Path jwksFile;

    @Autowired
    private WebTestClient webTestClient;
    @Autowired
    private DataCatalogProperties dataCatalogProperties;

    @TestConfiguration
    static class TestLlmServiceConfig {
        @Bean
        @Primary
        LlmService llmService() {
            return new LlmService(null, null) {
                @Override
                public Flux<String> streamContent(String providerKey, String model, String systemPrompt, String userPrompt) {
                    return Flux.just("test");
                }

                @Override
                public Flux<String> streamContent(String providerKey, String model, String systemPrompt, String userPrompt, String stage) {
                    return streamContent(providerKey, model, systemPrompt, userPrompt);
                }

                @Override
                public Mono<String> completeText(String providerKey, String model, String systemPrompt, String userPrompt) {
                    return Mono.just("{}");
                }

                @Override
                public Mono<String> completeText(String providerKey, String model, String systemPrompt, String userPrompt, String stage) {
                    return completeText(providerKey, model, systemPrompt, userPrompt);
                }
            };
        }
    }

    @BeforeAll
    static void beforeAll() throws Exception {
        rsaKey = new RSAKeyGenerator(2048).keyID("data-token-toggle-kid").generate();
        jwksFile = Files.createTempFile("data-token-toggle-jwks-", ".json");
        String jwks = new JWKSet(rsaKey.toPublicJWK()).toJSONObject().toString();
        Files.writeString(jwksFile, jwks, StandardCharsets.UTF_8);
    }

    @AfterAll
    static void afterAll() throws Exception {
        if (jwksFile != null) {
            Files.deleteIfExists(jwksFile);
        }
    }

    @DynamicPropertySource
    static void dynamicProperties(DynamicPropertyRegistry registry) {
        registry.add("agent.auth.jwks-uri", () -> jwksFile.toUri().toString());
        registry.add("agent.auth.jwks-cache-seconds", () -> 60);
    }

    @BeforeEach
    void setUp() throws Exception {
        Files.createDirectories(Path.of(dataCatalogProperties.getExternalDir()));
    }

    @Test
    void dataApiShouldNotBypassAuthByTokenQueryWhenValidationDisabled() {
        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "sample_photo.jpg")
                        .queryParam("t", "invalid-token")
                        .build())
                .exchange()
                .expectStatus().isUnauthorized();
    }

    @Test
    void dataApiShouldIgnoreTokenQueryValidationWhenDisabled() throws Exception {
        Files.write(Path.of(dataCatalogProperties.getExternalDir()).resolve("sample_photo.jpg"), createMinimalPng());
        String authToken = issueAuthToken("user-data-token-toggle");

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "sample_photo.jpg")
                        .queryParam("t", "invalid-token")
                        .build())
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + authToken)
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "image/jpeg")
                .expectHeader().valueEquals(HttpHeaders.CACHE_CONTROL, "private, no-store");
    }

    private String issueAuthToken(String subject) throws JOSEException {
        Instant now = Instant.now();
        JWTClaimsSet claimsSet = new JWTClaimsSet.Builder()
                .issuer("https://auth.example.local")
                .subject(subject)
                .issueTime(Date.from(now))
                .expirationTime(Date.from(now.plusSeconds(300)))
                .claim("scope", "app")
                .claim("device_id", "test-device")
                .build();

        SignedJWT jwt = new SignedJWT(
                new JWSHeader.Builder(JWSAlgorithm.RS256).keyID(rsaKey.getKeyID()).build(),
                claimsSet
        );

        JWSSigner signer = new RSASSASigner(rsaKey.toPrivateKey());
        jwt.sign(signer);
        return jwt.serialize();
    }

    private byte[] createMinimalPng() {
        return new byte[]{
                (byte) 0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A,
                0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52,
                0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
                0x08, 0x02, 0x00, 0x00, 0x00, (byte) 0x90, 0x77, 0x53,
                (byte) 0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41,
                0x54, 0x08, (byte) 0xD7, 0x63, (byte) 0xF8, (byte) 0xCF, (byte) 0xC0, 0x00,
                0x00, 0x00, 0x02, 0x00, 0x01, (byte) 0xE2, 0x21, (byte) 0xBC,
                0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E,
                0x44, (byte) 0xAE, 0x42, 0x60, (byte) 0x82
        };
    }
}
