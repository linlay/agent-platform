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
import com.linlay.springaiagw.agent.runtime.policy.ToolPolicy;
import com.linlay.springaiagw.agent.runtime.policy.VerifyPolicy;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.stream.AgentDelta;
import com.linlay.springaiagw.service.DeltaStreamService;
import com.linlay.springaiagw.service.LlmCallSpec;
import com.linlay.springaiagw.service.LlmService;
import com.linlay.springaiagw.tool.BaseTool;
import com.linlay.springaiagw.tool.PlanCreateTool;
import com.linlay.springaiagw.tool.PlanGetTool;
import com.linlay.springaiagw.tool.PlanTaskUpdateTool;
import com.linlay.springaiagw.tool.ToolRegistry;
import org.junit.jupiter.api.Test;
import org.junit.jupiter.api.extension.ExtendWith;
import org.springframework.boot.test.system.CapturedOutput;
import org.springframework.boot.test.system.OutputCaptureExtension;
import reactor.core.publisher.Flux;

import java.time.Duration;
import java.util.List;
import java.util.Map;
import java.util.concurrent.CopyOnWriteArrayList;
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
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of("echo_tool"), false, ComputePolicy.MEDIUM)),
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
                new DeltaStreamService(),
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
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.DISALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of(), false, ComputePolicy.MEDIUM)),
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
                new DeltaStreamService(),
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
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.DISALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of(), true, ComputePolicy.HIGH)),
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
                new DeltaStreamService(),
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
    void reactShouldContinueUntilToolThenFinalAnswer() {
        AgentDefinition definition = definition(
                "demoReact",
                AgentRuntimeMode.REACT,
                new RunSpec(ControlStrategy.REACT_LOOP, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, new Budget(10, 10, 4, 60_000)),
                new ReactMode(new StageSettings("你是测试助手", null, null, List.of("echo_tool"), true, ComputePolicy.MEDIUM), 6),
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
                new DeltaStreamService(),
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
    void planExecuteShouldUseStageSystemPrompts() {
        List<String> captured = new CopyOnWriteArrayList<>();

        AgentDefinition definition = definition(
                "demoPlan",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM)
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                captured.add(spec.stage() + "::" + spec.systemPrompt());
                if ("agent-plan-draft".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("先规划", null, "stop"));
                }
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
                new DeltaStreamService(),
                new ToolRegistry(List.of(new PlanCreateTool(), new PlanTaskUpdateTool())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan execute", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-draft::规划系统提示"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-generate::规划系统提示"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-execute-step-1::执行系统提示"))).isTrue();
        assertThat(captured.stream().anyMatch(item -> item.contains("agent-plan-final::总结系统提示"))).isTrue();
    }

    @Test
    void planExecutePlanRoundsShouldExposeDraftContentAndPlanToolDelta() {
        AgentDefinition definition = definition(
                "demoPlanPublicTurns",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), true, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM)
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                if ("agent-plan-draft".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("公开计划草稿", null, "stop"));
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
                new DeltaStreamService(),
                new ToolRegistry(List.of(new PlanCreateTool(), new PlanTaskUpdateTool())),
                objectMapper,
                null,
                null
        );

        List<AgentDelta> deltas = agent.stream(new AgentRequest("测试 plan 公开回合", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(deltas).isNotNull();
        assertThat(deltas.stream().map(AgentDelta::content).filter(text -> text != null && !text.isBlank()))
                .contains("公开计划草稿");
        assertThat(deltas.stream().flatMap(delta -> delta.toolCalls().stream()).map(ToolCallDelta::id))
                .contains("call_plan_public");
    }

    @Test
    void shouldInjectBackendPromptIntoPlanExecuteStages() {
        List<String> prompts = new CopyOnWriteArrayList<>();

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
            public String prompt() {
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
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_", "prompt_tool"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_", "prompt_tool"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of("prompt_tool"), false, ComputePolicy.MEDIUM)
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_", "prompt_tool")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                prompts.add(spec.stage() + "::" + spec.systemPrompt());
                if ("agent-plan-draft".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("草稿", null, "stop"));
                }
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
                new DeltaStreamService(),
                new ToolRegistry(List.of(new PlanCreateTool(), new PlanTaskUpdateTool(), promptTool)),
                objectMapper,
                null,
                null
        );

        agent.stream(new AgentRequest("测试 prompt 注入", null, null, null))
                .collectList()
                .block(Duration.ofSeconds(3));

        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-draft")
                && text.contains("工具补充指令:")
                && text.contains("prompt_tool: 请遵循 prompt_tool 的额外约束"))).isTrue();
        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-execute-step-1")
                && text.contains("prompt_tool: 请遵循 prompt_tool 的额外约束"))).isTrue();
        assertThat(prompts.stream().anyMatch(text -> text.contains("agent-plan-final")
                && text.contains("prompt_tool: 请遵循 prompt_tool 的额外约束"))).isTrue();
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
                        VerifyPolicy.NONE,
                        new Budget(20, 10, 6, 180_000)
                ),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", "bailian", "qwen3-max", List.of("_plan_add_tasks_"), true, ComputePolicy.HIGH),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of("_plan_get_tasks_"), false, ComputePolicy.LOW)
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
                new DeltaStreamService(),
                new ToolRegistry(List.of(new PlanCreateTool(), new PlanGetTool(), new PlanTaskUpdateTool())),
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
        assertThat(logs).contains("\"verify\" : \"NONE\"");
        assertThat(logs).contains("\"maxModelCalls\" : 20");
        assertThat(logs).contains("\"maxToolCalls\" : 10");
        assertThat(logs).contains("\"maxSteps\" : 6");
        assertThat(logs).contains("\"timeoutMs\" : 180000");
        assertThat(logs).contains("\"plan\" : {");
        assertThat(logs).contains("\"execute\" : {");
        assertThat(logs).contains("\"summary\" : {");
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
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null,
                                List.of("_plan_get_tasks_", "_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM)
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
                if ("agent-plan-draft".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("计划草稿", null, "stop"));
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
                new DeltaStreamService(),
                new ToolRegistry(List.of(new PlanCreateTool(), new PlanGetTool(), new PlanTaskUpdateTool())),
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
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null,
                                List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM)
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
                new DeltaStreamService(),
                new ToolRegistry(List.of(new PlanCreateTool(), new PlanTaskUpdateTool())),
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
    void planExecuteShouldStopWhenSameTaskHasNoProgressTwice() {
        List<String> stages = new CopyOnWriteArrayList<>();

        AgentDefinition definition = definition(
                "demoPlanNoProgress",
                AgentRuntimeMode.PLAN_EXECUTE,
                new RunSpec(ControlStrategy.PLAN_EXECUTE, OutputPolicy.PLAIN, ToolPolicy.ALLOW, VerifyPolicy.NONE, Budget.DEFAULT),
                new PlanExecuteMode(
                        new StageSettings("规划系统提示", null, null, List.of("_plan_add_tasks_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("执行系统提示", null, null, List.of("_plan_update_task_"), false, ComputePolicy.MEDIUM),
                        new StageSettings("总结系统提示", null, null, List.of(), false, ComputePolicy.MEDIUM)
                ),
                List.of("_plan_add_tasks_", "_plan_update_task_")
        );

        LlmService llmService = new StubLlmService() {
            @Override
            public Flux<LlmDelta> streamDeltas(LlmCallSpec spec) {
                stages.add(spec.stage());
                if ("agent-plan-draft".equals(spec.stage())) {
                    return Flux.just(new LlmDelta("计划草稿", null, "stop"));
                }
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
                new DeltaStreamService(),
                new ToolRegistry(List.of(new PlanCreateTool(), new PlanTaskUpdateTool())),
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
    void secondPassFixShouldOnlyExposeVerifyStreamOutput() {
        AgentDefinition definition = definition(
                "demoVerifySecondPass",
                AgentRuntimeMode.ONESHOT,
                new RunSpec(ControlStrategy.ONESHOT, OutputPolicy.PLAIN, ToolPolicy.DISALLOW, VerifyPolicy.SECOND_PASS_FIX, Budget.DEFAULT),
                new OneshotMode(new StageSettings("你是测试助手", null, null, List.of(), false, ComputePolicy.MEDIUM)),
                List.of()
        );

        LlmService llmService = new StubLlmService() {
            @Override
            protected Flux<LlmDelta> deltaByStage(String stage) {
                if ("agent-oneshot".equals(stage)) {
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
        assertThat(contentDeltas).containsExactly("修", "复");
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
                tools
        );
    }

    private String currentTaskId(LlmCallSpec spec) {
        if (spec == null || spec.messages() == null || spec.messages().isEmpty()) {
            return "unknown";
        }
        String text = spec.messages().get(spec.messages().size() - 1).getText();
        if (text == null || text.isBlank()) {
            return "unknown";
        }
        Matcher matcher = CURRENT_TASK_ID_PATTERN.matcher(text);
        if (!matcher.find()) {
            return "unknown";
        }
        return matcher.group(1).trim();
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
