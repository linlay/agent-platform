package com.linlay.agentplatform.security;

import java.nio.charset.StandardCharsets;
import java.time.Instant;
import java.util.Base64;
import java.util.Date;

import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.JWSSigner;
import com.nimbusds.jose.crypto.RSASSASigner;
import com.nimbusds.jose.jwk.RSAKey;
import com.nimbusds.jose.jwk.gen.RSAKeyGenerator;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
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
        "memory.chat.dir=${java.io.tmpdir}/springai-agent-platform-auth-local-offline-test-chats-${random.uuid}",
        "memory.chat.index.sqlite-file=${java.io.tmpdir}/springai-agent-platform-auth-local-offline-test-chats-db-${random.uuid}/chats.db",
        "agent.viewport.external-dir=${java.io.tmpdir}/springai-agent-platform-auth-local-offline-test-viewports-${random.uuid}",
        "agent.capability.tools-external-dir=${java.io.tmpdir}/springai-agent-platform-auth-local-offline-test-tools-${random.uuid}",
        "agent.skill.external-dir=${java.io.tmpdir}/springai-agent-platform-auth-local-offline-test-skills-${random.uuid}"
    }
)
@AutoConfigureWebTestClient
class ApiJwtAuthWebFilterLocalKeyOfflineTests {

    private static final RSAKey LOCAL_RSA_KEY = generateRsaKey("local-offline-kid");
    private static final String LOCAL_PUBLIC_KEY_PEM = toPem(LOCAL_RSA_KEY);

    @Autowired
    private WebTestClient webTestClient;

    @DynamicPropertySource
    static void dynamicProperties(DynamicPropertyRegistry registry) {
        registry.add("agent.auth.local-public-key", () -> LOCAL_PUBLIC_KEY_PEM);
        registry.add("agent.auth.jwks-uri", () -> "http://127.0.0.1:9/api/auth/jwks");
        registry.add("agent.auth.jwks-cache-seconds", () -> 60);
    }

    @Test
    void shouldAllowApiRequestWithLocalPublicKeyWhenJwksIsUnavailable() throws Exception {
        String token = issueToken(LOCAL_RSA_KEY, "app-user", "device-local");

        webTestClient.get()
            .uri("/api/ap/agents")
            .header("Authorization", "Bearer " + token)
            .exchange()
            .expectStatus().isOk()
            .expectBody()
            .jsonPath("$.code").isEqualTo(0)
            .jsonPath("$.msg").isEqualTo("success");
    }

    private static String issueToken(RSAKey rsaKey, String subject, String deviceId) throws JOSEException {
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

    private static RSAKey generateRsaKey(String kid) {
        try {
            return new RSAKeyGenerator(2048).keyID(kid).generate();
        } catch (JOSEException ex) {
            throw new IllegalStateException("Failed to generate RSA key for test", ex);
        }
    }

    private static String toPem(RSAKey rsaKey) {
        try {
            String base64 = Base64.getMimeEncoder(64, "\n".getBytes(StandardCharsets.US_ASCII))
                .encodeToString(rsaKey.toRSAPublicKey().getEncoded());
            return "-----BEGIN PUBLIC KEY-----\n" + base64 + "\n-----END PUBLIC KEY-----";
        } catch (JOSEException ex) {
            throw new IllegalStateException("Failed to encode RSA public key as PEM", ex);
        }
    }
}
