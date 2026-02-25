package com.linlay.agentplatform.security;

import com.linlay.agentplatform.config.ChatImageTokenProperties;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.MACSigner;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Instant;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class ChatImageTokenServiceTest {

    private static final String SECRET = "chat-image-token-secret-for-tests";
    private static final String CHAT_ID = "123e4567-e89b-12d3-a456-426614174001";
    private static final String UID = "user-image-ok";

    private ChatImageTokenService service;

    @BeforeEach
    void setUp() {
        ChatImageTokenProperties properties = new ChatImageTokenProperties();
        properties.setSecret(SECRET);
        properties.setTtlSeconds(86_400L);
        service = new ChatImageTokenService(properties);
    }

    @Test
    void issueAndVerifyShouldUseMinimalClaims() throws Exception {
        String token = service.issueToken(UID, CHAT_ID);

        assertThat(token).isNotBlank();
        assertThat(token.length()).isLessThanOrEqualTo(180);

        SignedJWT jwt = SignedJWT.parse(token);
        assertThat(jwt.getHeader().getAlgorithm()).isEqualTo(JWSAlgorithm.HS256);
        assertThat(jwt.getHeader().getType()).isNull();
        assertThat(jwt.getJWTClaimsSet().getClaims()).containsOnlyKeys("e", "c", "u");

        ChatImageTokenService.VerifyResult verifyResult = service.verify(token);
        assertThat(verifyResult.valid()).isTrue();
        assertThat(verifyResult.claims()).isNotNull();
        assertThat(verifyResult.claims().uid()).isEqualTo(UID);
        assertThat(verifyResult.claims().chatId()).isEqualTo(CHAT_ID);
        assertThat(verifyResult.claims().scope()).isEqualTo(ChatImageTokenService.DATA_READ_SCOPE);
        assertThat(verifyResult.claims().expiresAt()).isAfter(Instant.now());
    }

    @Test
    void verifyShouldReturnExpiredWhenEIsPast() throws Exception {
        String token = signToken(Map.of(
                "e", Instant.now().minusSeconds(5).getEpochSecond(),
                "c", CHAT_ID,
                "u", UID
        ));

        ChatImageTokenService.VerifyResult verifyResult = service.verify(token);
        assertThat(verifyResult.valid()).isFalse();
        assertThat(verifyResult.errorCode()).isEqualTo(ChatImageTokenService.ERROR_CODE_EXPIRED);
    }

    @Test
    void verifyShouldRejectWhenRequiredClaimsMissing() throws Exception {
        String missingE = signToken(Map.of("c", CHAT_ID, "u", UID));
        String missingC = signToken(Map.of("e", Instant.now().plusSeconds(60).getEpochSecond(), "u", UID));
        String missingU = signToken(Map.of("e", Instant.now().plusSeconds(60).getEpochSecond(), "c", CHAT_ID));

        assertThat(service.verify(missingE).valid()).isFalse();
        assertThat(service.verify(missingC).valid()).isFalse();
        assertThat(service.verify(missingU).valid()).isFalse();
    }

    @Test
    void verifyShouldRejectTamperedToken() {
        String token = service.issueToken(UID, CHAT_ID);
        assertThat(token).isNotBlank();
        String tampered = token.substring(0, token.length() - 1) + (token.endsWith("a") ? "b" : "a");

        ChatImageTokenService.VerifyResult verifyResult = service.verify(tampered);
        assertThat(verifyResult.valid()).isFalse();
        assertThat(verifyResult.errorCode()).isEqualTo(ChatImageTokenService.ERROR_CODE_INVALID);
    }

    @Test
    void verifyShouldRejectLegacyClaimFormat() throws Exception {
        String legacyToken = signToken(Map.of(
                "uid", UID,
                "chatId", CHAT_ID,
                "scope", ChatImageTokenService.DATA_READ_SCOPE,
                "exp", Instant.now().plusSeconds(60).getEpochSecond()
        ));

        ChatImageTokenService.VerifyResult verifyResult = service.verify(legacyToken);
        assertThat(verifyResult.valid()).isFalse();
        assertThat(verifyResult.errorCode()).isEqualTo(ChatImageTokenService.ERROR_CODE_INVALID);
    }

    private String signToken(Map<String, Object> claims) throws Exception {
        JWTClaimsSet.Builder builder = new JWTClaimsSet.Builder();
        for (Map.Entry<String, Object> entry : claims.entrySet()) {
            builder.claim(entry.getKey(), entry.getValue());
        }

        SignedJWT jwt = new SignedJWT(new JWSHeader.Builder(JWSAlgorithm.HS256).build(), builder.build());
        jwt.sign(new MACSigner(sha256(SECRET)));
        return jwt.serialize();
    }

    private byte[] sha256(String raw) throws Exception {
        MessageDigest digest = MessageDigest.getInstance("SHA-256");
        return digest.digest(raw.getBytes(StandardCharsets.UTF_8));
    }
}
