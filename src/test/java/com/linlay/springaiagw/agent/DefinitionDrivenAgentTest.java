package com.linlay.springaiagw.agent;

import com.aiagent.agw.sdk.model.AgwDelta;
import com.aiagent.agw.sdk.model.AgwEvent;
import com.aiagent.agw.sdk.service.AgwEventAssembler;
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
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicReference;

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
        int firstToolCallIndex = indexOfToolCallById(deltas, "call_bash_step_1");
        int secondToolCallIndex = indexOfToolCallById(deltas, "call_bash_step_2");

        assertThat(step1ThinkingIndex).isGreaterThanOrEqualTo(0);
        assertThat(firstToolCallIndex).isGreaterThan(step1ThinkingIndex);
        assertThat(step2ThinkingIndex).isGreaterThan(firstToolCallIndex);
        assertThat(secondToolCallIndex).isGreaterThan(step2ThinkingIndex);
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("结论：资源情况已获取。");
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    @Test
    void planExecuteFlowShouldEmitToolCallBeforeSlowToolResult() {
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
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just("{\"thinking\":\"先执行命令\",\"plan\":[\"执行bash\"],\"toolCalls\":[{\"name\":\"bash\",\"arguments\":{\"command\":\"ls\"}}]}");
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("执行完成。");
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

        AgentDelta firstToolCallDelta = agent.stream(new AgentRequest("执行一次 ls", null, null, null, null, null, null))
                .filter(delta -> delta.toolCalls() != null && !delta.toolCalls().isEmpty())
                .blockFirst(Duration.ofMillis(250));

        assertThat(firstToolCallDelta).isNotNull();
    }

    @Test
    void planExecuteFlowShouldExecuteTwoToolsSequentiallyAndSplitToolArgs() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("tool_a", "tool_b")
        );

        String longArg = "abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789";
        String plannerResponse = """
                {"thinking":"先后执行两个工具","plan":["先执行tool_a","再执行tool_b"],"toolCalls":[{"name":"tool_a","arguments":{"text":"%s"}},{"name":"tool_b","arguments":{"ok":true}}]}
                """.formatted(longArg);

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just(plannerResponse);
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("最终结论");
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
                try {
                    Thread.sleep(120);
                } catch (InterruptedException ex) {
                    Thread.currentThread().interrupt();
                }
                return objectMapper.valueToTree(Map.of(
                        "tool", "tool_a",
                        "args", args
                ));
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
                return objectMapper.valueToTree(Map.of(
                        "tool", "tool_b",
                        "args", args
                ));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(toolA, toolB)),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("顺序调用两个工具", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(deltas).isNotEmpty();

        int firstToolResult = indexOfToolResultById(deltas, "call_tool_a_1");
        int secondToolCall = indexOfToolCallById(deltas, "call_tool_b_2");
        long firstToolCallChunks = countToolCallById(deltas, "call_tool_a_1");

        assertThat(firstToolResult).isGreaterThanOrEqualTo(0);
        assertThat(secondToolCall).isGreaterThan(firstToolResult);
        assertThat(firstToolCallChunks).isGreaterThan(1);
    }

    @Test
    void planExecuteFlowShouldExecuteStepBoundToolCallsOneByOne() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("tool_a", "tool_b")
        );

        String plannerResponse = """
                {"thinking":"按步骤执行","plan":[{"step":"先执行tool_a","toolCall":{"name":"tool_a","arguments":{"ok":true}}},{"step":"再执行tool_b","toolCall":{"name":"tool_b","arguments":{"ok":true}}}]}
                """;

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just(plannerResponse);
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("完成");
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
                return objectMapper.valueToTree(Map.of("tool", "tool_a", "ok", true));
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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("按步骤顺序执行", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(deltas).isNotEmpty();

        int step1Thinking = indexOfThinkingContaining(deltas, "执行步骤 1/2：先执行tool_a");
        int step2Thinking = indexOfThinkingContaining(deltas, "执行步骤 2/2：再执行tool_b");
        int toolACall = indexOfToolCallById(deltas, "call_tool_a_1");
        int toolAResult = indexOfToolResultById(deltas, "call_tool_a_1");
        int toolBCall = indexOfToolCallById(deltas, "call_tool_b_2");

        assertThat(step1Thinking).isGreaterThanOrEqualTo(0);
        assertThat(toolACall).isGreaterThan(step1Thinking);
        assertThat(toolAResult).isGreaterThan(toolACall);
        assertThat(step2Thinking).isGreaterThan(toolAResult);
        assertThat(toolBCall).isGreaterThan(step2Thinking);
    }

    @Test
    void planExecuteFlowShouldSplitCompositeBashCommandIntoSequentialToolCalls() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("bash")
        );

        String plannerResponse = """
                {"thinking":"分两步执行系统检查","plan":[{"step":"检查系统资源","toolCall":{"name":"bash","arguments":{"command":"df -h && free -h"}}}]}
                """;

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just(plannerResponse);
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("完成");
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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("检查系统资源", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(commands).containsExactly("df -h", "free -h");
        assertThat(indexOfToolCallById(deltas, "call_bash_2")).isGreaterThan(indexOfToolResultById(deltas, "call_bash_1"));
    }

    @Test
    void planExecuteFlowShouldEmitOrderedToolEventsInAssembler() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("tool_a", "tool_b")
        );

        String plannerResponse = """
                {"thinking":"顺序执行","plan":["tool_a","tool_b"],"toolCalls":[{"name":"tool_a","arguments":{"text":"abcdefghijklmnopqrstuvwxyz0123456789abcdefghijklmnopqrstuvwxyz0123456789"}},{"name":"tool_b","arguments":{"ok":true}}]}
                """;

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just(plannerResponse);
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("done");
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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("顺序执行两个工具", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));
        assertThat(deltas).isNotNull();

        AgwEventAssembler.EventStreamState state = new AgwEventAssembler()
                .begin("顺序执行两个工具", "chat_1", "chat_1", "req_1", "run_1");
        List<AgwEvent> events = new ArrayList<>(state.bootstrapEvents());
        for (AgentDelta delta : deltas) {
            events.addAll(state.consume(toAgwDelta(delta)));
        }

        int toolAStart = indexOfToolEvent(events, "tool.start", "call_tool_a_1");
        int toolAEnd = indexOfToolEvent(events, "tool.end", "call_tool_a_1");
        int toolAResult = indexOfToolEvent(events, "tool.result", "call_tool_a_1");
        int toolBStart = indexOfToolEvent(events, "tool.start", "call_tool_b_2");
        int toolBResult = indexOfToolEvent(events, "tool.result", "call_tool_b_2");

        assertThat(toolAStart).isGreaterThanOrEqualTo(0);
        assertThat(toolAEnd).isGreaterThan(toolAStart);
        assertThat(toolAResult).isGreaterThan(toolAEnd);
        assertThat(toolBStart).isGreaterThan(toolAResult);
        assertThat(toolBResult).isGreaterThan(toolBStart);
        assertThat(countToolArgsEvents(events, "call_tool_a_1")).isGreaterThan(1);
    }

    @Test
    void planExecuteFlowShouldInvokeToolsStrictlySequentiallyWithoutOverlap() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("tool_a", "tool_b")
        );

        String plannerResponse = """
                {"thinking":"顺序执行","plan":["tool_a","tool_b"],"toolCalls":[{"name":"tool_a","arguments":{"text":"a"}},{"name":"tool_b","arguments":{"text":"b"}}]}
                """;

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just(plannerResponse);
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("done");
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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("顺序执行两个工具", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(maxInFlight.get()).isEqualTo(1);
        assertThat(invokeOrder).containsExactly("tool_a:start", "tool_a:end", "tool_b:start", "tool_b:end");
    }

    @Test
    void planExecuteFlowShouldResolveTomorrowUsingPreviousCityDatetime() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("city_datetime", "mock_city_weather")
        );

        String plannerResponse = """
                {"thinking":"先取时间再查天气","plan":["获取上海当前日期","查询明天天气"],"toolCalls":[{"name":"city_datetime","arguments":{"city":"上海"}},{"name":"mock_city_weather","arguments":{"city":"上海","date":"tomorrow"}}]}
                """;

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just(plannerResponse);
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("done");
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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("查上海明天天气", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(weatherArgs.get()).isNotNull();
        assertThat(weatherArgs.get().get("date")).isEqualTo("2026-02-12");
    }

    @Test
    void planExecuteFlowShouldResolveDateTemplateFromPreviousToolResult() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlanExecute",
                "demo",
                ProviderType.BAILIAN,
                "qwen3-max",
                "你是测试助手",
                AgentMode.PLAN_EXECUTE,
                List.of("city_datetime", "mock_city_weather")
        );

        String plannerResponse = """
                {"thinking":"先取时间再查天气","plan":["获取上海当前日期","查询明天天气"],"toolCalls":[{"name":"city_datetime","arguments":{"city":"上海"}},{"name":"mock_city_weather","arguments":{"city":"上海","date":"{{city_datetime.date+1d}}"}}]}
                """;

        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<String> streamContent(
                    ProviderType providerType,
                    String model,
                    String systemPrompt,
                    String userPrompt,
                    String stage
            ) {
                if ("agent-plan-execute-planner".equals(stage)) {
                    return Flux.just(plannerResponse);
                }
                if ("agent-plan-execute-final".equals(stage)) {
                    return Flux.just("done");
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

        List<AgentDelta> deltas = agent.stream(new AgentRequest("查上海明天天气", null, null, null, null, null, null))
                .collectList()
                .block(Duration.ofSeconds(6));

        assertThat(deltas).isNotNull();
        assertThat(weatherArgs.get()).isNotNull();
        assertThat(weatherArgs.get().get("date")).isEqualTo("2026-02-12");
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

    private int indexOfToolCallById(List<AgentDelta> deltas, String toolId) {
        for (int i = 0; i < deltas.size(); i++) {
            List<com.linlay.springaiagw.model.SseChunk.ToolCall> toolCalls = deltas.get(i).toolCalls();
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

    private int indexOfToolResultById(List<AgentDelta> deltas, String toolId) {
        for (int i = 0; i < deltas.size(); i++) {
            List<AgentDelta.ToolResult> toolResults = deltas.get(i).toolResults();
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

    private long countToolCallById(List<AgentDelta> deltas, String toolId) {
        return deltas.stream()
                .filter(delta -> delta.toolCalls() != null && !delta.toolCalls().isEmpty())
                .filter(delta -> delta.toolCalls().stream().anyMatch(call -> toolId.equals(call.id())))
                .count();
    }

    private AgwDelta toAgwDelta(AgentDelta delta) {
        List<AgwDelta.ToolCall> toolCalls = delta.toolCalls() == null ? null : delta.toolCalls().stream()
                .map(call -> new AgwDelta.ToolCall(
                        call.id(),
                        call.type(),
                        call.function() == null ? null : call.function().name(),
                        call.function() == null ? null : call.function().arguments()
                ))
                .toList();
        List<AgwDelta.ToolResult> toolResults = delta.toolResults() == null ? null : delta.toolResults().stream()
                .map(result -> new AgwDelta.ToolResult(result.toolId(), result.result()))
                .toList();
        return new AgwDelta(delta.content(), delta.thinking(), toolCalls, toolResults, delta.finishReason());
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
