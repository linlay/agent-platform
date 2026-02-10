package com.linlay.springaiagw.agent;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.model.AgentDelta;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.junit.jupiter.api.Test;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.time.Duration;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class DefinitionDrivenAgentTest {

    private final ObjectMapper objectMapper = new ObjectMapper();

    @Test
    void planExecuteFlowShouldStreamPlannerThinkingBeforeToolCalls() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("bash")
        );

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                if (systemPrompt != null && systemPrompt.contains("任务编排阶段")) {
                    return Flux.just(
                                    "{\"thinking\":\"先",
                                    "查目录\",\"plan\":[\"执行ls\"],\"toolCalls\":[{\"name\":\"bash\",\"arguments\":{\"command\":\"ls\"}}]}"
                            )
                            .delayElements(Duration.ofMillis(5));
                }
                return Flux.just("当前目录包含 Dockerfile、src、pom.xml");
            }

            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                return streamContent(providerType, model, systemPrompt, userPrompt);
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                return completeText(providerType, model, systemPrompt, userPrompt);
            }
        };

        BaseTool bashTool = new BaseTool() {
            @Override
            public String name() {
                return "bash";
            }

            @Override
            public String description() {
                return "test bash";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of(
                        "ok", true,
                        "command", args.getOrDefault("command", "")
                ));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(bashTool)),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("看看当前目录有哪些文件", null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas).isNotEmpty();

        int plannerThinkingIndex = indexOfThinkingContaining(deltas, "查目录");
        int toolCallIndex = indexOfToolCall(deltas);
        assertThat(plannerThinkingIndex).isGreaterThanOrEqualTo(0);
        assertThat(toolCallIndex).isGreaterThan(plannerThinkingIndex);

        assertThat(deltas.stream().map(AgentDelta::thinking).toList())
                .contains("正在生成执行计划...\n");
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    private int indexOfThinkingContaining(List<AgentDelta> deltas, String text) {
        for (int i = 0; i < deltas.size(); i++) {
            String thinking = deltas.get(i).thinking();
            if (thinking != null && thinking.contains(text)) {
                return i;
            }
        }
        return -1;
    }

    private int indexOfToolCall(List<AgentDelta> deltas) {
        for (int i = 0; i < deltas.size(); i++) {
            if (deltas.get(i).toolCalls() != null && !deltas.get(i).toolCalls().isEmpty()) {
                return i;
            }
        }
        return -1;
    }
}
