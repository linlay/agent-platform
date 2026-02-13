package com.linlay.springaiagw.agent;

import com.aiagent.agw.sdk.model.LlmDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.ControlStrategy;
import com.linlay.springaiagw.agent.runtime.policy.OutputPolicy;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.agw.AgentDelta;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.LlmCallSpec;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.junit.jupiter.api.Test;
import org.springframework.ai.chat.messages.Message;
import org.springframework.ai.chat.messages.ToolResponseMessage;
import reactor.core.publisher.Flux;

import java.time.Duration;
import java.util.ArrayList;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;

import static org.assertj.core.api.Assertions.assertThat;

class DefinitionDrivenAgentTest {

    private final ObjectMapper objectMapper = new ObjectMapper();

    @Test
    void plainToolingShouldEmitToolCallResultAndFinalAnswer() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlainTooling",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.PLAIN_TOOLING,
                new RunSpec(
                        ControlStrategy.TOOL_ONESHOT,
                        OutputPolicy.PLAIN,
                        ToolPolicy.ALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        false,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("你是测试助手", null, null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-tooling-first".equals(stage)) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_echo_1", "function", "echo_tool", "{\"text\":\"hello\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-tooling-final".equals(stage)) {
                    return Flux.just(new LlmDelta("最终结论", null, "stop"));
                }
                return Flux.empty();
            }
        };

        BaseTool echoTool = new BaseTool() {
            @Override
            public String name() {
                return "echo_tool";
            }

            @Override
            public String description() {
                return "echo";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "echo", args.get("text")));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(echoTool)),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试工具模式", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().flatMap(d -> d.toolCalls().stream()).map(ToolCallDelta::id).toList())
                .contains("call_echo_1");
        assertThat(deltas.stream().flatMap(d -> d.toolResults().stream()).map(AgentDelta.ToolResult::toolId).toList())
                .contains("call_echo_1");
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("最终结论");
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    @Test
    void plainShouldStreamContentChunksWithoutMerging() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlain",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.PLAIN,
                new RunSpec(
                        ControlStrategy.ONESHOT,
                        OutputPolicy.PLAIN,
                        ToolPolicy.DISALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        false,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("你是测试助手", null, null, null),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-plain-oneshot".equals(stage)) {
                    return Flux.just(
                            new LlmDelta("这", null, null),
                            new LlmDelta("是", null, null),
                            new LlmDelta("答案", null, "stop")
                    );
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of()),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plain", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        List<String> contentDeltas = deltas.stream()
                .map(AgentDelta::content)
                .filter(value -> value != null && !value.isBlank())
                .toList();
        assertThat(contentDeltas).containsExactly("这", "是", "答案");
        assertThat(deltas.get(deltas.size() - 1).finishReason()).isEqualTo("stop");
    }

    @Test
    void toolingShouldKeepToolArgsAsStreamedChunks() {
        AgentDefinition definition = new AgentDefinition(
                "demoPlainToolingChunk",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.PLAIN_TOOLING,
                new RunSpec(
                        ControlStrategy.TOOL_ONESHOT,
                        OutputPolicy.PLAIN,
                        ToolPolicy.ALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        false,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("你是测试助手", null, null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-tooling-first".equals(stage)) {
                    return Flux.just(
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta("call_chunk_1", "function", "echo_tool", "{\"text\":\"he")),
                                    null
                            ),
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta("call_chunk_1", "function", null, "llo\"}")),
                                    "tool_calls"
                            )
                    );
                }
                if ("agent-tooling-final".equals(stage)) {
                    return Flux.just(new LlmDelta("最终结论", null, "stop"));
                }
                return Flux.empty();
            }
        };

        BaseTool echoTool = new BaseTool() {
            @Override
            public String name() {
                return "echo_tool";
            }

            @Override
            public String description() {
                return "echo";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "echo", args.get("text")));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(echoTool)),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 tool args chunk", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        List<String> toolArgsDeltas = deltas.stream()
                .flatMap(delta -> delta.toolCalls().stream())
                .map(ToolCallDelta::arguments)
                .filter(value -> value != null && !value.isBlank())
                .toList();
        assertThat(toolArgsDeltas).containsExactly("{\"text\":\"he", "llo\"}");
    }

    @Test
    void thinkingShouldExposeReasoningSummaryWhenEnabled() {
        AgentDefinition definition = new AgentDefinition(
                "demoThinking",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.THINKING,
                new RunSpec(
                        ControlStrategy.ONESHOT,
                        OutputPolicy.REASONING_SUMMARY,
                        ToolPolicy.DISALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        true,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("你是测试助手", null, null, null),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<String> contentByStage(String stage) {
                if ("agent-thinking-oneshot".equals(stage)) {
                    return Flux.just("{\"finalText\":\"答案正文\",\"reasoningSummary\":\"推理摘要\"}");
                }
                return Flux.just("答案正文");
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of()),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 thinking", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().map(AgentDelta::thinking).toList()).contains("推理摘要");
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("答案正文");
    }

    @Test
    void thinkingShouldStreamStructuredFieldDeltasIncrementally() {
        AgentDefinition definition = new AgentDefinition(
                "demoThinkingChunk",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.THINKING,
                new RunSpec(
                        ControlStrategy.ONESHOT,
                        OutputPolicy.REASONING_SUMMARY,
                        ToolPolicy.DISALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        true,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("你是测试助手", null, null, null),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<String> contentByStage(String stage) {
                if ("agent-thinking-oneshot".equals(stage)) {
                    return Flux.just(
                            "{\"finalText\":\"答",
                            "案正文\",\"reasoningSummary\":\"推",
                            "理摘要\"}"
                    );
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of()),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 thinking chunk", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        List<String> reasoningDeltas = deltas.stream()
                .map(AgentDelta::thinking)
                .filter(value -> value != null && !value.isBlank())
                .toList();
        List<String> contentDeltas = deltas.stream()
                .map(AgentDelta::content)
                .filter(value -> value != null && !value.isBlank())
                .toList();
        assertThat(reasoningDeltas.size()).isGreaterThan(1);
        assertThat(contentDeltas.size()).isGreaterThan(1);
        assertThat(String.join("", reasoningDeltas)).isEqualTo("推理摘要");
        assertThat(String.join("", contentDeltas)).isEqualTo("答案正文");
    }

    @Test
    void planExecuteShouldUseDualSystemPrompts() {
        List<String> captured = new CopyOnWriteArrayList<>();

        AgentDefinition definition = new AgentDefinition(
                "demoPlan",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(
                        ControlStrategy.PLAN_EXECUTE,
                        OutputPolicy.PLAIN,
                        ToolPolicy.ALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.HIGH,
                        false,
                        Budget.DEFAULT
                ),
                new AgentPromptSet(
                        "执行系统提示",
                        "规划系统提示",
                        "执行系统提示",
                        "总结系统提示"
                ),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                captured.add(spec.stage() + "::" + spec.systemPrompt());
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("{\"steps\":[{\"id\":\"s1\",\"title\":\"步骤1\",\"goal\":\"目标\",\"successCriteria\":\"标准\"}]}", null, "stop"));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("步骤执行完成", null, "stop"));
                }
                return Flux.just(new LlmDelta("最终回答", null, "stop"));
            }

            @Override
            public Flux<String> streamContent(LlmCallSpec spec) {
                captured.add(spec.stage() + "::" + spec.systemPrompt());
                return Flux.just("最终回答");
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of()),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan execute", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-generate::规划系统提示"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-execute-step-1::执行系统提示"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-final::总结系统提示"))).isTrue();
    }

    @Test
    void reactShouldContinueUntilToolThenFinalAnswer() {
        AgentDefinition definition = new AgentDefinition(
                "demoReact",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.REACT,
                new RunSpec(
                        ControlStrategy.REACT_LOOP,
                        OutputPolicy.PLAIN,
                        ToolPolicy.ALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        false,
                        new Budget(10, 10, 4, 60_000)
                ),
                new AgentPromptSet("你是测试助手", null, null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            private int step;

            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if (stage.startsWith("agent-react-step-")) {
                    step++;
                    if (step == 1) {
                        return Flux.just(new LlmDelta(
                                null,
                                List.of(new ToolCallDelta("call_react_1", "function", "echo_tool", "{\"text\":\"ping\"}")),
                                "tool_calls"
                        ));
                    }
                    return Flux.just(new LlmDelta("react 最终结论", null, "stop"));
                }
                return Flux.empty();
            }
        };

        BaseTool echoTool = new BaseTool() {
            @Override
            public String name() {
                return "echo_tool";
            }

            @Override
            public String description() {
                return "echo";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(echoTool)),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 react", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().flatMap(d -> d.toolCalls().stream()).map(ToolCallDelta::id).toList())
                .contains("call_react_1");
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("react 最终结论");
    }

    @Test
    void toolPolicyRequireShouldTriggerRepairTurnInToolOneShot() {
        List<LlmCallSpec> capturedSpecs = new CopyOnWriteArrayList<>();

        AgentDefinition definition = new AgentDefinition(
                "demoRequireTooling",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.PLAIN_TOOLING,
                new RunSpec(
                        ControlStrategy.TOOL_ONESHOT,
                        OutputPolicy.PLAIN,
                        ToolPolicy.REQUIRE,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        false,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("固定系统提示", null, null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                capturedSpecs.add(spec);
                if ("agent-tooling-first".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("先不调工具", null, "stop"));
                }
                if ("agent-tooling-first-repair".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_require_1", "function", "echo_tool", "{\"text\":\"repair\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-tooling-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("require 最终结论", null, "stop"));
                }
                return Flux.empty();
            }
        };

        BaseTool echoTool = new BaseTool() {
            @Override
            public String name() {
                return "echo_tool";
            }

            @Override
            public String description() {
                return "echo";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(echoTool)),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 require", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().flatMap(d -> d.toolCalls().stream()).map(ToolCallDelta::id).toList())
                .contains("call_require_1");
        assertThat(capturedSpecs.stream().map(LlmCallSpec::stage))
                .contains("agent-tooling-first-repair");
        LlmCallSpec repairSpec = capturedSpecs.stream()
                .filter(spec -> "agent-tooling-first-repair".equals(spec.stage()))
                .findFirst()
                .orElseThrow();
        assertThat(repairSpec.messages().stream()
                .map(Message::getText)
                .anyMatch(text -> text != null && text.contains("必须调用至少一个工具")))
                .isTrue();
    }

    @Test
    void nonPlanToolingShouldKeepSystemPromptStableAndAppendToolMessages() {
        List<LlmCallSpec> capturedSpecs = new CopyOnWriteArrayList<>();

        AgentDefinition definition = new AgentDefinition(
                "demoCache",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.PLAIN_TOOLING,
                new RunSpec(
                        ControlStrategy.TOOL_ONESHOT,
                        OutputPolicy.PLAIN,
                        ToolPolicy.ALLOW,
                        VerifyPolicy.NONE,
                        ComputePolicy.MEDIUM,
                        false,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("固定 system prompt", null, null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                capturedSpecs.add(spec);
                if ("agent-tooling-first".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_cache_1", "function", "echo_tool", "{\"text\":\"hello\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-tooling-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("缓存结论", null, "stop"));
                }
                return Flux.empty();
            }
        };

        BaseTool echoTool = new BaseTool() {
            @Override
            public String name() {
                return "echo_tool";
            }

            @Override
            public String description() {
                return "echo";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "echo", args.get("text")));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of(echoTool)),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试缓存", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(capturedSpecs).hasSizeGreaterThanOrEqualTo(2);
        assertThat(capturedSpecs.stream().map(LlmCallSpec::systemPrompt).distinct())
                .containsExactly("固定 system prompt");

        LlmCallSpec secondCall = capturedSpecs.stream()
                .filter(spec -> "agent-tooling-final".equals(spec.stage()))
                .findFirst()
                .orElseThrow();
        assertThat(secondCall.messages().stream().anyMatch(message -> message instanceof ToolResponseMessage)).isTrue();
        assertThat(secondCall.userPrompt()).doesNotContain("toolResults").doesNotContain("历史工具结果");
    }

    @Test
    void secondPassFixShouldOnlyExposeVerifyStreamOutput() {
        AgentDefinition definition = new AgentDefinition(
                "demoVerifySecondPass",
                "demo",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.PLAIN,
                new RunSpec(
                        ControlStrategy.ONESHOT,
                        OutputPolicy.PLAIN,
                        ToolPolicy.DISALLOW,
                        VerifyPolicy.SECOND_PASS_FIX,
                        ComputePolicy.MEDIUM,
                        false,
                        Budget.DEFAULT
                ),
                new AgentPromptSet("你是测试助手", null, null, null),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-plain-oneshot".equals(stage)) {
                    return Flux.just(new LlmDelta("候选答案", null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            protected Flux<String> contentByStage(String stage) {
                if ("agent-verify".equals(stage)) {
                    return Flux.just("修", "复");
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new DeltaStreamService(),
                new ToolRegistry(List.of()),
                objectMapper
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试二次校验", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        List<String> contentDeltas = deltas.stream()
                .map(AgentDelta::content)
                .filter(value -> value != null && !value.isBlank())
                .toList();
        assertThat(contentDeltas).containsExactly("修", "复");
        assertThat(String.join("", contentDeltas)).isEqualTo("修复");
    }

    private abstract static class StubLlmService extends LlmService {

        protected StubLlmService() {
            super(null, null);
        }

        @Override
        public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
            return deltaByStage(spec.stage());
        }

        @Override
        public Flux<String> streamContent(LlmCallSpec spec) {
            return contentByStage(spec.stage());
        }

        protected Flux<LlmDelta> deltaByStage(String stage) {
            return Flux.empty();
        }

        protected Flux<String> contentByStage(String stage) {
            return Flux.empty();
        }
    }
}
