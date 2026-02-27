package com.linlay.agentplatform.security;

import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Instant;
import java.util.Date;

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
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.reactive.AutoConfigureWebTestClient;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.test.context.DynamicPropertyRegistry;
import org.springframework.test.context.DynamicPropertySource;
import org.springframework.test.web.reactive.server.WebTestClient;

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
        "memory.chat.dir=${java.io.tmpdir}/springai-agent-platform-auth-test-chats-${random.uuid}",
        "memory.chat.index.sqlite-file=${java.io.tmpdir}/springai-agent-platform-auth-test-chats-db-${random.uuid}/chats.db",
        "agent.viewport.external-dir=${java.io.tmpdir}/springai-agent-platform-auth-test-viewports-${random.uuid}",
        "agent.capability.tools-external-dir=${java.io.tmpdir}/springai-agent-platform-auth-test-tools-${random.uuid}",
        "agent.skill.external-dir=${java.io.tmpdir}/springai-agent-platform-auth-test-skills-${random.uuid}"
    }
)
@AutoConfigureWebTestClient
class ApiJwtAuthWebFilterTests {

    @Autowired
    private WebTestClient webTestClient;

    private static RSAKey rsaKey;
    private static Path jwksFile;

    @BeforeAll
    static void beforeAll() throws Exception {
        rsaKey = new RSAKeyGenerator(2048).keyID("test-kid").generate();
        jwksFile = Files.createTempFile("agent-platform-jwks-", ".json");
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

    @Test
    void shouldRejectApiRequestWithoutToken() {
        webTestClient.get()
            .uri("/api/ap/agents")
            .exchange()
            .expectStatus().isUnauthorized();
    }

    @Test
    void shouldAllowApiRequestWithValidToken() throws Exception {
        String token = issueToken("app-user", "device-1");

        webTestClient.get()
            .uri("/api/ap/agents")
            .header("Authorization", "Bearer " + token)
            .exchange()
            .expectStatus().isOk()
            .expectBody()
            .jsonPath("$.code").isEqualTo(0)
            .jsonPath("$.msg").isEqualTo("success");
    }

    private String issueToken(String subject, String deviceId) throws JOSEException {
        Instant now = Instant.now();
        JWTClaimsSet claimsSet = new JWTClaimsSet.Builder()
            .issuer("https://auth.example.local")
            .subject(subject)
            .issueTime(Date.from(now))
            .expirationTime(Date.from(now.plusSeconds(300)))
            .claim("scope", "app")
            .claim("device_id", deviceId)
            .build();

        SignedJWT jwt = new SignedJWT(
            new JWSHeader.Builder(JWSAlgorithm.RS256).keyID(rsaKey.getKeyID()).build(),
            claimsSet
        );

        JWSSigner signer = new RSASSASigner(rsaKey.toPrivateKey());
        jwt.sign(signer);
        return jwt.serialize();
    }
}
