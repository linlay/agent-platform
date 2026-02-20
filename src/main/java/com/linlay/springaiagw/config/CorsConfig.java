package com.linlay.springaiagw.config;

import org.springframework.beans.factory.annotation.Value;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Configuration;
import org.springframework.web.cors.CorsConfiguration;
import org.springframework.web.cors.reactive.CorsWebFilter;
import org.springframework.web.cors.reactive.UrlBasedCorsConfigurationSource;

import java.util.ArrayList;
import java.util.List;

@Configuration
public class CorsConfig {

    @Bean
    public CorsWebFilter corsWebFilter(
            @Value("${agw.cors.path-pattern:/api/**}") String pathPattern,
            @Value("${agw.cors.allowed-origin-patterns:http://localhost:*}") List<String> allowedOriginPatterns,
            @Value("${agw.cors.allowed-methods:GET,POST,PUT,PATCH,DELETE,OPTIONS}") List<String> allowedMethods,
            @Value("${agw.cors.allowed-headers:*}") List<String> allowedHeaders,
            @Value("${agw.cors.exposed-headers:Content-Type}") List<String> exposedHeaders,
            @Value("${agw.cors.allow-credentials:false}") boolean allowCredentials,
            @Value("${agw.cors.max-age-seconds:3600}") long maxAgeSeconds
    ) {
        CorsConfiguration configuration = new CorsConfiguration();
        configuration.setAllowedOriginPatterns(compact(allowedOriginPatterns));
        configuration.setAllowedMethods(compact(allowedMethods));
        configuration.setAllowedHeaders(compact(allowedHeaders));

        List<String> exposed = compact(exposedHeaders);
        if (!exposed.isEmpty()) {
            configuration.setExposedHeaders(exposed);
        }

        configuration.setAllowCredentials(allowCredentials);
        configuration.setMaxAge(maxAgeSeconds);

        UrlBasedCorsConfigurationSource source = new UrlBasedCorsConfigurationSource();
        source.registerCorsConfiguration(pathPattern, configuration);
        return new CorsWebFilter(source);
    }

    private List<String> compact(List<String> input) {
        List<String> output = new ArrayList<>();
        if (input == null) {
            return output;
        }
        for (String item : input) {
            if (item == null) {
                continue;
            }
            String trimmed = item.trim();
            if (!trimmed.isEmpty()) {
                output.add(trimmed);
            }
        }
        return output;
    }
}
