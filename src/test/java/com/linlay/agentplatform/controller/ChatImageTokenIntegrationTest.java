package com.linlay.agentplatform.controller;

import com.fasterxml.jackson.core.type.TypeReference;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.config.DataCatalogProperties;
import com.linlay.agentplatform.memory.ChatWindowMemoryProperties;
import com.linlay.agentplatform.service.ChatRecordStore;
import com.linlay.agentplatform.service.LlmService;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.crypto.MACSigner;
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
import org.springframework.http.MediaType;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.reactive.server.FluxExchangeResult;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.util.UriComponentsBuilder;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.nio.file.StandardOpenOption;
import java.security.MessageDigest;
import java.time.Duration;
import java.time.Instant;
import java.util.Date;
import java.util.LinkedHashMap;
import java.util.List;
import java.util.Map;
import java.util.UUID;

import static org.assertj.core.api.Assertions.assertThat;

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
                "agent.chat-image-token.ttl-seconds=86400",
                "memory.chat.dir=${java.io.tmpdir}/springai-agent-platform-chat-image-token-chats-${random.uuid}",
                "memory.chat.index.sqlite-file=${java.io.tmpdir}/springai-agent-platform-chat-image-token-chats-db-${random.uuid}/chats.db",
                "agent.viewport.external-dir=${java.io.tmpdir}/springai-agent-platform-chat-image-token-viewports-${random.uuid}",
                "agent.capability.tools-external-dir=${java.io.tmpdir}/springai-agent-platform-chat-image-token-tools-${random.uuid}",
                "agent.skill.external-dir=${java.io.tmpdir}/springai-agent-platform-chat-image-token-skills-${random.uuid}",
                "agent.data.external-dir=${java.io.tmpdir}/springai-agent-platform-chat-image-token-data-${random.uuid}"
        }
)
@AutoConfigureWebTestClient
@Import(ChatImageTokenIntegrationTest.TestLlmServiceConfig.class)
class ChatImageTokenIntegrationTest {

    private static final String CHAT_IMAGE_SECRET = "chat-image-token-secret-for-tests";
    private static final String ISSUER = "https://auth.example.local";

    private static RSAKey rsaKey;
    private static Path jwksFile;

    @Autowired
    private WebTestClient webTestClient;
    @Autowired
    private ChatWindowMemoryProperties chatWindowMemoryProperties;
    @Autowired
    private DataCatalogProperties dataCatalogProperties;
    @Autowired
    private ChatRecordStore chatRecordStore;

    private final ObjectMapper objectMapper = new ObjectMapper();

    @TestConfiguration
    static class TestLlmServiceConfig {
        @Bean
        @Primary
        LlmService llmService() {
            return new LlmService(null, null) {
                @Override
                public Flux<String> streamContent(String providerKey, String model, String systemPrompt, String userPrompt) {
                    return Flux.just("hello", " ", "world");
                }

                @Override
                public Flux<String> streamContent(String providerKey, String model, String systemPrompt, String userPrompt, String stage) {
                    return streamContent(providerKey, model, systemPrompt, userPrompt);
                }

                @Override
                public Mono<String> completeText(String providerKey, String model, String systemPrompt, String userPrompt) {
                    return Mono.just("{\"thinking\":\"ok\",\"plan\":[],\"toolCalls\":[]}");
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
        rsaKey = new RSAKeyGenerator(2048).keyID("chat-image-token-kid").generate();
        jwksFile = Files.createTempFile("chat-image-token-jwks-", ".json");
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
    void setUp() throws IOException {
        Files.createDirectories(Path.of(chatWindowMemoryProperties.getDir()));
        Files.createDirectories(Path.of(dataCatalogProperties.getExternalDir()));
    }

    @Test
    void chatApiShouldReturnChatImageTokenWhenAuthorized() throws Exception {
        String chatId = UUID.randomUUID().toString();
        seedChatWithImageContent(chatId, "![sample](sample_photo.jpg)");

        String authToken = issueAuthToken("user-chat-api");

        byte[] responseBody = webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path("/api/ap/chat").queryParam("chatId", chatId).build())
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + authToken)
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .returnResult()
                .getResponseBody();

        assertThat(responseBody).isNotNull();
        Map<String, Object> root = objectMapper.readValue(responseBody, new TypeReference<>() {
        });
        Map<String, Object> data = objectMapper.convertValue(root.get("data"), new TypeReference<>() {
        });
        assertThat(String.valueOf(data.get("chatImageToken"))).isNotBlank();
    }

    @Test
    void querySseChatStartShouldContainChatImageToken() throws Exception {
        String authToken = issueAuthToken("user-sse");
        FluxExchangeResult<String> result = webTestClient.post()
                .uri("/api/ap/query")
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + authToken)
                .contentType(MediaType.APPLICATION_JSON)
                .bodyValue(Map.of(
                        "agentKey", "demoModePlain",
                        "message", "hello"
                ))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().contentTypeCompatibleWith(MediaType.TEXT_EVENT_STREAM)
                .returnResult(String.class);

        List<String> chunks = result.getResponseBody()
                .take(1200)
                .collectList()
                .block(Duration.ofSeconds(8));
        assertThat(chunks).isNotNull();
        String joined = String.join("", chunks);

        assertThat(joined).contains("\"type\":\"chat.start\"");
        assertThat(joined).contains("\"chatImageToken\":\"");
    }

    @Test
    void dataApiShouldServeImageWithValidChatImageTokenWithoutAuthorizationHeader() throws Exception {
        String chatId = UUID.randomUUID().toString();
        String userId = "user-image-ok";
        seedChatWithImageContent(chatId, "![sample](sample_photo.jpg)");
        writeImageFile("sample_photo.jpg");

        String authToken = issueAuthToken(userId);
        String chatImageToken = fetchChatImageToken(chatId, authToken);

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "sample_photo.jpg")
                        .queryParam("t", chatImageToken)
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CACHE_CONTROL, "private, no-store")
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "image/jpeg");
    }

    @Test
    void dataApiShouldReturn403ForInvalidToken() throws Exception {
        writeImageFile("sample_photo.jpg");

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "sample_photo.jpg")
                        .queryParam("t", "invalid-token")
                        .build())
                .exchange()
                .expectStatus().isForbidden()
                .expectHeader().valueEquals(HttpHeaders.CACHE_CONTROL, "private, no-store")
                .expectBody()
                .jsonPath("$.data.errorCode").isEqualTo("CHAT_IMAGE_TOKEN_INVALID");
    }

    @Test
    void dataApiShouldReturn403ForExpiredToken() throws Exception {
        String chatId = UUID.randomUUID().toString();
        seedChatWithImageContent(chatId, "![sample](sample_photo.jpg)");
        writeImageFile("sample_photo.jpg");

        String expiredToken = issueChatImageToken(
                "user-expired",
                chatId,
                Instant.now().minusSeconds(60)
        );

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "sample_photo.jpg")
                        .queryParam("t", expiredToken)
                        .build())
                .exchange()
                .expectStatus().isForbidden()
                .expectHeader().valueEquals(HttpHeaders.CACHE_CONTROL, "private, no-store")
                .expectBody()
                .jsonPath("$.data.errorCode").isEqualTo("CHAT_IMAGE_TOKEN_EXPIRED");
    }

    @Test
    void dataApiShouldReturn403ForFileOutsideChatAssets() throws Exception {
        String chatId = UUID.randomUUID().toString();
        String userId = "user-asset-scope";
        seedChatWithImageContent(chatId, "![sample](sample_photo.jpg)");
        writeImageFile("sample_photo.jpg");
        writeImageFile("other_photo.jpg");

        String authToken = issueAuthToken(userId);
        String chatImageToken = fetchChatImageToken(chatId, authToken);

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "other_photo.jpg")
                        .queryParam("t", chatImageToken)
                        .build())
                .exchange()
                .expectStatus().isForbidden()
                .expectHeader().valueEquals(HttpHeaders.CACHE_CONTROL, "private, no-store")
                .expectBody()
                .jsonPath("$.data.errorCode").isEqualTo("CHAT_IMAGE_TOKEN_INVALID");
    }

    @Test
    void dataApiShouldReturn404WhenFileNotFoundWithValidToken() throws Exception {
        String chatId = UUID.randomUUID().toString();
        String userId = "user-404";
        seedChatWithImageContent(chatId, "![sample](missing.jpg)");

        String authToken = issueAuthToken(userId);
        String chatImageToken = fetchChatImageToken(chatId, authToken);

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "missing.jpg")
                        .queryParam("t", chatImageToken)
                        .build())
                .exchange()
                .expectStatus().isNotFound()
                .expectHeader().valueEquals(HttpHeaders.CACHE_CONTROL, "private, no-store");
    }

    @Test
    void dataApiShouldKeepAuthorizationCompatibilityWithoutTokenQueryParam() throws Exception {
        writeImageFile("sample_photo.jpg");
        String authToken = issueAuthToken("user-auth-compat");

        webTestClient.get()
                .uri(UriComponentsBuilder.fromPath("/api/ap/data")
                        .queryParam("file", "sample_photo.jpg")
                        .build()
                        .toUriString())
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + authToken)
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CACHE_CONTROL, "private, no-store")
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "image/jpeg");
    }

    private void seedChatWithImageContent(String chatId, String assistantContent) throws Exception {
        Path chatDir = Path.of(chatWindowMemoryProperties.getDir());
        Files.createDirectories(chatDir);
        long now = System.currentTimeMillis();
        String runId = UUID.randomUUID().toString();
        chatRecordStore.ensureChat(chatId, "demoModePlain", "示例-单次直答", "show image");

        Map<String, Object> queryLine = new LinkedHashMap<>();
        queryLine.put("_type", "query");
        queryLine.put("chatId", chatId);
        queryLine.put("runId", runId);
        queryLine.put("updatedAt", now);
        queryLine.put("query", Map.of(
                "requestId", runId,
                "chatId", chatId,
                "role", "user",
                "message", "show image",
                "stream", true
        ));
        writeJsonLine(chatDir.resolve(chatId + ".json"), queryLine);

        Map<String, Object> stepLine = new LinkedHashMap<>();
        stepLine.put("_type", "step");
        stepLine.put("chatId", chatId);
        stepLine.put("runId", runId);
        stepLine.put("_stage", "oneshot");
        stepLine.put("_seq", 1);
        stepLine.put("updatedAt", now);
        stepLine.put("messages", List.of(
                Map.of(
                        "role", "user",
                        "content", List.of(Map.of("type", "text", "text", "show image")),
                        "ts", now
                ),
                Map.of(
                        "role", "assistant",
                        "content", List.of(Map.of("type", "text", "text", assistantContent)),
                        "ts", now + 1
                )
        ));
        writeJsonLine(chatDir.resolve(chatId + ".json"), stepLine);
    }

    private String fetchChatImageToken(String chatId, String authToken) throws Exception {
        byte[] responseBody = webTestClient.get()
                .uri(uriBuilder -> uriBuilder.path("/api/ap/chat").queryParam("chatId", chatId).build())
                .header(HttpHeaders.AUTHORIZATION, "Bearer " + authToken)
                .exchange()
                .expectStatus().isOk()
                .expectBody()
                .returnResult()
                .getResponseBody();

        assertThat(responseBody).isNotNull();
        Map<String, Object> root = objectMapper.readValue(responseBody, new TypeReference<>() {
        });
        Map<String, Object> data = objectMapper.convertValue(root.get("data"), new TypeReference<>() {
        });
        String token = String.valueOf(data.get("chatImageToken"));
        assertThat(token).isNotBlank();
        return token;
    }

    private String issueAuthToken(String subject) throws JOSEException {
        Instant now = Instant.now();
        JWTClaimsSet claimsSet = new JWTClaimsSet.Builder()
                .issuer(ISSUER)
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

    private String issueChatImageToken(
            String uid,
            String chatId,
            Instant expiresAt
    ) throws Exception {
        JWTClaimsSet claimsSet = new JWTClaimsSet.Builder()
                .claim("e", expiresAt.getEpochSecond())
                .claim("c", chatId)
                .claim("u", uid)
                .build();

        SignedJWT jwt = new SignedJWT(new JWSHeader.Builder(JWSAlgorithm.HS256).build(), claimsSet);
        jwt.sign(new MACSigner(sha256(CHAT_IMAGE_SECRET)));
        return jwt.serialize();
    }

    private byte[] sha256(String raw) throws Exception {
        MessageDigest digest = MessageDigest.getInstance("SHA-256");
        return digest.digest(raw.getBytes(StandardCharsets.UTF_8));
    }

    private void writeImageFile(String filename) throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        Files.createDirectories(dataDir);
        Files.write(dataDir.resolve(filename), createMinimalPng());
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

    private void writeJsonLine(Path path, Object value) throws Exception {
        Files.createDirectories(path.getParent());
        String line = objectMapper.writeValueAsString(value) + System.lineSeparator();
        Files.writeString(path, line, StandardOpenOption.CREATE, StandardOpenOption.APPEND);
    }
}
