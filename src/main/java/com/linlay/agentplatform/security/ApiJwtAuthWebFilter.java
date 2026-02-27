package com.linlay.agentplatform.security;

import java.nio.charset.StandardCharsets;

import com.linlay.agentplatform.config.AppAuthProperties;
import com.linlay.agentplatform.config.ChatImageTokenProperties;
import com.linlay.agentplatform.security.JwksJwtVerifier.JwtPrincipal;
import org.springframework.http.HttpMethod;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.stereotype.Component;
import org.springframework.util.StringUtils;
import org.springframework.web.server.ServerWebExchange;
import org.springframework.web.server.WebFilter;
import org.springframework.web.server.WebFilterChain;
import reactor.core.publisher.Mono;

@Component
public class ApiJwtAuthWebFilter implements WebFilter {

    public static final String JWT_PRINCIPAL_ATTR = "APP_JWT_PRINCIPAL";

    private static final String AUTH_PREFIX = "Bearer ";

    private final AppAuthProperties authProperties;
    private final ChatImageTokenProperties chatImageTokenProperties;
    private final JwksJwtVerifier jwtVerifier;

    public ApiJwtAuthWebFilter(
            AppAuthProperties authProperties,
            ChatImageTokenProperties chatImageTokenProperties,
            JwksJwtVerifier jwtVerifier
    ) {
        this.authProperties = authProperties;
        this.chatImageTokenProperties = chatImageTokenProperties;
        this.jwtVerifier = jwtVerifier;
    }

    @Override
    public Mono<Void> filter(ServerWebExchange exchange, WebFilterChain chain) {
        if (!authProperties.isEnabled()) {
            return chain.filter(exchange);
        }

        String path = exchange.getRequest().getPath().value();
        if (!StringUtils.hasText(path) || !path.startsWith("/api/ap/")) {
            return chain.filter(exchange);
        }

        if (HttpMethod.OPTIONS.equals(exchange.getRequest().getMethod())) {
            return chain.filter(exchange);
        }
        if (isDataApiTokenRequest(exchange)) {
            return chain.filter(exchange);
        }

        String token = resolveBearerToken(exchange);
        JwtPrincipal principal = jwtVerifier.verify(token).orElse(null);
        if (principal == null) {
            return writeUnauthorized(exchange);
        }

        exchange.getAttributes().put(JWT_PRINCIPAL_ATTR, principal);
        return chain.filter(exchange);
    }

    private String resolveBearerToken(ServerWebExchange exchange) {
        String authorization = exchange.getRequest().getHeaders().getFirst("Authorization");
        if (!StringUtils.hasText(authorization)) {
            return null;
        }
        if (!authorization.startsWith(AUTH_PREFIX)) {
            return null;
        }
        String token = authorization.substring(AUTH_PREFIX.length()).trim();
        return StringUtils.hasText(token) ? token : null;
    }

    private boolean isDataApiTokenRequest(ServerWebExchange exchange) {
        if (!chatImageTokenProperties.isDataTokenValidationEnabled()) {
            return false;
        }
        if (!HttpMethod.GET.equals(exchange.getRequest().getMethod())) {
            return false;
        }
        String path = exchange.getRequest().getPath().value();
        if (!"/api/ap/data".equals(path)) {
            return false;
        }
        return exchange.getRequest().getQueryParams().containsKey("t");
    }

    private Mono<Void> writeUnauthorized(ServerWebExchange exchange) {
        byte[] body = "{\"error\":\"unauthorized\"}".getBytes(StandardCharsets.UTF_8);
        exchange.getResponse().setStatusCode(HttpStatus.UNAUTHORIZED);
        exchange.getResponse().getHeaders().setContentType(MediaType.APPLICATION_JSON);
        return exchange.getResponse().writeWith(Mono.just(exchange.getResponse().bufferFactory().wrap(body)));
    }
}
