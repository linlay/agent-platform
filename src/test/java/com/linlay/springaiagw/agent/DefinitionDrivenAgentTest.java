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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("看看当前目录有哪些文件", null, null, null, null, null, null))
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

    @Test
    void reactFlowShouldStreamStepThinkingBeforeToolCalls() {
        AgentDefinition definition = new AgentDefinition(
                "demoReAct",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.RE_ACT,
                List.of("bash")
        );

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-react-step-1".equals(stage)) {
                    return Flux.just(
                                    "{\"thinking\":\"需要先",
                                    "运行 df 命令查看磁盘使用情况\",\"action\":{\"name\":\"bash\",\"arguments\":{\"command\":\"df\"}},\"done\":false}"
                            )
                            .delayElements(Duration.ofMillis(5));
                }
                if ("agent-react-step-2".equals(stage)) {
                    return Flux.just(
                                    "{\"thinking\":\"已获取磁盘使用情况，",
                                    "还需运行 free 命令查看内存使用情况。\",\"action\":{\"name\":\"bash\",\"arguments\":{\"command\":\"free\"}},\"done\":false}"
                            )
                            .delayElements(Duration.ofMillis(5));
                }
                if ("agent-react-step-3".equals(stage)) {
                    return Flux.just("{\"thinking\":\"信息已齐备，可以给出结论。\",\"action\":null,\"done\":true}")
                            .delayElements(Duration.ofMillis(5));
                }
                if (stage != null && stage.startsWith("agent-react-final-step-")) {
                    return Flux.just("结论：资源情况已获取。");
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return streamContent(providerType, model, systemPrompt, userPrompt, "default");
            }

            @Override
            public Mono<String> completeText(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("使用最简单的df和free看看服务器的资源情况", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas).isNotEmpty();

        int step1ThinkingIndex = indexOfThinkingContaining(deltas, "需要先");
        int step2ThinkingIndex = indexOfThinkingContaining(deltas, "已获取磁盘使用情况");
        int firstToolCallIndex = indexOfToolCall(deltas, 1);
        int secondToolCallIndex = indexOfToolCall(deltas, 2);

        assertThat(step1ThinkingIndex).isGreaterThanOrEqualTo(0);
        assertThat(firstToolCallIndex).isGreaterThan(step1ThinkingIndex);
        assertThat(step2ThinkingIndex).isGreaterThan(firstToolCallIndex);
        assertThat(secondToolCallIndex).isGreaterThan(step2ThinkingIndex);
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("结论：资源情况已获取。");
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
        return indexOfToolCall(deltas, 1);
    }

    private int indexOfToolCall(List<AgentDelta> deltas, int occurrence) {
        int count = 0;
        for (int i = 0; i < deltas.size(); i++) {
            if (deltas.get(i).toolCalls() != null && !deltas.get(i).toolCalls().isEmpty()) {
                count++;
                if (count == occurrence) {
                    return i;
                }
            }
        }
        return -1;
    }
}
