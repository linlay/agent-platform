package com.linlay.springaiagw.agent.runtime;

import com.linlay.springaiagw.agent.RuntimePromptTemplates;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.OutputShape;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.service.LlmCallSpec;
import com.linlay.springaiagw.service.LlmService;
import org.springframework.ai.chat.messages.Message;
import reactor.core.publisher.Flux;

import java.util.List;
import java.util.Map;

public class VerifyService {

    private final LlmService llmService;

    public VerifyService(LlmService llmService) {
        this.llmService = llmService;
    }

    public boolean requiresSecondPass(VerifyPolicy policy) {
        return policy == VerifyPolicy.SECOND_PASS_FIX;
    }

    public Flux<String> streamSecondPass(
            VerifyPolicy policy,
            String providerKey,
            String model,
            String systemPrompt,
            List<Message> messages,
            String finalText,
            String stage,
            RuntimePromptTemplates runtimePrompts
    ) {
        if (!requiresSecondPass(policy)) {
            return Flux.empty();
        }
        RuntimePromptTemplates templates = runtimePrompts == null ? RuntimePromptTemplates.defaults() : runtimePrompts;
        String verifySystemPrompt = appendPrompts(systemPrompt, templates.verify().systemPrompt());
        String verifyUserPrompt = templates.render(
                templates.verify().userPromptTemplate(),
                Map.of("candidate_final_text", finalText == null ? "" : finalText)
        );
        return llmService.streamContent(
                        new LlmCallSpec(
                                providerKey,
                                model,
                                verifySystemPrompt,
                                messages == null ? List.of() : messages,
                                verifyUserPrompt,
                                List.of(),
                                ToolChoice.NONE,
                                OutputShape.TEXT_ONLY,
                                null,
                                ComputePolicy.MEDIUM,
                                false,
                                4096,
                                stage,
                                false
                        )
                )
                .filter(chunk -> chunk != null && !chunk.isEmpty());
    }

    private String appendPrompts(String base, String appendix) {
        String normalizedBase = base == null ? "" : base.trim();
        String normalizedAppendix = appendix == null ? "" : appendix.trim();
        if (normalizedBase.isEmpty()) {
            return normalizedAppendix;
        }
        if (normalizedAppendix.isEmpty()) {
            return normalizedBase;
        }
        return normalizedBase + "\n\n" + normalizedAppendix;
    }
}
