package com.linlay.springaiagw.config;

import io.netty.handler.logging.LogLevel;
import com.linlay.springaiagw.service.LlmLogSanitizer;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatusCode;
import org.springframework.http.client.ClientHttpResponse;
import org.springframework.http.client.reactive.ReactorClientHttpConnector;
import org.springframework.util.StreamUtils;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.client.RestClient;
import org.springframework.web.reactive.function.client.ClientRequest;
import org.springframework.web.reactive.function.client.ClientResponse;
import org.springframework.web.reactive.function.client.ExchangeStrategies;
import org.springframework.web.reactive.function.client.WebClient;
import reactor.netty.http.client.HttpClient;
import reactor.netty.resources.ConnectionProvider;
import reactor.netty.transport.logging.AdvancedByteBufFormat;

import java.io.ByteArrayInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.time.Duration;
import java.util.function.Consumer;

@Configuration
public class SpringAiConfiguration {

    private static final Logger log = LoggerFactory.getLogger(SpringAiConfiguration.class);
    private static final String LLM_WIRETAP_LOGGER = "com.linlay.springaiagw.llm.wiretap";

    @Bean
    public RestClient.Builder loggingRestClientBuilder(LlmInteractionLogProperties logProperties) {
        RestClient.Builder builder = RestClient.builder();
        if (!logProperties.isEnabled()) {
            return builder;
        }

        return builder
                .requestInterceptor((request, body, execution) -> {
                    boolean maskSensitive = logProperties.isMaskSensitive();
                    log.info("[llm-http][request] {} {}", request.getMethod(), request.getURI());
                    log.info("[llm-http][request-headers] {}", LlmLogSanitizer.maskHeaders(request.getHeaders(), maskSensitive));
                    log.info("[llm-http][request-body]\n{}", LlmLogSanitizer.maskText(new String(body, StandardCharsets.UTF_8), maskSensitive));

                    ClientHttpResponse response = execution.execute(request, body);
                    byte[] responseBody = StreamUtils.copyToByteArray(response.getBody());

                    log.info("[llm-http][response] status={}", response.getStatusCode().value());
                    log.info("[llm-http][response-headers] {}", LlmLogSanitizer.maskHeaders(response.getHeaders(), maskSensitive));
                    log.info("[llm-http][response-body]\n{}",
                            LlmLogSanitizer.maskText(new String(responseBody, StandardCharsets.UTF_8), maskSensitive));

                    return new ReplayableClientHttpResponse(response, responseBody);
                });
    }

    @Bean
    public ConnectionProvider llmConnectionProvider() {
        return ConnectionProvider.builder("llm-pool")
                .maxIdleTime(Duration.ofSeconds(30))
                .maxLifeTime(Duration.ofMinutes(5))
                .evictInBackground(Duration.ofSeconds(30))
                .build();
    }

    @Bean
    public WebClient.Builder loggingWebClientBuilder(
            LlmInteractionLogProperties logProperties,
            ConnectionProvider llmConnectionProvider) {
        HttpClient httpClient = HttpClient.create(llmConnectionProvider);
        if (logProperties.isEnabled() && !logProperties.isMaskSensitive()) {
            httpClient = httpClient.wiretap(LLM_WIRETAP_LOGGER, LogLevel.DEBUG, AdvancedByteBufFormat.TEXTUAL);
        }

        WebClient.Builder builder = WebClient.builder()
                .clientConnector(new ReactorClientHttpConnector(httpClient))
                .exchangeStrategies(ExchangeStrategies.builder()
                        .codecs(configurer -> configurer.defaultCodecs().maxInMemorySize(16 * 1024 * 1024))
                        .build());
        if (!logProperties.isEnabled()) {
            return builder;
        }

        boolean maskSensitive = logProperties.isMaskSensitive();
        return builder.filter((request, next) -> {
            log.info("[llm-webclient][request] {} {}", request.method(), request.url());
            log.info("[llm-webclient][request-headers] {}", LlmLogSanitizer.maskHeaders(request.headers(), maskSensitive));
            return next.exchange(request)
                    .doOnNext(logResponse(maskSensitive, request));
        });
    }

    private Consumer<ClientResponse> logResponse(boolean maskSensitive, ClientRequest request) {
        return response -> {
            log.info(
                    "[llm-webclient][response] {} {} status={}",
                    request.method(),
                    request.url(),
                    response.statusCode().value()
            );
            log.info("[llm-webclient][response-headers] {}", LlmLogSanitizer.maskHeaders(response.headers().asHttpHeaders(), maskSensitive));
        };
    }

    private static final class ReplayableClientHttpResponse implements ClientHttpResponse {

        private final ClientHttpResponse delegate;
        private final byte[] body;

        private ReplayableClientHttpResponse(ClientHttpResponse delegate, byte[] body) {
            this.delegate = delegate;
            this.body = body;
        }

        @Override
        public HttpStatusCode getStatusCode() throws IOException {
            return delegate.getStatusCode();
        }

        @Override
        public String getStatusText() throws IOException {
            return delegate.getStatusText();
        }

        @Override
        public HttpHeaders getHeaders() {
            return delegate.getHeaders();
        }

        @Override
        public InputStream getBody() {
            return new ByteArrayInputStream(body);
        }

        @Override
        public void close() {
            delegate.close();
        }
    }
}
