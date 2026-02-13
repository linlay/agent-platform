package com.linlay.springaiagw.agent.runtime;

import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.OutputShape;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.service.LlmCallSpec;
import com.linlay.springaiagw.service.LlmService;
import org.springframework.ai.chat.messages.Message;
import reactor.core.publisher.Flux;

import java.util.List;

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
            String stage
    ) {
        if (!requiresSecondPass(policy)) {
            return Flux.empty();
        }
        return llmService.streamContent(
                        new LlmCallSpec(
                                providerKey,
                                model,
                                systemPrompt,
                                messages == null ? List.of() : messages,
                                buildVerifyPrompt(finalText),
                                List.of(),
                                ToolChoice.NONE,
                                OutputShape.TEXT_ONLY,
                                null,
                                ComputePolicy.MEDIUM,
                                4096,
                                stage,
                                false
                        )
                )
                .filter(chunk -> chunk != null && !chunk.isEmpty());
    }

    private String buildVerifyPrompt(String finalText) {
        return """
                请仔细审查以下答案：
                1. 答案是否自洽、逻辑是否连贯
                2. 是否遗漏了用户问题中的关键约束或要求
                3. 事实性陈述是否有明显错误
                4. 如发现问题，请输出修复后的完整最终答案
                5. 如答案无误，请原样输出

                待审查答案：
                %s
                """.formatted(finalText == null ? "" : finalText);
    }
}
