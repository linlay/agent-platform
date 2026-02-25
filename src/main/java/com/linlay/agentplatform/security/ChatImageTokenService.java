package com.linlay.agentplatform.security;

import com.linlay.agentplatform.config.ChatImageTokenProperties;
import com.nimbusds.jose.JOSEException;
import com.nimbusds.jose.JWSAlgorithm;
import com.nimbusds.jose.JWSHeader;
import com.nimbusds.jose.crypto.MACSigner;
import com.nimbusds.jose.crypto.MACVerifier;
import com.nimbusds.jwt.JWTClaimsSet;
import com.nimbusds.jwt.SignedJWT;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;

import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.time.Instant;
import java.util.ArrayList;
import java.util.List;
import java.util.Locale;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicBoolean;

@Service
public class ChatImageTokenService {

    public static final String DATA_READ_SCOPE = "ap_data:read";
    public static final String ERROR_CODE_INVALID = "CHAT_IMAGE_TOKEN_INVALID";
    public static final String ERROR_CODE_EXPIRED = "CHAT_IMAGE_TOKEN_EXPIRED";

    private static final Logger log = LoggerFactory.getLogger(ChatImageTokenService.class);

    private final ChatImageTokenProperties properties;
    private final AtomicBoolean missingSecretWarned = new AtomicBoolean(false);

    public ChatImageTokenService(ChatImageTokenProperties properties) {
        this.properties = properties;
    }

    public String issueToken(String uid, String chatId) {
        if (!StringUtils.hasText(uid) || !StringUtils.hasText(chatId)) {
            return null;
        }
        if (!StringUtils.hasText(properties.getSecret())) {
            logMissingSecretOnce();
            return null;
        }
        String normalizedUid = uid.trim();
        String normalizedChatId = normalizeUuid(chatId);
        if (!StringUtils.hasText(normalizedChatId)) {
            return null;
        }

        Instant now = Instant.now();
        long ttlSeconds = Math.max(60L, properties.getTtlSeconds());
        long expiresAt = now.plusSeconds(ttlSeconds).getEpochSecond();

        JWTClaimsSet claimsSet = new JWTClaimsSet.Builder()
                .claim("e", expiresAt)
                .claim("c", normalizedChatId)
                .claim("u", normalizedUid)
                .build();

        try {
            SignedJWT jwt = new SignedJWT(new JWSHeader.Builder(JWSAlgorithm.HS256).build(), claimsSet);
            jwt.sign(new MACSigner(deriveSigningKey(properties.getSecret())));
            return jwt.serialize();
        } catch (Exception ex) {
            log.error("failed to issue chat image token chatId={}", chatId, ex);
            return null;
        }
    }

    public VerifyResult verify(String token) {
        if (!StringUtils.hasText(token)) {
            return VerifyResult.invalid(ERROR_CODE_INVALID, "chat image token missing");
        }

        SignedJWT jwt;
        try {
            jwt = SignedJWT.parse(token.trim());
        } catch (Exception ex) {
            log.debug("chat image token parse failed token={}", maskToken(token));
            return VerifyResult.invalid(ERROR_CODE_INVALID, "chat image token invalid");
        }

        JWTClaimsSet claimsSet;
        try {
            claimsSet = jwt.getJWTClaimsSet();
        } catch (Exception ex) {
            log.debug("chat image token claims parse failed token={}", maskToken(token));
            return VerifyResult.invalid(ERROR_CODE_INVALID, "chat image token invalid");
        }

        if (!verifySignature(jwt, resolveSigningSecrets())) {
            return VerifyResult.invalid(ERROR_CODE_INVALID, "chat image token invalid");
        }

        Long expiresEpochSeconds = longClaim(claimsSet, "e");
        if (expiresEpochSeconds == null) {
            return VerifyResult.invalid(ERROR_CODE_INVALID, "chat image token invalid");
        }
        Instant expiresAt = Instant.ofEpochSecond(expiresEpochSeconds);
        if (expiresAt.isBefore(Instant.now())) {
            return VerifyResult.invalid(ERROR_CODE_EXPIRED, "chat image token expired");
        }

        String uid = stringClaim(claimsSet, "u");
        String chatId = normalizeUuid(stringClaim(claimsSet, "c"));

        if (!StringUtils.hasText(uid) || !StringUtils.hasText(chatId)) {
            return VerifyResult.invalid(ERROR_CODE_INVALID, "chat image token invalid");
        }

        return VerifyResult.valid(new Claims(
                uid.trim(),
                chatId.trim(),
                DATA_READ_SCOPE,
                null,
                expiresAt,
                null
        ));
    }

    private boolean verifySignature(SignedJWT jwt, List<String> secrets) {
        if (secrets.isEmpty()) {
            logMissingSecretOnce();
            return false;
        }
        for (String secret : secrets) {
            try {
                if (jwt.verify(new MACVerifier(deriveSigningKey(secret)))) {
                    return true;
                }
            } catch (JOSEException ex) {
                log.debug("chat image token signature verification failed token={}", maskToken(jwt.serialize()));
            } catch (Exception ex) {
                log.debug("chat image token signature verification failed token={}", maskToken(jwt.serialize()));
            }
        }
        return false;
    }

    private List<String> resolveSigningSecrets() {
        List<String> secrets = new ArrayList<>();
        if (StringUtils.hasText(properties.getSecret())) {
            secrets.add(properties.getSecret().trim());
        }
        if (StringUtils.hasText(properties.getPreviousSecrets())) {
            for (String item : properties.getPreviousSecrets().split(",")) {
                if (StringUtils.hasText(item)) {
                    secrets.add(item.trim());
                }
            }
        }
        return secrets;
    }

    private byte[] deriveSigningKey(String secret) throws Exception {
        MessageDigest digest = MessageDigest.getInstance("SHA-256");
        return digest.digest(secret.getBytes(StandardCharsets.UTF_8));
    }

    private String stringClaim(JWTClaimsSet claimsSet, String key) {
        Object value = claimsSet.getClaim(key);
        return value == null ? null : String.valueOf(value);
    }

    private Long longClaim(JWTClaimsSet claimsSet, String key) {
        Object value = claimsSet.getClaim(key);
        if (value instanceof Number number) {
            return number.longValue();
        }
        if (value instanceof String text && StringUtils.hasText(text)) {
            try {
                return Long.parseLong(text.trim());
            } catch (NumberFormatException ignored) {
                return null;
            }
        }
        return null;
    }

    private String normalizeUuid(String raw) {
        if (!StringUtils.hasText(raw)) {
            return null;
        }
        try {
            return UUID.fromString(raw.trim()).toString();
        } catch (IllegalArgumentException ex) {
            return null;
        }
    }

    private String maskToken(String token) {
        if (!StringUtils.hasText(token)) {
            return "<empty>";
        }
        String normalized = token.trim();
        if (normalized.length() <= 12) {
            return "***";
        }
        return normalized.substring(0, 6) + "..." + normalized.substring(normalized.length() - 4);
    }

    private void logMissingSecretOnce() {
        if (missingSecretWarned.compareAndSet(false, true)) {
            log.warn("chat image token secret is missing, token issue/verify is disabled");
        }
    }

    public record Claims(
            String uid,
            String chatId,
            String scope,
            Instant issuedAt,
            Instant expiresAt,
            String jti
    ) {
    }

    public record VerifyResult(
            boolean valid,
            Claims claims,
            String errorCode,
            String message
    ) {
        public static VerifyResult valid(Claims claims) {
            return new VerifyResult(true, claims, null, null);
        }

        public static VerifyResult invalid(String errorCode, String message) {
            return new VerifyResult(false, null, errorCode, message);
        }

        public boolean hasScope(String expectedScope) {
            if (!valid || claims == null || !StringUtils.hasText(expectedScope)) {
                return false;
            }
            return expectedScope.toLowerCase(Locale.ROOT).equals(String.valueOf(claims.scope).toLowerCase(Locale.ROOT));
        }
    }
}
