package com.linlay.springaiagw.service;

import com.aiagent.agw.sdk.model.AgwEvent;
import com.aiagent.agw.sdk.model.AgwInput;
import com.aiagent.agw.sdk.model.AgwRequest;
import com.aiagent.agw.sdk.model.ToolCallDelta;
import com.aiagent.agw.sdk.service.AgwEventAssembler;
import com.linlay.springaiagw.model.agw.AgentDelta;
import org.junit.jupiter.api.Test;

import java.util.ArrayList;
import java.util.List;

import static org.assertj.core.api.Assertions.assertThat;

class AgentDeltaToAgwInputMapperTest {

    @Test
    void shouldEmitActionEndBeforeActionResult() {
        AgentDeltaToAgwInputMapper mapper = new AgentDeltaToAgwInputMapper("run_1");
        List<AgwEvent> events = assembleEvents(mapper, List.of(
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
        AgentDeltaToAgwInputMapper mapper = new AgentDeltaToAgwInputMapper("run_1");
        List<AgwEvent> events = assembleEvents(mapper, List.of(
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
    void shouldPreserveToolArgsChunkOrderWithoutMerging() {
        AgentDeltaToAgwInputMapper mapper = new AgentDeltaToAgwInputMapper("run_1");
        List<AgwEvent> events = assembleEvents(mapper, List.of(
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

        List<AgwEvent> toolArgsEvents = events.stream()
                .filter(event -> "tool.args".equals(event.type()))
                .filter(event -> "tool_chunk_1".equals(String.valueOf(event.payload().get("toolId"))))
                .toList();

        assertThat(toolArgsEvents).hasSize(2);
        assertThat(String.valueOf(toolArgsEvents.get(0).payload().get("delta"))).isEqualTo("{\"command\":\"ec");
        assertThat(String.valueOf(toolArgsEvents.get(1).payload().get("delta"))).isEqualTo("ho hi\"}");
        assertThat(Integer.parseInt(String.valueOf(toolArgsEvents.get(0).payload().get("chunkIndex")))).isEqualTo(0);
        assertThat(Integer.parseInt(String.valueOf(toolArgsEvents.get(1).payload().get("chunkIndex")))).isEqualTo(1);
    }

    private List<AgwEvent> assembleEvents(AgentDeltaToAgwInputMapper mapper, List<AgentDelta> deltas) {
        AgwEventAssembler.EventStreamState state = new AgwEventAssembler()
                .begin(new AgwRequest.Query(
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
        List<AgwEvent> events = new ArrayList<>(state.bootstrapEvents());
        for (AgentDelta delta : deltas) {
            for (AgwInput input : mapper.mapOrEmpty(delta)) {
                events.addAll(state.consume(input));
            }
        }
        events.addAll(state.complete());
        return events;
    }

    private int indexOfActionEvent(List<AgwEvent> events, String type, String actionId) {
        for (int i = 0; i < events.size(); i++) {
            AgwEvent event = events.get(i);
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

    private long countActionEvent(List<AgwEvent> events, String type, String actionId) {
        return events.stream()
                .filter(event -> type.equals(event.type()))
                .filter(event -> actionId.equals(String.valueOf(event.payload().get("actionId"))))
                .count();
    }

    private long countToolEvent(List<AgwEvent> events, String type, String toolId) {
        return events.stream()
                .filter(event -> type.equals(event.type()))
                .filter(event -> toolId.equals(String.valueOf(event.payload().get("toolId"))))
                .count();
    }
}
