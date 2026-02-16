package com.linlay.springaiagw.tool;

import com.fasterxml.jackson.databind.JsonNode;
import org.junit.jupiter.api.Test;

import java.util.List;
import java.util.Map;
import java.util.regex.Pattern;

import static org.assertj.core.api.Assertions.assertThat;

class PlanToolsContractTest {

    private static final Pattern TASK_LINE_PATTERN = Pattern.compile("^[a-f0-9]{8}\\s\\|\\s(init|completed|failed|canceled)\\s\\|\\s.+$");

    @Test
    void planAddTasksShouldReturnTaskLines() {
        SystemPlanAddTasks tool = new SystemPlanAddTasks();
        JsonNode result = tool.invoke(Map.of(
                "tasks",
                List.of(
                        Map.of("description", "任务一", "status", "init"),
                        Map.of("description", "任务二", "status", "completed")
                )
        ));

        assertThat(result.isTextual()).isTrue();
        String text = result.asText();
        assertThat(text).doesNotStartWith("失败:");
        String[] lines = text.split("\n");
        assertThat(lines).hasSize(2);
        assertThat(lines[0]).matches(TASK_LINE_PATTERN);
        assertThat(lines[1]).matches(TASK_LINE_PATTERN);
    }

    @Test
    void planAddTasksShouldRejectInvalidStatus() {
        SystemPlanAddTasks tool = new SystemPlanAddTasks();
        JsonNode result = tool.invoke(Map.of(
                "tasks",
                List.of(Map.of("description", "任务一", "status", "done"))
        ));

        assertThat(result.isTextual()).isTrue();
        assertThat(result.asText()).startsWith("失败:");
    }

    @Test
    void planUpdateTaskShouldReturnOkOrFailureText() {
        SystemPlanUpdateTask tool = new SystemPlanUpdateTask();

        JsonNode success = tool.invoke(Map.of("taskId", "abc12345", "status", "completed"));
        assertThat(success.isTextual()).isTrue();
        assertThat(success.asText()).isEqualTo("OK");

        JsonNode missingTask = tool.invoke(Map.of("status", "completed"));
        assertThat(missingTask.isTextual()).isTrue();
        assertThat(missingTask.asText()).startsWith("失败:");

        JsonNode invalidStatus = tool.invoke(Map.of("taskId", "abc12345", "status", "done"));
        assertThat(invalidStatus.isTextual()).isTrue();
        assertThat(invalidStatus.asText()).startsWith("失败:");
    }
}

