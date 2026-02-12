package com.linlay.springaiagw.agent;

import com.aiagent.agw.sdk.model.AgwDelta;
import com.aiagent.agw.sdk.model.AgwEvent;
import com.aiagent.agw.sdk.model.LlmDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.aiagent.agw.sdk.service.AgwEventAssembler;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.ProviderType;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.springframework.ai.chat.messages.Message;
import org.junit.jupiter.api.Test;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

import static org.assertj.core.api.Assertions.assertThat;

class DefinitionDrivenAgentTest {

    private final ObjectMapper objectMapper = new ObjectMapper();

    // ======================== RE_ACT Tests ========================

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

        List<AgwDelta> deltas = agent.stream(new AgentRequest("使用最简单的df和free看看服务器的资源情况", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas).isNotEmpty();

        int step1ThinkingIndex = indexOfThinkingContaining(deltas, "需要先");
        int step2ThinkingIndex = indexOfThinkingContaining(deltas, "已获取磁盘使用情况");
        int firstToolCallIndex = indexOfToolCallById(deltas, "call_bash_step_1");
        int secondToolCallIndex = indexOfToolCallById(deltas, "call_bash_step_2");

        assertThat(step1ThinkingIndex).isGreaterThanOrEqualTo(0);
        assertThat(firstToolCallIndex).isGreaterThan(step1ThinkingIndex);
        assertThat(step2ThinkingIndex).isGreaterThan(firstToolCallIndex);
        assertThat(secondToolCallIndex).isGreaterThan(step2ThinkingIndex);
        assertThat(deltas.stream().map(AgwDelta::content).toList()).contains("结论：资源情况已获取。");
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    // ======================== PLAIN Tests ========================

    @Test
    void plainFlowShouldSelectOnlyOneToolFromCandidates() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlain",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAIN,
                List.of("tool_a", "tool_b")
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
                if ("agent-plain-decision".equals(stage)) {
                    return Flux.just("{\"thinking\":\"需要先执行一个工具\",\"toolCall\":{\"name\":\"tool_b\",\"arguments\":{\"keyword\":\"alice\"}}}");
                }
                if ("agent-plain-final-with-tool".equals(stage)) {
                    return Flux.just("结论：已执行 tool_b。");
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return streamContent(providerType, model, systemPrompt, userPrompt, "default");
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
                return Mono.just("");
            }
        };

        AtomicInteger toolAInvokeCount = new AtomicInteger();
        BaseTool toolA = new BaseTool() {
            @Override
            public String name() {
                return "tool_a";
            }

            @Override
            public String description() {
                return "tool a";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                toolAInvokeCount.incrementAndGet();
                return objectMapper.valueToTree(Map.of("tool", "tool_a", "ok", true));
            }
        };

        AtomicInteger toolBInvokeCount = new AtomicInteger();
        BaseTool toolB = new BaseTool() {
            @Override
            public String name() {
                return "tool_b";
            }

            @Override
            public String description() {
                return "tool b";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                toolBInvokeCount.incrementAndGet();
                return objectMapper.valueToTree(Map.of("tool", "tool_b", "ok", true));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(toolA, toolB)),
                objectMapper
        );

        List<AgwDelta> deltas = agent.stream(new AgentRequest("从多个工具里选择一个执行", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(4));

        assertThat(deltas).isNotNull();
        assertThat(toolAInvokeCount.get()).isEqualTo(0);
        assertThat(toolBInvokeCount.get()).isEqualTo(1);
        assertThat(indexOfToolCallById(deltas, "call_tool_b_plain_1")).isGreaterThanOrEqualTo(0);
        assertThat(indexOfToolCallById(deltas, "call_tool_a_plain_1")).isLessThan(0);
        assertThat(indexOfToolResultById(deltas, "call_tool_b_plain_1")).isGreaterThan(indexOfToolCallById(deltas, "call_tool_b_plain_1"));
        assertThat(deltas.stream().map(AgwDelta::content).toList()).contains("结论：已执行 tool_b。");
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    // ======================== PLAN_EXECUTE Loop Tests ========================

    @Test
    void planExecuteLoopShouldExecuteSingleToolThenFinalAnswer() {
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
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_bash_1",
                                            "function",
                                            "bash", "{\"command\":\"ls\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-2".equals(stage)) {
                    return Flux.just(
                            new LlmDelta("当前目录包含 Dockerfile、src、pom.xml", null, null),
                            new LlmDelta(null, null, "stop")
                    );
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Flux.just("最终回答");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Flux.just("最终回答");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
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

        List<AgwDelta> deltas = agent.stream(new AgentRequest("看看当前目录有哪些文件", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(deltas).isNotEmpty();

        assertThat(deltas.stream().map(AgwDelta::thinking).toList())
                .contains("进入 PLAN-EXECUTE 模式，正在逐步决策...\n");
        assertThat(indexOfToolResultById(deltas, "call_bash_1")).isGreaterThanOrEqualTo(0);
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    @Test
    void planExecuteLoopShouldExecuteMultiRoundTools() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("city_datetime", "mock_city_weather")
        );

        AtomicReference<Boolean> step1ParallelToolCalls = new AtomicReference<>();
        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    step1ParallelToolCalls.set(parallelToolCalls);
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_city_datetime",
                                            "function",
                                            "city_datetime", "{\"city\":\"上海\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-2".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_city_weather",
                                            "function",
                                            "mock_city_weather", "{\"city\":\"上海\",\"date\":\"tomorrow\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-3".equals(stage)) {
                    return Flux.just(new LlmDelta(null, null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                if (stage != null && stage.startsWith("agent-plan-execute-final")) {
                    return Flux.just("done");
                }
                return Flux.just("fallback");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return streamContent(providerType, model, systemPrompt, userPrompt, "default");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Mono.just("");
            }
        };

        BaseTool cityDateTimeTool = new BaseTool() {
            @Override
            public String name() {
                return "city_datetime";
            }

            @Override
            public String description() {
                return "city datetime";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of(
                        "tool", "city_datetime",
                        "city", "上海",
                        "date", "2026-02-11"
                ));
            }
        };

        AtomicReference<Map<String, Object>> weatherArgs = new AtomicReference<>();
        BaseTool weatherTool = new BaseTool() {
            @Override
            public String name() {
                return "mock_city_weather";
            }

            @Override
            public String description() {
                return "weather";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                weatherArgs.set(Map.copyOf(args));
                return objectMapper.valueToTree(Map.of("ok", true, "date", String.valueOf(args.get("date"))));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(cityDateTimeTool, weatherTool)),
                objectMapper
        );

        List<AgwDelta> deltas = agent.stream(new AgentRequest("查上海明天天气", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(step1ParallelToolCalls.get()).isTrue();
        assertThat(weatherArgs.get()).isNotNull();
        assertThat(weatherArgs.get().get("date")).isEqualTo("2026-02-12");
        assertThat(indexOfToolResultById(deltas, "call_city_datetime")).isGreaterThanOrEqualTo(0);
        assertThat(indexOfToolResultById(deltas, "call_city_weather"))
                .isGreaterThan(indexOfToolResultById(deltas, "call_city_datetime"));
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    @Test
    void planExecuteLoopShouldSupportParallelToolCallsInSingleRound() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("tool_a", "tool_b")
        );

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_tool_a",
                                            "function",
                                            "tool_a", "{\"text\":\"hello\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_tool_b",
                                            "function",
                                            "tool_b", "{\"ok\":true}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-2".equals(stage)) {
                    return Flux.just(new LlmDelta(null, null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Flux.just("最终结论");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Flux.just("最终结论");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Mono.just("");
            }
        };

        BaseTool toolA = new BaseTool() {
            @Override
            public String name() {
                return "tool_a";
            }

            @Override
            public String description() {
                return "tool a";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "tool", "tool_a"));
            }
        };

        BaseTool toolB = new BaseTool() {
            @Override
            public String name() {
                return "tool_b";
            }

            @Override
            public String description() {
                return "tool b";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "tool", "tool_b"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(toolA, toolB)),
                objectMapper
        );

        List<AgwDelta> deltas = agent.stream(new AgentRequest("同时调用两个工具", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(indexOfToolResultById(deltas, "call_tool_a")).isGreaterThanOrEqualTo(0);
        assertThat(indexOfToolResultById(deltas, "call_tool_b")).isGreaterThanOrEqualTo(0);
        assertThat(indexOfToolResultById(deltas, "call_tool_b"))
                .isGreaterThan(indexOfToolResultById(deltas, "call_tool_a"));
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    @Test
    void planExecuteLoopShouldInvokeToolsStrictlySequentiallyWithoutOverlap() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("tool_a", "tool_b")
        );

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_tool_a",
                                            "function",
                                            "tool_a", "{\"text\":\"a\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_tool_b",
                                            "function",
                                            "tool_b", "{\"text\":\"b\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-2".equals(stage)) {
                    return Flux.just(new LlmDelta(null, null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Flux.just("done");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Flux.just("done");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Mono.just("");
            }
        };

        AtomicInteger inFlight = new AtomicInteger(0);
        AtomicInteger maxInFlight = new AtomicInteger(0);
        List<String> invokeOrder = new CopyOnWriteArrayList<>();

        BaseTool toolA = new BaseTool() {
            @Override
            public String name() {
                return "tool_a";
            }

            @Override
            public String description() {
                return "tool a";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                int current = inFlight.incrementAndGet();
                maxInFlight.updateAndGet(previous -> Math.max(previous, current));
                invokeOrder.add("tool_a:start");
                try {
                    Thread.sleep(100);
                    return objectMapper.valueToTree(Map.of("ok", true, "tool", "tool_a"));
                } catch (InterruptedException ex) {
                    Thread.currentThread().interrupt();
                    return objectMapper.valueToTree(Map.of("ok", false, "tool", "tool_a"));
                } finally {
                    invokeOrder.add("tool_a:end");
                    inFlight.decrementAndGet();
                }
            }
        };

        BaseTool toolB = new BaseTool() {
            @Override
            public String name() {
                return "tool_b";
            }

            @Override
            public String description() {
                return "tool b";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                int current = inFlight.incrementAndGet();
                maxInFlight.updateAndGet(previous -> Math.max(previous, current));
                invokeOrder.add("tool_b:start");
                try {
                    Thread.sleep(100);
                    return objectMapper.valueToTree(Map.of("ok", true, "tool", "tool_b"));
                } catch (InterruptedException ex) {
                    Thread.currentThread().interrupt();
                    return objectMapper.valueToTree(Map.of("ok", false, "tool", "tool_b"));
                } finally {
                    invokeOrder.add("tool_b:end");
                    inFlight.decrementAndGet();
                }
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(toolA, toolB)),
                objectMapper
        );

        List<AgwDelta> deltas = agent.stream(new AgentRequest("顺序执行两个工具", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(maxInFlight.get()).isEqualTo(1);
        assertThat(invokeOrder).containsExactly("tool_a:start", "tool_a:end", "tool_b:start", "tool_b:end");
    }

    @Test
    void planExecuteLoopShouldSplitCompositeBashCommand() {
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
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_bash_1",
                                            "function",
                                            "bash", "{\"command\":\"df -h && free -h\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-2".equals(stage)) {
                    return Flux.just(new LlmDelta(null, null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Flux.just("完成");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Flux.just("完成");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Mono.just("");
            }
        };

        List<String> commands = new CopyOnWriteArrayList<>();
        BaseTool bashTool = new BaseTool() {
            @Override
            public String name() {
                return "bash";
            }

            @Override
            public String description() {
                return "bash";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                commands.add(String.valueOf(args.get("command")));
                return objectMapper.valueToTree(Map.of("ok", true));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(bashTool)),
                objectMapper
        );

        List<AgwDelta> deltas = agent.stream(new AgentRequest("检查系统资源", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(commands).containsExactly("df -h", "free -h");
    }

    @Test
    void planExecuteLoopShouldEmitOrderedToolEventsInAssembler() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("tool_a", "tool_b")
        );

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_tool_a_1",
                                            "function",
                                            "tool_a", "{\"text\":\"abcdefghijklmnopqrstuvwxyz0123456789\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_tool_b_2",
                                            "function",
                                            "tool_b", "{\"ok\":true}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-2".equals(stage)) {
                    return Flux.just(new LlmDelta(null, null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Flux.just("done");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Flux.just("done");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Mono.just("");
            }
        };

        BaseTool toolA = new BaseTool() {
            @Override
            public String name() {
                return "tool_a";
            }

            @Override
            public String description() {
                return "tool a";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "tool", "tool_a"));
            }
        };

        BaseTool toolB = new BaseTool() {
            @Override
            public String name() {
                return "tool_b";
            }

            @Override
            public String description() {
                return "tool b";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "tool", "tool_b"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(toolA, toolB)),
                objectMapper
        );

        List<AgwDelta> deltas = agent.stream(new AgentRequest("顺序执行两个工具", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));
        assertThat(deltas).isNotNull();

        AgwEventAssembler.EventStreamState state = new AgwEventAssembler()
                .begin("顺序执行两个工具", "chat_1", "chat_1", "req_1", "run_1");
        List<AgwEvent> events = new ArrayList<>(state.bootstrapEvents());
        for (AgwDelta delta : deltas) {
            events.addAll(state.consume(toAgwDelta(delta)));
        }

        int toolAStart = indexOfToolEvent(events, "tool.start", "call_tool_a_1");
        int toolAEnd = indexOfToolEvent(events, "tool.end", "call_tool_a_1");
        int toolAResult = indexOfToolEvent(events, "tool.result", "call_tool_a_1");
        int toolBStart = indexOfToolEvent(events, "tool.start", "call_tool_b_2");
        int toolBResult = indexOfToolEvent(events, "tool.result", "call_tool_b_2");

        assertThat(toolAStart).isGreaterThanOrEqualTo(0);
        assertThat(toolAEnd).isGreaterThan(toolAStart);
        // 同一轮的并行 native FC tool_calls：两个 tool 的 start/args 在 LLM 流阶段顺序发出，
        // 然后 tool.end/tool.result 在工具执行阶段顺序发出。
        // 因此 toolB.start 在 toolA.start 之后，但在 toolA.end 之前（因为 end 在 result 时才发出）
        assertThat(toolBStart).isGreaterThan(toolAStart);
        assertThat(toolAEnd).isGreaterThan(toolBStart);
        // tool results 按执行顺序：A result 在 A end 之后，B result 在 A result 之后
        assertThat(toolAResult).isGreaterThan(toolAEnd);
        assertThat(toolBResult).isGreaterThan(toolAResult);
        assertThat(countToolArgsEvents(events, "call_tool_a_1")).isEqualTo(1);
    }

    @Test
    void planExecuteLoopShouldEmitToolCallBeforeSlowToolResult() {
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
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_bash_1",
                                            "function",
                                            "bash", "{\"command\":\"ls\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                return Flux.just(new LlmDelta(null, null, "stop"));
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Flux.just("执行完成。");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Flux.just("执行完成。");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Mono.just("");
            }
        };

        BaseTool slowBashTool = new BaseTool() {
            @Override
            public String name() {
                return "bash";
            }

            @Override
            public String description() {
                return "slow bash";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                try {
                    Thread.sleep(400);
                } catch (InterruptedException ex) {
                    Thread.currentThread().interrupt();
                }
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
                new ToolRegistry(List.of(slowBashTool)),
                objectMapper
        );

        AgwDelta firstToolCallDelta = agent.stream(new AgentRequest("执行一次 ls", null, null, null))
                .filter(delta -> delta.toolCalls() != null && !delta.toolCalls().isEmpty())
                .blockFirst(Duration.ofMillis(250));

        assertThat(firstToolCallDelta).isNotNull();
    }

    @Test
    void planExecuteLoopShouldResolveDateTemplateFromPreviousToolResult() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("city_datetime", "mock_city_weather")
        );

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<LlmDelta> streamDeltas(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    List<Message> historyMessages,
                    String userPrompt,
                    List<LlmFunctionTool> tools,
                    String stage,
                    boolean parallelToolCalls
            ) {
                if ("agent-plan-execute-step-1".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_city_datetime",
                                            "function",
                                            "city_datetime", "{\"city\":\"上海\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta(
                                            "call_city_weather",
                                            "function",
                                            "mock_city_weather", "{\"city\":\"上海\",\"date\":\"{{city_datetime.date+1d}}\"}"
                                    )),
                                    null
                            ),
                            new LlmDelta(null, null, "tool_calls")
                    );
                }
                if ("agent-plan-execute-step-2".equals(stage)) {
                    return Flux.just(new LlmDelta(null, null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Flux.just("done");
            }

            @Override
            public Flux<String> streamContent(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Flux.just("done");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt) {
                return Mono.just("");
            }

            @Override
            public Mono<String> completeText(ProviderType providerType, String model, String systemPrompt, String userPrompt, String stage) {
                return Mono.just("");
            }
        };

        BaseTool cityDateTimeTool = new BaseTool() {
            @Override
            public String name() {
                return "city_datetime";
            }

            @Override
            public String description() {
                return "city datetime";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of(
                        "tool", "city_datetime",
                        "city", "上海",
                        "date", "2026-02-11"
                ));
            }
        };

        AtomicReference<Map<String, Object>> weatherArgs = new AtomicReference<>();
        BaseTool weatherTool = new BaseTool() {
            @Override
            public String name() {
                return "mock_city_weather";
            }

            @Override
            public String description() {
                return "weather";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                weatherArgs.set(Map.copyOf(args));
                return objectMapper.valueToTree(Map.of("ok", true, "date", String.valueOf(args.get("date"))));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(cityDateTimeTool, weatherTool)),
                objectMapper
        );

        List<AgwDelta> deltas = agent.stream(new AgentRequest("查上海明天天气", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(weatherArgs.get()).isNotNull();
        assertThat(weatherArgs.get().get("date")).isEqualTo("2026-02-12");
    }

    // ======================== Helper Methods ========================

    private int indexOfThinkingContaining(List<AgwDelta> deltas, String text) {
        for (int i = 0; i < deltas.size(); i++) {
            String thinking = deltas.get(i).thinking();
            if (thinking != null && thinking.contains(text)) {
                return i;
            }
        }
        return -1;
    }

    private int indexOfToolCall(List<AgwDelta> deltas) {
        return indexOfToolCall(deltas, 1);
    }

    private int indexOfToolCall(List<AgwDelta> deltas, int occurrence) {
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

    private int indexOfToolCallById(List<AgwDelta> deltas, String toolId) {
        for (int i = 0; i < deltas.size(); i++) {
            List<ToolCallDelta> toolCalls = deltas.get(i).toolCalls();
            if (toolCalls == null || toolCalls.isEmpty()) {
                continue;
            }
            boolean matched = toolCalls.stream().anyMatch(call -> toolId.equals(call.id()));
            if (matched) {
                return i;
            }
        }
        return -1;
    }

    private int indexOfToolResultById(List<AgwDelta> deltas, String toolId) {
        for (int i = 0; i < deltas.size(); i++) {
            List<AgwDelta.ToolResult> toolResults = deltas.get(i).toolResults();
            if (toolResults == null || toolResults.isEmpty()) {
                continue;
            }
            boolean matched = toolResults.stream().anyMatch(result -> toolId.equals(result.toolId()));
            if (matched) {
                return i;
            }
        }
        return -1;
    }

    private long countToolCallById(List<AgwDelta> deltas, String toolId) {
        return deltas.stream()
                .filter(delta -> delta.toolCalls() != null && !delta.toolCalls().isEmpty())
                .filter(delta -> delta.toolCalls().stream().anyMatch(call -> toolId.equals(call.id())))
                .count();
    }

    private AgwDelta toAgwDelta(AgwDelta delta) {
        return delta;
    }

    private int indexOfToolEvent(List<AgwEvent> events, String type, String toolId) {
        for (int i = 0; i < events.size(); i++) {
            AgwEvent event = events.get(i);
            if (!type.equals(event.type())) {
                continue;
            }
            Object value = event.payload().get("toolId");
            if (toolId.equals(String.valueOf(value))) {
                return i;
            }
        }
        return -1;
    }

    private long countToolArgsEvents(List<AgwEvent> events, String toolId) {
        return events.stream()
                .filter(event -> "tool.args".equals(event.type()))
                .filter(event -> toolId.equals(String.valueOf(event.payload().get("toolId"))))
                .count();
    }
}
