package com.linlay.springaiagw.agent;

import com.aiagent.agw.sdk.model.LlmDelta;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.mode.OneshotMode;
import com.linlay.springaiagw.agent.mode.PlanExecuteMode;
import com.linlay.springaiagw.agent.mode.ReactMode;
import com.linlay.springaiagw.agent.mode.StageSettings;
import com.linlay.springaiagw.agent.runtime.AgentRuntimeMode;
import com.linlay.springaiagw.agent.runtime.policy.Budget;
import com.linlay.springaiagw.agent.runtime.policy.ComputePolicy;
import com.linlay.springaiagw.agent.runtime.policy.ControlStrategy;
import com.linlay.springaiagw.agent.runtime.policy.OutputPolicy;
import com.linlay.springaiagw.agent.runtime.policy.RunSpec;
import com.linlay.springaiagw.agent.runtime.policy.ToolChoice;
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.memory.ChatWindowMemoryProperties;
import com.linlay.springaiagw.memory.ChatWindowMemoryStore;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.service.LlmCallSpec;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.skill.SkillCatalogProperties;
import com.linlay.springaiagw.skill.SkillRegistryService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.SystemPlanAddTasks;
import com.linlay.springaiagw.tool.SystemPlanGetTasks;
import com.linlay.springaiagw.tool.SystemPlanUpdateTask;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import reactor.core.publisher.Flux;

import java.nio.file.Files;
import java.nio.file.Path;
import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.UUID;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.CopyOnWriteArrayList;
import java.util.concurrent.atomic.AtomicReference;
import java.util.regex.Matcher;
import java.util.regex.Pattern;

import static org.assertj.core.api.Assertions.assertThat;

@ExtendWith(OutputCaptureExtension.class)
class DefinitionDrivenAgentTest {

    private final ObjectMapper objectMapper = new ObjectMapper();
    private static final Pattern CURRENT_TASK_ID_PATTERN =
            Pattern.compile("(?:当前要执行的 taskId|当前 taskId):\\s*([^\\n]+)");

    @Test
    void oneshotToolingShouldEmitToolCallResultAndFinalAnswer() {
        AgentDefinition definition = definition(
                "demoOneshotTooling",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of("echo_tool"), false, ComputePolicy.MEDIUM), null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-oneshot-tool-first".equals(stage)) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_echo_1", "function", "echo_tool", "{\"text\":\"hello\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-oneshot-tool-final".equals(stage)) {
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
                new ToolRegistry(List.of(echoTool)),
                objectMapper,
                null,
                null
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
    void oneshotShouldStreamContentChunksWithoutMerging() {
        AgentDefinition definition = definition(
                "demoOneshot",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.DISALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of(), false, ComputePolicy.MEDIUM), null, null),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-oneshot".equals(stage)) {
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
                new ToolRegistry(List.of()),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 oneshot", null, null, null))
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
    void oneshotReasoningShouldExposeReasoningTokensWhenEnabled() {
        AgentDefinition definition = definition(
                "demoReasoning",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.DISALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of(), true, ComputePolicy.HIGH), null, null),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-oneshot".equals(stage)) {
                    return Flux.just(
                            new LlmDelta("推理摘要", null, null, null),
                            new LlmDelta(null, "答案正文", null, "stop")
                    );
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of()),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 reasoning", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().map(AgentDelta::reasoning).toList()).contains("推理摘要");
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("答案正文");
    }

    @Test
    void shouldInjectSkillCatalogIntoSystemPromptOnly() throws Exception {
        Path skillsRoot = Files.createTempDirectory("skills-registry");
        Path skillFile = skillsRoot.resolve("screenshot").resolve("SKILL.md");
        Files.createDirectories(skillFile.getParent());
        Files.writeString(skillFile, """
                ---
                name: "screenshot"
                description: "capture screenshots"
                ---
                # Skill Prompt
                always verify target window.
                """);

        SkillCatalogProperties skillProperties = new SkillCatalogProperties();
        skillProperties.setExternalDir(skillsRoot.toString());
        skillProperties.setMaxPromptChars(1000);
        SkillRegistryService skillRegistryService = new SkillRegistryService(skillProperties, null);

        AgentDefinition definition = new AgentDefinition(
                "demoWithSkill",
                "demoWithSkill",
                null,
                "demo with skill",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.DISALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of(), false, ComputePolicy.MEDIUM), null, null),
                List.of(),
                List.of("screenshot")
        );

        AtomicReference<LlmCallSpec> captured = new AtomicReference<>();
        LlmService llmService = new LlmService(null, null) {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                captured.set(spec);
                return Flux.just(new LlmDelta("完成", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of()),
                objectMapper,
                null,
                null,
                skillRegistryService
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 skill 注入", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(captured.get()).isNotNull();
        assertThat(captured.get().systemPrompt()).contains("你是测试助手");
        assertThat(captured.get().systemPrompt()).contains("skillId: screenshot");
        assertThat(captured.get().systemPrompt()).contains("description: capture screenshots");
        assertThat(captured.get().systemPrompt()).doesNotContain("instructions:");
        assertThat(captured.get().systemPrompt()).doesNotContain("always verify target window.");
        assertThat(captured.get().userPrompt()).isNull();
    }

    @Test
    void shouldAppendDeferredSkillPromptAfterSkillToolCall() throws Exception {
        SkillRegistryService skillRegistryService = createSkillRegistry("always verify target window.");
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();

        AgentDefinition definition = new AgentDefinition(
                "demoSkillDeferredPrompt",
                "demoSkillDeferredPrompt",
                null,
                "demo deferred prompt",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of("_skill_run_script_"), false, ComputePolicy.MEDIUM), null, null),
                List.of("_skill_run_script_"),
                List.of("screenshot")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stageSpecs.put(spec.stage(), spec);
                if ("agent-oneshot-tool-first".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta(
                                    "call_skill_1",
                                    "function",
                                    "_skill_run_script_",
                                    "{\"skill\":\"screenshot\",\"script\":\"scripts/demo_echo.py\"}"
                            )),
                            "tool_calls"
                    ));
                }
                if ("agent-oneshot-tool-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("完成", null, "stop"));
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(skillScriptRunTool())),
                objectMapper,
                null,
                null,
                skillRegistryService
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 skill 延迟注入", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(stageSpecs.get("agent-oneshot-tool-first")).isNotNull();
        assertThat(stageSpecs.get("agent-oneshot-tool-final")).isNotNull();
        assertThat(stageSpecs.get("agent-oneshot-tool-first").systemPrompt())
                .contains("skillId: screenshot")
                .doesNotContain("always verify target window.");
        assertThat(stageSpecs.get("agent-oneshot-tool-first").userPrompt()).isNull();
        assertThat(stageSpecs.get("agent-oneshot-tool-final").systemPrompt())
                .contains("skillId: screenshot")
                .contains("instructions:")
                .contains("always verify target window.");
        assertThat(stageSpecs.get("agent-oneshot-tool-final").userPrompt()).isNull();
    }

    @Test
    void shouldDiscloseSameSkillOnlyOnceAcrossMultipleToolCalls() throws Exception {
        SkillRegistryService skillRegistryService = createSkillRegistry("always verify target window.");
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();

        AgentDefinition definition = new AgentDefinition(
                "demoSkillDedup",
                "demoSkillDedup",
                null,
                "demo skill dedup",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.REACT,
                new RunSpec(ControlStrategy.REACT_LOOP, OutputPolicy.PLAIN, ToolPolicy.ALLOW, new Budget(10, 10, 4, 60_000)),
                new ReactMode(new StageSettings("你是测试助手", null, null, List.of("_skill_run_script_"), false, ComputePolicy.MEDIUM), 4, null, null),
                List.of("_skill_run_script_"),
                List.of("screenshot")
        );

        LlmService llmService = new StubLlmService() {
            private int step;

            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stageSpecs.put(spec.stage(), spec);
                if (!spec.stage().startsWith("agent-react-step-")) {
                    return Flux.empty();
                }
                step++;
                if (step <= 2) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta(
                                    "call_skill_" + step,
                                    "function",
                                    "_skill_run_script_",
                                    "{\"skill\":\"screenshot\",\"script\":\"scripts/demo_echo.py\"}"
                            )),
                            "tool_calls"
                    ));
                }
                return Flux.just(new LlmDelta("完成", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(skillScriptRunTool())),
                objectMapper,
                null,
                null,
                skillRegistryService
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 skill 去重披露", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(stageSpecs.get("agent-react-step-1")).isNotNull();
        assertThat(stageSpecs.get("agent-react-step-2")).isNotNull();
        assertThat(stageSpecs.get("agent-react-step-3")).isNotNull();
        assertThat(stageSpecs.get("agent-react-step-1").userPrompt()).isNull();
        assertThat(stageSpecs.get("agent-react-step-2").systemPrompt()).contains("always verify target window.");
        assertThat(stageSpecs.get("agent-react-step-3").systemPrompt()).doesNotContain("always verify target window.");
        assertThat(stageSpecs.get("agent-react-step-2").userPrompt()).isNull();
        assertThat(stageSpecs.get("agent-react-step-3").userPrompt()).isNull();
    }

    @Test
    void shouldIgnoreUnknownSkillFromToolCallAndLogWarning(CapturedOutput output) throws Exception {
        SkillRegistryService skillRegistryService = createSkillRegistry("always verify target window.");
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();

        AgentDefinition definition = new AgentDefinition(
                "demoSkillUnknown",
                "demoSkillUnknown",
                null,
                "demo unknown skill",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of("_skill_run_script_"), false, ComputePolicy.MEDIUM), null, null),
                List.of("_skill_run_script_"),
                List.of("screenshot")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stageSpecs.put(spec.stage(), spec);
                if ("agent-oneshot-tool-first".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta(
                                    "call_skill_unknown",
                                    "function",
                                    "_skill_run_script_",
                                    "{\"skill\":\"unknown_skill\",\"script\":\"scripts/demo_echo.py\"}"
                            )),
                            "tool_calls"
                    ));
                }
                if ("agent-oneshot-tool-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("完成", null, "stop"));
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(skillScriptRunTool())),
                objectMapper,
                null,
                null,
                skillRegistryService
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试未知 skill", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(stageSpecs.get("agent-oneshot-tool-final")).isNotNull();
        assertThat(stageSpecs.get("agent-oneshot-tool-final").systemPrompt())
                .doesNotContain("instructions:")
                .doesNotContain("always verify target window.");
        assertThat(stageSpecs.get("agent-oneshot-tool-final").userPrompt()).isNull();
        String logs = output.getOut() + output.getErr();
        assertThat(logs).contains("requested unknown skill");
    }

    @Test
    void shouldNotPersistDeferredSkillPromptIntoChatMemory() throws Exception {
        SkillRegistryService skillRegistryService = createSkillRegistry("always verify target window.");
        Path memoryDir = Files.createTempDirectory("chat-memory");
        ChatWindowMemoryProperties properties = new ChatWindowMemoryProperties();
        properties.setDir(memoryDir.toString());
        properties.setK(20);
        ChatWindowMemoryStore chatWindowMemoryStore = new ChatWindowMemoryStore(objectMapper, properties);
        String chatId = UUID.randomUUID().toString();

        AgentDefinition definition = new AgentDefinition(
                "demoSkillMemory",
                "demoSkillMemory",
                null,
                "demo skill memory",
                "bailian",
                "qwen3-max",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of("_skill_run_script_"), false, ComputePolicy.MEDIUM), null, null),
                List.of("_skill_run_script_"),
                List.of("screenshot")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                if ("agent-oneshot-tool-first".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta(
                                    "call_skill_memory",
                                    "function",
                                    "_skill_run_script_",
                                    "{\"skill\":\"screenshot\",\"script\":\"scripts/demo_echo.py\"}"
                            )),
                            "tool_calls"
                    ));
                }
                if ("agent-oneshot-tool-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("完成", null, "stop"));
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(skillScriptRunTool())),
                objectMapper,
                chatWindowMemoryStore,
                null,
                skillRegistryService
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 memory", chatId, "req_memory", "run_memory"))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        Path memoryFile = memoryDir.resolve(chatId + ".json");
        assertThat(memoryFile).exists();
        String stored = Files.readString(memoryFile);
        assertThat(stored).contains("测试 memory");
        assertThat(stored).doesNotContain("always verify target window.");
        assertThat(stored).doesNotContain("instructions:");
    }

    @Test
    void reactShouldContinueUntilToolThenFinalAnswer() {
        AgentDefinition definition = definition(
                "demoReact",
                AgentRuntimeMode.REACT,
                new RunSpec(ControlStrategy.REACT_LOOP, OutputPolicy.PLAIN, ToolPolicy.ALLOW, new Budget(10, 10, 4, 60_000)),
                new ReactMode(new StageSettings("你是测试助手", null, null, List.of("echo_tool"), true, ComputePolicy.MEDIUM), 6, null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            private int step;

            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if (stage.startsWith("agent-react-step-")) {
                    step++;
                    if (step == 1) {
                        return Flux.just(
                                new LlmDelta("思考中", null, null, null),
                                new LlmDelta(
                                        null,
                                        List.of(new ToolCallDelta("call_react_1", "function", "echo_tool", "{\"text\":\"ping\"}")),
                                        "tool_calls"
                                )
                        );
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
                new ToolRegistry(List.of(echoTool)),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 react", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().map(AgentDelta::reasoning).filter(value -> value != null && !value.isBlank()))
                .contains("思考中");
        assertThat(deltas.stream().flatMap(d -> d.toolCalls().stream()).map(ToolCallDelta::id).toList())
                .contains("call_react_1");
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("react 最终结论");
    }

    @Test
    void reactShouldRetryBlankFinalWithoutInjectingRedundantUserMessage() {
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();
        AgentDefinition definition = definition(
                "demoReactRetryBlankFinal",
                AgentRuntimeMode.REACT,
                new RunSpec(ControlStrategy.REACT_LOOP, OutputPolicy.PLAIN, ToolPolicy.ALLOW, new Budget(10, 10, 2, 60_000)),
                new ReactMode(new StageSettings("你是测试助手", null, null, List.of(), false, ComputePolicy.MEDIUM), 2, null, null),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stageSpecs.put(spec.stage(), spec);
                if ("agent-react-step-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("", null, "stop"));
                }
                if ("agent-react-step-2".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("react 空回复重试后最终结论", null, "stop"));
                }
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of()),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 react 空回复重试", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(stageSpecs.get("agent-react-step-1")).isNotNull();
        assertThat(stageSpecs.get("agent-react-step-2")).isNotNull();
        List<String> step2Messages = stageSpecs.get("agent-react-step-2")
                .messages()
                .stream()
                .map(message -> message.getText() == null ? "" : message.getText())
                .toList();
        assertThat(step2Messages).containsExactly("测试 react 空回复重试");
        assertThat(step2Messages).noneMatch(text -> text.contains("请基于已有信息给出最终答案，或调用工具获取更多信息。"));
        assertThat(deltas.stream().map(AgentDelta::content).toList()).contains("react 空回复重试后最终结论");
    }

    @Test
    void reactFinalShouldUseModelOutputWithoutBlockedFallback() {
        AgentDefinition definition = definition(
                "demoReactForceFinalFallback",
                AgentRuntimeMode.REACT,
                new RunSpec(ControlStrategy.REACT_LOOP, OutputPolicy.PLAIN, ToolPolicy.ALLOW, new Budget(10, 10, 1, 60_000)),
                new ReactMode(new StageSettings("你是测试助手", null, null, List.of("echo_tool"), false, ComputePolicy.MEDIUM), 1, null, null),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-react-step-1".equals(stage)) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_force_1", "function", "echo_tool", "{\"text\":\"ping\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-react-final".equals(stage)) {
                    return Flux.just(new LlmDelta("我需要先检查系统中是否有可执行脚本。", null, "stop"));
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
                return objectMapper.valueToTree(Map.of("ok", true, "msg", "ready"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(echoTool)),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 react 强制终局回退", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        String mergedContent = deltas.stream()
                .map(AgentDelta::content)
                .filter(value -> value != null && !value.isBlank())
                .reduce("", String::concat);
        assertThat(mergedContent)
                .contains("我需要先检查系统中是否有可执行脚本。")
                .doesNotContain("已确认信息")
                .doesNotContain("阻塞点")
                .doesNotContain("最小下一步");
    }

    @Test
    void planExecuteShouldUseStageSystemPrompts() {
        List<String> captured = new CopyOnWriteArrayList<>();
        Map<String, List<String>> stageTools = new ConcurrentHashMap<>();
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();

        AgentDefinition definition = definition(
                "demoPlan",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                captured.add(spec.stage() + "::" + spec.systemPrompt());
                stageTools.put(spec.stage(), spec.tools().stream().map(LlmService.LlmFunctionTool::name).toList());
                stageSpecs.put(spec.stage(), spec);
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_1", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"s1\",\"description\":\"步骤1\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    String taskId = currentTaskId(spec);
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_update_1", "function", "_plan_update_task_",
                                    "{\"taskId\":\"" + taskId + "\",\"status\":\"completed\"}")),
                            "tool_calls"
                    ));
                }
                return Flux.just(new LlmDelta("最终回答", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan execute", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(captured.stream().noneMatch(item -> item.contains("agent-plan-draft::"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-generate::规划系统提示"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-execute-step-1::执行系统提示"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-final::总结系统提示"))).isTrue();
        assertThat(stageTools.get("agent-plan-generate")).containsExactly("_plan_add_tasks_");
        assertThat(stageTools.get("agent-plan-execute-step-1")).containsExactly("_plan_update_task_");
        assertThat(stageSpecs.get("agent-plan-generate").reasoningEnabled()).isFalse();
        assertThat(stageSpecs.get("agent-plan-generate").toolChoice()).isEqualTo(ToolChoice.REQUIRED);
        assertThat(stageSpecs.get("agent-plan-generate").userPrompt()).isNull();
        assertThat(stageSpecs.get("agent-plan-generate").messages().stream().map(message -> message.getText()).toList())
                .containsExactly("测试 plan execute");
    }

    @Test
    void planExecuteDeepThinkingShouldUseDraftThenGenerate() {
        List<String> stages = new CopyOnWriteArrayList<>();
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();

        AgentDefinition definition = definition(
                "demoPlanPublicTurns",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), true, ComputePolicy.MEDIUM, true),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stages.add(spec.stage());
                stageSpecs.put(spec.stage(), spec);
                if ("agent-plan-draft".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("先分析约束并输出规划正文", null, "stop"));
                }
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_public", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"p1\",\"description\":\"任务A\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    String taskId = currentTaskId(spec);
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_update_public", "function", "_plan_update_task_",
                                    "{\"taskId\":\"" + taskId + "\",\"status\":\"completed\"}")),
                            "tool_calls"
                    ));
                }
                return Flux.just(new LlmDelta("完成", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan 公开回合", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(stages).contains("agent-plan-draft");
        assertThat(stages).contains("agent-plan-generate");
        assertThat(stageSpecs.get("agent-plan-draft").tools()).isEmpty();
        assertThat(stageSpecs.get("agent-plan-draft").toolChoice()).isEqualTo(ToolChoice.NONE);
        assertThat(stageSpecs.get("agent-plan-draft").reasoningEnabled()).isTrue();
        assertThat(stageSpecs.get("agent-plan-draft").userPrompt()).isNull();
        assertThat(stageSpecs.get("agent-plan-draft").messages().stream().map(message -> message.getText()).toList())
                .containsExactly("测试 plan 公开回合");
        assertThat(stageSpecs.get("agent-plan-generate").tools().stream().map(LlmService.LlmFunctionTool::name).toList())
                .containsExactly("_plan_add_tasks_");
        assertThat(stageSpecs.get("agent-plan-generate").toolChoice()).isEqualTo(ToolChoice.REQUIRED);
        assertThat(stageSpecs.get("agent-plan-generate").reasoningEnabled()).isFalse();
        assertThat(stageSpecs.get("agent-plan-generate").userPrompt()).isNull();
        assertThat(stageSpecs.get("agent-plan-generate").messages().stream().map(message -> message.getText()).toList())
                .contains("测试 plan 公开回合", "先分析约束并输出规划正文")
                .noneMatch(text -> text.contains("本回合必须调用 _plan_add_tasks_"));
        assertThat(deltas.stream().flatMap(delta -> delta.toolCalls().stream()).map(ToolCallDelta::id))
                .contains("call_plan_public");
        assertThat(deltas.stream().map(AgentDelta::content).filter(text -> text != null && !text.isBlank()))
                .contains("先分析约束并输出规划正文");
    }

    @Test
    void shouldInjectBackendPromptIntoPlanExecuteStages() {
        List<String> prompts = new CopyOnWriteArrayList<>();
        Map<String, List<String>> stageTools = new ConcurrentHashMap<>();

        BaseTool promptTool = new BaseTool() {
            @Override
            public String name() {
                return "prompt_tool";
            }

            @Override
            public String description() {
                return "prompt helper";
            }

            @Override
            public String afterCallHint() {
                return "请遵循 prompt_tool 的额外约束";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true));
            }
        };

        AgentDefinition definition = definition(
                "demoPromptInject",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_", "prompt_tool"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_", "prompt_tool"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of("prompt_tool"), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_", "prompt_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                prompts.add(spec.stage() + "::" + spec.systemPrompt());
                stageTools.put(spec.stage(), spec.tools().stream().map(LlmService.LlmFunctionTool::name).toList());
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_prompt", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"pt1\",\"description\":\"任务1\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    String taskId = currentTaskId(spec);
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_update_prompt", "function", "_plan_update_task_",
                                    "{\"taskId\":\"" + taskId + "\",\"status\":\"completed\"}")),
                            "tool_calls"
                    ));
                }
                return Flux.just(new LlmDelta("最终", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanUpdateTask(), promptTool)),
                objectMapper,
                null,
                null
        );

        agent.stream(new AgentRequest("测试 prompt 注入", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-generate")
                && text.contains("工具说明:")
                && text.contains("_plan_add_tasks_: 创建计划任务（追加模式）")
                && text.contains("prompt_tool: prompt helper"))).isTrue();
        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-generate")
                && text.contains("工具调用后推荐指令:"))).isFalse();
        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-execute-step-1")
                && text.contains("工具说明:")
                && text.contains("_plan_update_task_: 更新计划中的任务状态"))).isTrue();
        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-execute-step-1")
                && text.contains("工具调用后推荐指令:")
                && text.contains("prompt_tool: 请遵循 prompt_tool 的额外约束"))).isTrue();
        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-final")
                && text.contains("工具调用后推荐指令:")
                && text.contains("prompt_tool: 请遵循 prompt_tool 的额外约束"))).isTrue();
        assertThat(stageTools.get("agent-plan-generate")).containsExactly("_plan_add_tasks_");
        assertThat(stageTools.get("agent-plan-execute-step-1")).contains("_plan_update_task_");
    }

    @Test
    void shouldLogRunSnapshotWithPolicyStagesAndToolGroups(CapturedOutput output) {
        AgentDefinition definition = definition(
                "demoPlanSnapshot",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(
                        ControlStrategy.PLAN_EXECUTE,
                        OutputPolicy.REASONING_SUMMARY,
                        ToolPolicy.ALLOW,
                        new Budget(20, 10, 6, 180_000)
                ),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", "bailian", "qwen3-max", List.of("_plan_add_tasks_"), true, ComputePolicy.HIGH),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of("_plan_get_tasks_"), false, ComputePolicy.LOW),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_", "_plan_get_tasks_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_1", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"t1\",\"description\":\"任务1\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_update_1", "function", "_plan_update_task_",
                                    "{\"taskId\":\"t1\",\"status\":\"completed\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-step-summary-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("步骤完成", null, "stop"));
                }
                return Flux.just(new LlmDelta("最终回答", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanGetTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        agent.stream(new AgentRequest("测试快照", "chat_demo", "req_demo", "run_demo"))
                .collectList()
                .block(Duration.ofSeconds(3));

        String logs = output.getOut();
        assertThat(logs).contains("run snapshot");
        assertThat(logs).contains("\"runId\" : \"run_demo\"");
        assertThat(logs).contains("\"chatId\" : \"chat_demo\"");
        assertThat(logs).contains("\"requestId\" : \"req_demo\"");
        assertThat(logs).contains("\"message\" : \"测试快照\"");
        assertThat(logs).contains("\"control\" : \"PLAN_EXECUTE\"");
        assertThat(logs).contains("\"output\" : \"REASONING_SUMMARY\"");
        assertThat(logs).contains("\"toolPolicy\" : \"ALLOW\"");
        assertThat(logs).contains("\"maxModelCalls\" : 20");
        assertThat(logs).contains("\"maxToolCalls\" : 10");
        assertThat(logs).contains("\"maxSteps\" : 6");
        assertThat(logs).contains("\"timeoutMs\" : 180000");
        assertThat(logs).contains("\"plan\" : {");
        assertThat(logs).contains("\"execute\" : {");
        assertThat(logs).contains("\"summary\" : {");
        assertThat(logs.split("\"deepThinking\"").length - 1).isEqualTo(1);
        assertThat(logs).contains("\"reasoningEffort\" : \"HIGH\"");
        assertThat(logs).contains("\"backend\" : [");
        assertThat(logs).contains("\"frontend\" : [");
        assertThat(logs).contains("\"action\" : [");
        assertThat(logs).contains("\"_plan_add_tasks_\"");
        assertThat(logs).contains("\"_plan_update_task_\"");
        assertThat(logs).contains("\"_plan_get_tasks_\"");
    }

    @Test
    void planExecuteShouldFollowPlanTaskOrderAndExposePlanGetResult() {
        List<String> executeStages = new CopyOnWriteArrayList<>();
        List<String> executeUserMessages = new CopyOnWriteArrayList<>();

        AgentDefinition definition = definition(
                "demoPlanOrder",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null,
                                List.of("_plan_get_tasks_", "_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_get_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                if (spec.stage().startsWith("agent-plan-execute-step-")) {
                    executeStages.add(spec.stage());
                    if (spec.messages() != null && !spec.messages().isEmpty()) {
                        executeUserMessages.add(spec.messages().get(spec.messages().size() - 1).getText());
                    }
                }
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_1", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"task1\",\"description\":\"任务1\",\"status\":\"init\"},"
                                            + "{\"taskId\":\"task2\",\"description\":\"任务2\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_get_tasks_1", "function", "_plan_get_tasks_", "{}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1-update".equals(spec.stage())) {
                    String taskId = currentTaskId(spec);
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_update_1", "function", "_plan_update_task_",
                                    "{\"taskId\":\"" + taskId + "\",\"status\":\"completed\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-2".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_get_tasks_2", "function", "_plan_get_tasks_", "{}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-2-update".equals(spec.stage())) {
                    String taskId = currentTaskId(spec);
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_update_2", "function", "_plan_update_task_",
                                    "{\"taskId\":\"" + taskId + "\",\"status\":\"completed\"}")),
                            "tool_calls"
                    ));
                }
                return Flux.just(new LlmDelta("最终回答", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanGetTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan 顺序执行", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(executeStages).contains(
                "agent-plan-execute-step-1",
                "agent-plan-execute-step-1-update",
                "agent-plan-execute-step-2",
                "agent-plan-execute-step-2-update"
        );
        assertThat(executeUserMessages).anyMatch(text -> text.contains("这是任务列表："));
        assertThat(executeUserMessages).anyMatch(text -> text.contains("当前要执行的 taskId:"));
        assertThat(deltas.stream().flatMap(delta -> delta.toolResults().stream())
                .filter(result -> "call_plan_get_tasks_1".equals(result.toolId()))
                .map(AgentDelta.ToolResult::result)
                .findFirst()
                .orElse(""))
                .contains("计划ID:")
                .contains("| init | 任务1")
                .contains("当前应执行 taskId:");
    }

    @Test
    void planExecuteShouldSuppressReasoningAndContentAfterToolCallUntilResult() {
        AgentDefinition definition = definition(
                "demoPlanToolGate",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null,
                                List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_1", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"task1\",\"description\":\"任务1\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    return Flux.just(
                            new LlmDelta("工具前内容", null, null),
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta("call_update_1", "function", "_plan_update_task_",
                                            "{\"taskId\":\"task1\"")),
                                    null
                            ),
                            new LlmDelta(
                                    null,
                                    List.of(new ToolCallDelta("call_update_1", null, null,
                                            ",\"status\":\"completed\"}")),
                                    null
                            ),
                            new LlmDelta("工具后推理不应外发", null, null, null),
                            new LlmDelta("工具后内容不应外发", null, "stop")
                    );
                }
                if ("agent-plan-step-summary-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("步骤执行完成", null, "stop"));
                }
                return Flux.just(new LlmDelta("最终回答", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 tool gate", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream()
                .flatMap(delta -> delta.toolResults().stream())
                .map(AgentDelta.ToolResult::toolId))
                .contains("call_update_1");
        assertThat(deltas.stream()
                .map(AgentDelta::reasoning)
                .filter(text -> text != null && !text.isBlank()))
                .doesNotContain("工具后推理不应外发");
        assertThat(deltas.stream()
                .map(AgentDelta::content)
                .filter(text -> text != null && !text.isBlank()))
                .doesNotContain("工具后内容不应外发");
    }

    @Test
    void planExecuteShouldFailWhenPlanStageDoesNotCallPlanAddTasks() {
        List<String> stages = new CopyOnWriteArrayList<>();

        AgentDefinition definition = definition(
                "demoPlanMissingPlanAdd",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stages.add(spec.stage());
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("仅返回计划文本，不调用工具", null, "stop"));
                }
                return Flux.just(new LlmDelta("不应到达这里", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan 未调用 _plan_add_tasks_", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream()
                .map(AgentDelta::content)
                .filter(text -> text != null && !text.isBlank())
                .toList()).anyMatch(text -> text.contains("规划阶段必须调用 _plan_add_tasks_"));
        assertThat(stages).contains("agent-plan-generate");
        assertThat(stages.stream().noneMatch(stage -> stage.startsWith("agent-plan-execute-step-"))).isTrue();
    }

    @Test
    void planExecuteShouldStopWhenSameTaskHasNoProgressTwice() {
        List<String> stages = new CopyOnWriteArrayList<>();

        AgentDefinition definition = definition(
                "demoPlanNoProgress",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stages.add(spec.stage());
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_1", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"task1\",\"description\":\"任务1\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())
                        || "agent-plan-execute-step-1-update".equals(spec.stage())
                        || "agent-plan-execute-step-1-update-repair".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("已执行但未更新状态", null, "stop"));
                }
                return Flux.just(new LlmDelta("不应到达这里", null, "stop"));
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan 卡住中断", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().map(AgentDelta::content).filter(text -> text != null && !text.isBlank()).toList())
                .anyMatch(text -> text.contains("更新任务状态失败 2 次"));
        assertThat(stages).contains("agent-plan-execute-step-1", "agent-plan-execute-step-1-update");
        assertThat(stages.stream().noneMatch(stage -> "agent-plan-final".equals(stage))).isTrue();
    }

    @Test
    void oneshotShouldRetryRequireToolWithoutInjectedRepairPrompt() {
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();
        AgentDefinition definition = definition(
                "demoOneshotRequireRetry",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.REQUIRE, Budget.DEFAULT),
                new OneshotMode(
                        new StageSettings("你是测试助手", null, null, List.of("echo_tool"), false, ComputePolicy.MEDIUM),
                        null, null
                ),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stageSpecs.put(spec.stage(), spec);
                if ("agent-oneshot-tool-first".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("先返回文本但不调工具", null, "stop"));
                }
                if ("agent-oneshot-tool-first-repair".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("第二次仍不调用工具", null, "stop"));
                }
                if ("agent-oneshot-tool-first-repair-2".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_echo_oneshot", "function", "echo_tool", "{\"text\":\"ok\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-oneshot-tool-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("最终回答", null, "stop"));
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
                new ToolRegistry(List.of(echoTool)),
                objectMapper,
                null,
                null
        );

        agent.stream(new AgentRequest("测试 oneshot runtime prompts", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        LlmCallSpec repairSpec = stageSpecs.get("agent-oneshot-tool-first-repair");
        LlmCallSpec repairSpec2 = stageSpecs.get("agent-oneshot-tool-first-repair-2");
        LlmCallSpec finalSpec = stageSpecs.get("agent-oneshot-tool-final");
        assertThat(repairSpec).isNotNull();
        assertThat(repairSpec2).isNotNull();
        assertThat(repairSpec.messages().stream().map(message -> message.getText()))
                .noneMatch(text -> text != null && text.contains("必须调用至少一个工具"));
        assertThat(finalSpec).isNotNull();
        assertThat(finalSpec.userPrompt()).isNull();
    }

    @Test
    void reactShouldRetryWithoutInjectedRepairPrompt() {
        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();
        AgentDefinition definition = definition(
                "demoReactRequireRetry",
                AgentRuntimeMode.REACT,
                new RunSpec(ControlStrategy.REACT_LOOP, OutputPolicy.PLAIN, ToolPolicy.REQUIRE, new Budget(20, 20, 2, 60_000)),
                new ReactMode(
                        new StageSettings("你是测试助手", null, null, List.of("echo_tool"), false, ComputePolicy.MEDIUM),
                        2, null, null
                ),
                List.of("echo_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stageSpecs.put(spec.stage(), spec);
                if ("agent-react-step-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("第一步不调工具", null, "stop"));
                }
                if ("agent-react-step-1-retry-1".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_echo_react", "function", "echo_tool", "{\"text\":\"ok\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-react-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("forced-final", null, "stop"));
                }
                if (spec.stage() != null && spec.stage().startsWith("agent-react-step-")) {
                    return Flux.just(new LlmDelta("不调用工具", null, "stop"));
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
                new ToolRegistry(List.of(echoTool)),
                objectMapper,
                null,
                null
        );

        agent.stream(new AgentRequest("测试 react runtime prompts", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(stageSpecs).containsKeys("agent-react-step-1", "agent-react-step-1-retry-1", "agent-react-final");
        LlmCallSpec retrySpec = stageSpecs.get("agent-react-step-2-retry-1");
        assertThat(retrySpec).isNotNull();
        assertThat(retrySpec.messages().stream().map(message -> message.getText()))
                .noneMatch(text -> text != null && text.contains("必须调用至少一个工具"));
    }

    @Test
    void planExecuteShouldUseRuntimeTaskTemplateAndSkipVerifyStage() {
        String taskExecutionPromptTemplate = """
                RUNTIME_TASK_PROMPT
                当前要执行的 taskId: {{task_id}}
                当前任务描述: {{task_description}}
                """;

        Map<String, LlmCallSpec> stageSpecs = new ConcurrentHashMap<>();
        AtomicReference<String> verifyStage = new AtomicReference<>();
        AgentDefinition definition = definition(
                "demoPlanRuntimePrompts",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM),
                        null, null, taskExecutionPromptTemplate
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stageSpecs.put(spec.stage(), spec);
                if ("agent-plan-generate".equals(spec.stage())) {
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_plan_runtime", "function", "_plan_add_tasks_",
                                    "{\"tasks\":[{\"taskId\":\"rt1\",\"description\":\"任务A\",\"status\":\"init\"}]}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-execute-step-1".equals(spec.stage())) {
                    String taskId = currentTaskId(spec);
                    return Flux.just(new LlmDelta(
                            null,
                            List.of(new ToolCallDelta("call_update_runtime", "function", "_plan_update_task_",
                                    "{\"taskId\":\"" + taskId + "\",\"status\":\"completed\"}")),
                            "tool_calls"
                    ));
                }
                if ("agent-plan-final".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("候选总结", null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            public Flux<String> streamContent(LlmCallSpec spec) {
                verifyStage.set(spec.stage());
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of(new SystemPlanAddTasks(), new SystemPlanUpdateTask())),
                objectMapper,
                null,
                null
        );

        agent.stream(new AgentRequest("测试 plan runtime prompts", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        LlmCallSpec executeSpec = stageSpecs.get("agent-plan-execute-step-1");
        assertThat(executeSpec).isNotNull();
        assertThat(executeSpec.messages().stream().map(message -> message.getText()))
                .anyMatch(text -> text != null && text.contains("RUNTIME_TASK_PROMPT") && text.contains("当前任务描述: 任务A"));
        assertThat(verifyStage.get()).isNull();
    }

    @Test
    void oneshotShouldUseSinglePassOutputWithoutVerifyStage() {
        AgentDefinition definition = definition(
                "demoOneshotSinglePass",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.DISALLOW, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of(), false, ComputePolicy.MEDIUM), null, null),
                List.of()
        );

        AtomicReference<String> streamContentStage = new AtomicReference<>();
        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-oneshot".equals(stage)) {
                    return Flux.just(new LlmDelta("单轮答案", null, "stop"));
                }
                return Flux.empty();
            }

            @Override
            protected Flux<String> contentByStage(String stage) {
                streamContentStage.set(stage);
                return Flux.empty();
            }
        };

        DefinitionDrivenAgent agent = new DefinitionDrivenAgent(
                definition,
                llmService,
                new ToolRegistry(List.of()),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试二次校验", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        List<String> contentDeltas = deltas.stream()
                .map(AgentDelta::content)
                .filter(value -> value != null && !value.isBlank())
                .toList();
        assertThat(contentDeltas).contains("单轮答案");
        assertThat(streamContentStage.get()).isNull();
    }

    private SkillRegistryService createSkillRegistry(String promptText) throws Exception {
        Path skillsRoot = Files.createTempDirectory("skills-registry");
        Path skillFile = skillsRoot.resolve("screenshot").resolve("SKILL.md");
        Files.createDirectories(skillFile.getParent());
        Files.writeString(skillFile, """
                ---
                name: "screenshot"
                description: "capture screenshots"
                ---
                # Skill Prompt
                %s
                """.formatted(promptText));

        SkillCatalogProperties skillProperties = new SkillCatalogProperties();
        skillProperties.setExternalDir(skillsRoot.toString());
        skillProperties.setMaxPromptChars(1000);
        return new SkillRegistryService(skillProperties, null);
    }

    private BaseTool skillScriptRunTool() {
        return new BaseTool() {
            @Override
            public String name() {
                return "_skill_run_script_";
            }

            @Override
            public String description() {
                return "run skill scripts";
            }

            @Override
            public JsonNode invoke(Map<String, Object> args) {
                return objectMapper.valueToTree(Map.of("ok", true, "stdout", "ok"));
            }
        };
    }

    private AgentDefinition definition(
            String id,
            AgentRuntimeMode mode,
            RunSpec runSpec,
            com.linlay.springaiagw.agent.mode.AgentMode agentMode,
            List<String> tools
    ) {
        return new AgentDefinition(
                id,
                id,
                null,
                "demo",
                "bailian",
                "qwen3-max",
                mode,
                runSpec,
                agentMode,
                tools,
                List.of()
        );
    }

    private String currentTaskId(LlmCallSpec spec) {
        if (spec == null || spec.messages() == null || spec.messages().isEmpty()) {
            return "unknown";
        }
        for (int i = spec.messages().size() - 1; i >= 0; i--) {
            String text = spec.messages().get(i).getText();
            if (text == null || text.isBlank()) {
                continue;
            }
            Matcher matcher = CURRENT_TASK_ID_PATTERN.matcher(text);
            if (matcher.find()) {
                return matcher.group(1).trim();
            }
        }
        return "unknown";
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
