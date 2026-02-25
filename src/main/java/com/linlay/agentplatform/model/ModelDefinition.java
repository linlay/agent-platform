package com.linlay.agentplatform.model;

import java.util.List;

public record ModelDefinition(
        String key,
        String provider,
        ModelProtocol protocol,
        String modelId,
        boolean isReasoner,
        boolean isFunction,
        Integer maxTokens,
        Integer maxInputTokens,
        Integer maxOutputTokens,
        Pricing pricing,
        String sourceFile
) {

    public record Pricing(
            Integer promptPointsPer1k,
            Integer completionPointsPer1k,
            Integer perCallPoints,
            Double priceRatio,
            List<Tier> tiers
    ) {
        public Pricing {
            if (tiers == null) {
                tiers = List.of();
            } else {
                tiers = List.copyOf(tiers);
            }
        }
    }

    public record Tier(
            Integer minInputTokens,
            Integer maxInputTokens,
            Integer promptPointsPer1k,
            Integer completionPointsPer1k,
            Integer perCallPoints,
            Double priceRatio
    ) {
    }
}
