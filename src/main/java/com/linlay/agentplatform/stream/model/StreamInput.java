package com.linlay.agentplatform.stream.model;

import java.util.Map;

public sealed interface StreamInput permits
        StreamInput.PlanCreate,
        StreamInput.PlanUpdate,
        StreamInput.TaskStart,
        StreamInput.TaskComplete,
        StreamInput.TaskCancel,
        StreamInput.TaskFail,
        StreamInput.ReasoningDelta,
        StreamInput.ReasoningSnapshot,
        StreamInput.ContentDelta,
        StreamInput.ContentSnapshot,
        StreamInput.ToolArgs,
        StreamInput.ToolEnd,
        StreamInput.ToolResult,
        StreamInput.ToolSnapshot,
        StreamInput.ActionArgs,
        StreamInput.ActionEnd,
        StreamInput.ActionParam,
        StreamInput.ActionResult,
        StreamInput.ActionSnapshot,
        StreamInput.SourceSnapshot,
        StreamInput.RunComplete,
        StreamInput.RunCancel {

    record PlanCreate(String planId, String chatId, Object plan) implements StreamInput {
        public PlanCreate {
            requireNonBlank(planId, "planId");
            requireNonBlank(chatId, "chatId");
            requireNonNull(plan, "plan");
        }
    }

    record PlanUpdate(String planId, Object plan, String chatId) implements StreamInput {
        public PlanUpdate {
            requireNonBlank(planId, "planId");
            requireNonNull(plan, "plan");
        }
    }

    record TaskStart(String taskId, String runId, String taskName, String description) implements StreamInput {
        public TaskStart {
            requireNonBlank(taskId, "taskId");
            requireNonBlank(runId, "runId");
        }
    }

    record TaskComplete(String taskId) implements StreamInput {
        public TaskComplete {
            requireNonBlank(taskId, "taskId");
        }
    }

    record TaskCancel(String taskId) implements StreamInput {
        public TaskCancel {
            requireNonBlank(taskId, "taskId");
        }
    }

    record TaskFail(String taskId, Map<String, Object> error) implements StreamInput {
        public TaskFail {
            requireNonBlank(taskId, "taskId");
            requireNonNull(error, "error");
        }
    }

    record ReasoningDelta(String reasoningId, String delta, String taskId) implements StreamInput {
        public ReasoningDelta {
            requireNonBlank(reasoningId, "reasoningId");
            requireNonNull(delta, "delta");
        }
    }

    record ReasoningSnapshot(String reasoningId, String text, String taskId) implements StreamInput {
        public ReasoningSnapshot {
            requireNonBlank(reasoningId, "reasoningId");
            requireNonNull(text, "text");
        }
    }

    record ContentDelta(String contentId, String delta, String taskId) implements StreamInput {
        public ContentDelta {
            requireNonBlank(contentId, "contentId");
            requireNonNull(delta, "delta");
        }
    }

    record ContentSnapshot(String contentId, String text, String taskId) implements StreamInput {
        public ContentSnapshot {
            requireNonBlank(contentId, "contentId");
            requireNonNull(text, "text");
        }
    }

    record ToolArgs(
            String toolId,
            String delta,
            String taskId,
            String toolName,
            String toolType,
            String toolApi,
            Object toolParams,
            String description,
            Integer chunkIndex
    ) implements StreamInput {
        public ToolArgs {
            requireNonBlank(toolId, "toolId");
            requireNonNull(delta, "delta");
        }
    }

    record ToolEnd(String toolId) implements StreamInput {
        public ToolEnd {
            requireNonBlank(toolId, "toolId");
        }
    }

    record ToolResult(String toolId, String result) implements StreamInput {
        public ToolResult {
            requireNonBlank(toolId, "toolId");
            requireNonNull(result, "result");
        }
    }

    record ToolSnapshot(
            String toolId,
            String toolName,
            String taskId,
            String toolType,
            String toolApi,
            Object toolParams,
            String description,
            String arguments
    ) implements StreamInput {
        public ToolSnapshot {
            requireNonBlank(toolId, "toolId");
        }
    }

    record ActionArgs(String actionId, String delta, String taskId, String actionName, String description) implements StreamInput {
        public ActionArgs {
            requireNonBlank(actionId, "actionId");
            requireNonNull(delta, "delta");
        }
    }

    record ActionEnd(String actionId) implements StreamInput {
        public ActionEnd {
            requireNonBlank(actionId, "actionId");
        }
    }

    record ActionParam(String actionId, Object param) implements StreamInput {
        public ActionParam {
            requireNonBlank(actionId, "actionId");
            requireNonNull(param, "param");
        }
    }

    record ActionResult(String actionId, Object result) implements StreamInput {
        public ActionResult {
            requireNonBlank(actionId, "actionId");
            requireNonNull(result, "result");
        }
    }

    record ActionSnapshot(String actionId, String actionName, String taskId, String description, String arguments) implements StreamInput {
        public ActionSnapshot {
            requireNonBlank(actionId, "actionId");
        }
    }

    record SourceSnapshot(String sourceId, String runId, String taskId, String icon, String title, String url) implements StreamInput {
        public SourceSnapshot {
            requireNonBlank(sourceId, "sourceId");
        }
    }

    record RunComplete(String finishReason) implements StreamInput {
    }

    record RunCancel() implements StreamInput {
    }

    private static void requireNonBlank(String value, String fieldName) {
        if (value == null || value.isBlank()) {
            throw new IllegalArgumentException(fieldName + " must not be null or blank");
        }
    }

    private static void requireNonNull(Object value, String fieldName) {
        if (value == null) {
            throw new IllegalArgumentException(fieldName + " must not be null");
        }
    }
}
