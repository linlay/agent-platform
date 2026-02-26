package com.linlay.agentplatform.service;

import com.linlay.agentplatform.stream.model.StreamEvent;
import com.linlay.agentplatform.stream.model.StreamInput;
import com.linlay.agentplatform.stream.model.StreamRequest;
import com.linlay.agentplatform.stream.model.ToolCallDelta;
import com.linlay.agentplatform.stream.service.StreamEventAssembler;
import com.linlay.agentplatform.model.AgentDelta;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;
import java.util.Map;

import static org.assertj.core.api.Assertions.assertThat;

class AgentDeltaToStreamInputMapperTest {

    @Test
    void shouldEmitActionEndBeforeActionResult() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "action_1",
                        "action",
                        "switch_theme",
                        "{\"theme\":\"dark\"}"
                ))),
                AgentDelta.toolResult("action_1", "OK")
        ));

        int actionStart = indexOfActionEvent(events, "action.start", "action_1");
        int actionArgs = indexOfActionEvent(events, "action.args", "action_1");
        int actionEnd = indexOfActionEvent(events, "action.end", "action_1");
        int actionResult = indexOfActionEvent(events, "action.result", "action_1");

        assertThat(actionStart).isGreaterThanOrEqualTo(0);
        assertThat(actionArgs).isGreaterThan(actionStart);
        assertThat(actionEnd).isGreaterThan(actionArgs);
        assertThat(actionResult).isGreaterThan(actionEnd);
        assertThat(countActionEvent(events, "action.end", "action_1")).isEqualTo(1);
    }

    @Test
    void shouldKeepToolEndBeforeToolResult() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "tool_1",
                        "function",
                        "bash",
                        "{\"command\":\"ls\"}"
                ))),
                AgentDelta.toolResult("tool_1", "{\"ok\":true}")
        ));

        int toolStart = indexOfToolEvent(events, "tool.start", "tool_1");
        int toolArgs = indexOfToolEvent(events, "tool.args", "tool_1");
        int toolEnd = indexOfToolEvent(events, "tool.end", "tool_1");
        int toolResult = indexOfToolEvent(events, "tool.result", "tool_1");

        assertThat(toolStart).isGreaterThanOrEqualTo(0);
        assertThat(toolArgs).isGreaterThan(toolStart);
        assertThat(toolEnd).isGreaterThan(toolArgs);
        assertThat(toolResult).isGreaterThan(toolEnd);
        assertThat(countToolEvent(events, "tool.end", "tool_1")).isEqualTo(1);
    }

    @Test
    void shouldNotEmitDuplicateToolEndWhenExplicitToolEndProvided() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "tool_1",
                        "function",
                        "bash",
                        "{\"command\":\"ls\"}"
                ))),
                AgentDelta.toolEnd("tool_1"),
                AgentDelta.toolResult("tool_1", "{\"ok\":true}")
        ));

        int toolStart = indexOfToolEvent(events, "tool.start", "tool_1");
        int toolArgs = indexOfToolEvent(events, "tool.args", "tool_1");
        int toolEnd = indexOfToolEvent(events, "tool.end", "tool_1");
        int toolResult = indexOfToolEvent(events, "tool.result", "tool_1");

        assertThat(toolStart).isGreaterThanOrEqualTo(0);
        assertThat(toolArgs).isGreaterThan(toolStart);
        assertThat(toolEnd).isGreaterThan(toolArgs);
        assertThat(toolResult).isGreaterThan(toolEnd);
        assertThat(countToolEvent(events, "tool.end", "tool_1")).isEqualTo(1);
    }

    @Test
    void shouldPreserveToolArgsChunkOrderWithoutMerging() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "tool_chunk_1",
                        "function",
                        "bash",
                        "{\"command\":\"ec"
                ))),
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "tool_chunk_1",
                        "function",
                        null,
                        "ho hi\"}"
                ))),
                AgentDelta.toolResult("tool_chunk_1", "{\"ok\":true}")
        ));

        List<StreamEvent> toolArgsEvents = events.stream()
                .filter(event -> "tool.args".equals(event.type()))
                .filter(event -> "tool_chunk_1".equals(String.valueOf(event.payload().get("toolId"))))
                .toList();

        assertThat(toolArgsEvents).hasSize(2);
        assertThat(String.valueOf(toolArgsEvents.get(0).payload().get("delta"))).isEqualTo("{\"command\":\"ec");
        assertThat(String.valueOf(toolArgsEvents.get(1).payload().get("delta"))).isEqualTo("ho hi\"}");
        assertThat(Integer.parseInt(String.valueOf(toolArgsEvents.get(0).payload().get("chunkIndex")))).isEqualTo(0);
        assertThat(Integer.parseInt(String.valueOf(toolArgsEvents.get(1).payload().get("chunkIndex")))).isEqualTo(1);
    }

    @Test
    void shouldAssignUniqueReasoningIdsAcrossBlocks() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.reasoning("reasoning-1"),
                AgentDelta.stageMarker("stage-1"),
                AgentDelta.reasoning("reasoning-2")
        ));

        List<String> reasoningStartIds = payloadValues(events, "reasoning.start", "reasoningId");
        assertThat(reasoningStartIds).containsExactly("run_1_r_1", "run_1_r_2");
    }

    @Test
    void shouldAssignUniqueContentIdsAcrossMultipleSegments() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.content("content-1"),
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "tool_1",
                        "function",
                        "bash",
                        "{\"command\":\"pwd\"}"
                ))),
                AgentDelta.toolResult("tool_1", "{\"ok\":true}"),
                AgentDelta.content("content-2")
        ));

        List<String> contentStartIds = payloadValues(events, "content.start", "contentId");
        assertThat(contentStartIds).containsExactly("run_1_c_1", "run_1_c_2");
    }

    @Test
    void shouldRotateReasoningIdAfterToolBoundary() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.reasoning("before-tool"),
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "tool_1",
                        "function",
                        "bash",
                        "{\"command\":\"ls\"}"
                ))),
                AgentDelta.toolResult("tool_1", "{\"ok\":true}"),
                AgentDelta.reasoning("after-tool")
        ));

        List<String> reasoningStartIds = payloadValues(events, "reasoning.start", "reasoningId");
        assertThat(reasoningStartIds).containsExactly("run_1_r_1", "run_1_r_2");
    }

    @Test
    void shouldUseNewContentIdAfterStageMarker() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.content("before-stage"),
                AgentDelta.stageMarker("summary"),
                AgentDelta.content("after-stage")
        ));

        List<String> contentStartIds = payloadValues(events, "content.start", "contentId");
        assertThat(contentStartIds).containsExactly("run_1_c_1", "run_1_c_2");
    }

    @Test
    void shouldMapPlanUpdateWithSinglePlanEventType() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.planUpdate(
                        "plan_demo_001",
                        "chat_1",
                        List.of(
                                new AgentDelta.PlanTask("task0", "收集信息", "init"),
                                new AgentDelta.PlanTask("task1", "执行任务", "in_progress")
                        )
                )
        ));

        List<StreamEvent> planEvents = events.stream()
                .filter(event -> event.type() != null && event.type().startsWith("plan."))
                .toList();

        assertThat(planEvents).hasSize(1);
        assertThat(planEvents.getFirst().type()).isEqualTo("plan.update");
        assertThat(planEvents.getFirst().payload().get("planId")).isEqualTo("plan_demo_001");
        assertThat(planEvents.getFirst().payload().get("chatId")).isEqualTo("chat_1");
        assertThat(planEvents.getFirst().payload().get("plan")).isInstanceOf(List.class);
    }

    @Test
    void shouldMapTaskLifecycleAndBindTaskIdToLeafEvents() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.planUpdate(
                        "plan_1",
                        "chat_1",
                        List.of(new AgentDelta.PlanTask("task_1", "执行任务", "init"))
                ),
                AgentDelta.taskStart("task_1", "run_1", "task_1", "执行任务"),
                AgentDelta.reasoning("先分析", "task_1"),
                AgentDelta.content("再输出", "task_1"),
                AgentDelta.toolCalls(List.of(new ToolCallDelta(
                        "tool_1",
                        "function",
                        "bash",
                        "{\"command\":\"pwd\"}"
                )), "task_1"),
                AgentDelta.toolResult("tool_1", "{\"ok\":true}"),
                AgentDelta.taskComplete("task_1")
        ));

        int taskStart = indexOfEvent(events, "task.start", "taskId", "task_1");
        int taskComplete = indexOfEvent(events, "task.complete", "taskId", "task_1");
        int reasoningStart = indexOfEvent(events, "reasoning.start", "taskId", "task_1");
        int contentStart = indexOfEvent(events, "content.start", "taskId", "task_1");
        int toolStart = indexOfEvent(events, "tool.start", "taskId", "task_1");

        assertThat(taskStart).isGreaterThanOrEqualTo(0);
        assertThat(reasoningStart).isGreaterThan(taskStart);
        assertThat(contentStart).isGreaterThan(taskStart);
        assertThat(toolStart).isGreaterThan(taskStart);
        assertThat(taskComplete).isGreaterThan(toolStart);
    }

    @Test
    void shouldMapTaskFailEventWithErrorPayload() {
        AgentDeltaToStreamInputMapper mapper = new AgentDeltaToStreamInputMapper("run_1");
        List<StreamEvent> events = assembleEvents(mapper, List.of(
                AgentDelta.planUpdate(
                        "plan_1",
                        "chat_1",
                        List.of(new AgentDelta.PlanTask("task_1", "执行任务", "init"))
                ),
                AgentDelta.taskStart("task_1", "run_1", "task_1", "执行任务"),
                AgentDelta.taskFail("task_1", Map.of("code", "task_execution_error", "message", "boom"))
        ));

        int failIndex = indexOfEvent(events, "task.fail", "taskId", "task_1");
        assertThat(failIndex).isGreaterThanOrEqualTo(0);
        StreamEvent failEvent = events.get(failIndex);
        assertThat(failEvent.payload().get("error")).isInstanceOf(Map.class);
        Map<?, ?> error = (Map<?, ?>) failEvent.payload().get("error");
        assertThat(error.get("code")).isEqualTo("task_execution_error");
        assertThat(error.get("message")).isEqualTo("boom");
    }

    private List<StreamEvent> assembleEvents(AgentDeltaToStreamInputMapper mapper, List<AgentDelta> deltas) {
        StreamEventAssembler.EventStreamState state = new StreamEventAssembler()
                .begin(new StreamRequest.Query(
                        "req_1",
                        "chat_1",
                        "user",
                        "test",
                        null,
                        null,
                        null,
                        null,
                        true,
                        "chat_1",
                        "run_1"
                ));
        List<StreamEvent> events = new ArrayList<>(state.bootstrapEvents());
        for (AgentDelta delta : deltas) {
            for (StreamInput input : mapper.mapOrEmpty(delta)) {
                events.addAll(state.consume(input));
            }
        }
        events.addAll(state.complete());
        return events;
    }

    private int indexOfActionEvent(List<StreamEvent> events, String type, String actionId) {
        for (int i = 0; i < events.size(); i++) {
            StreamEvent event = events.get(i);
            if (!type.equals(event.type())) {
                continue;
            }
            Object value = event.payload().get("actionId");
            if (actionId.equals(String.valueOf(value))) {
                return i;
            }
        }
        return -1;
    }

    private int indexOfToolEvent(List<StreamEvent> events, String type, String toolId) {
        for (int i = 0; i < events.size(); i++) {
            StreamEvent event = events.get(i);
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

    private long countActionEvent(List<StreamEvent> events, String type, String actionId) {
        return events.stream()
                .filter(event -> type.equals(event.type()))
                .filter(event -> actionId.equals(String.valueOf(event.payload().get("actionId"))))
                .count();
    }

    private long countToolEvent(List<StreamEvent> events, String type, String toolId) {
        return events.stream()
                .filter(event -> type.equals(event.type()))
                .filter(event -> toolId.equals(String.valueOf(event.payload().get("toolId"))))
                .count();
    }

    private List<String> payloadValues(List<StreamEvent> events, String type, String payloadKey) {
        return events.stream()
                .filter(event -> type.equals(event.type()))
                .map(event -> String.valueOf(event.payload().get(payloadKey)))
                .toList();
    }

    private int indexOfEvent(List<StreamEvent> events, String type, String payloadKey, String payloadValue) {
        for (int i = 0; i < events.size(); i++) {
            StreamEvent event = events.get(i);
            if (!type.equals(event.type())) {
                continue;
            }
            Object value = event.payload().get(payloadKey);
            if (payloadValue.equals(String.valueOf(value))) {
                return i;
            }
        }
        return -1;
    }
}
