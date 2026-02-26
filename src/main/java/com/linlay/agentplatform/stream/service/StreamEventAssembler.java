package com.linlay.agentplatform.stream.service;

import com.linlay.agentplatform.stream.model.StreamEvent;
import com.linlay.agentplatform.stream.model.StreamInput;
import com.linlay.agentplatform.stream.model.StreamRequest;

import org.slf4j.Logger;
import org.slf4j.LoggerFactory;

import java.time.Instant;
import java.util.ArrayList;
import java.util.HashMap;
import java.util.LinkedHashMap;
import java.util.LinkedHashSet;
import java.util.List;
import java.util.Map;
import java.util.Objects;
import java.util.Set;
import java.util.UUID;
import java.util.concurrent.atomic.AtomicInteger;
import java.util.concurrent.atomic.AtomicLong;

public class StreamEventAssembler {

    public static final String INTERNAL_PARAM_EMIT_CHAT_START = "__emit_chat_start";

    public EventStreamState begin(StreamRequest request) {
        return new EventStreamState(Objects.requireNonNull(request, "request must not be null"));
    }

    public static final class EventStreamState {

        private static final Logger log = LoggerFactory.getLogger(EventStreamState.class);

        private final AtomicLong seq = new AtomicLong(0);
        private final List<StreamEvent> bootstrapEvents = new ArrayList<>();

        private final StreamRequest request;
        private final String chatId;
        private final String chatName;
        private final String runId;
        private final boolean emitChatStart;

        private String planId;
        private String activeTaskId;
        private String activeReasoningId;
        private String activeContentId;

        private final Set<String> knownToolIds = new LinkedHashSet<>();
        private final Set<String> openToolIds = new LinkedHashSet<>();
        private final Map<String, AtomicInteger> toolArgChunkCounters = new HashMap<>();

        private final Set<String> knownActionIds = new LinkedHashSet<>();
        private final Set<String> openActionIds = new LinkedHashSet<>();

        private boolean terminated;

        private EventStreamState(StreamRequest request) {
            this.request = request;
            this.chatId = resolveChatId(request);
            this.chatName = request instanceof StreamRequest.Query q ? defaultIfBlank(q.chatName(), "default") : null;
            this.runId = resolveRunId(request);
            this.emitChatStart = request instanceof StreamRequest.Query query ? resolveEmitChatStart(query.params()) : false;
            bootstrap();
        }

        public List<StreamEvent> bootstrapEvents() {
            return List.copyOf(bootstrapEvents);
        }

        public List<StreamEvent> consume(StreamInput input) {
            List<StreamEvent> events = new ArrayList<>();
            if (input == null) {
                return events;
            }
            if (terminated) {
                log.warn("consume() called after stream already terminated; input ignored: {}", input);
                return events;
            }

            try {
                dispatch(events, input);
            } catch (RuntimeException ex) {
                if (!hasRunContext()) {
                    throw ex;
                }
                events.addAll(fail(ex));
            }
            return events;
        }

        public List<StreamEvent> complete() {
            if (terminated) {
                return List.of();
            }
            List<StreamEvent> events = new ArrayList<>();
            if (!hasRunContext()) {
                terminated = true;
                return events;
            }
            completeRun(events, null);
            return events;
        }

        public List<StreamEvent> fail(Throwable ex) {
            if (terminated) {
                return List.of();
            }
            terminated = true;
            if (!hasRunContext()) {
                return List.of();
            }
            List<StreamEvent> events = new ArrayList<>();
            closeOpenBlocks(events);
            Map<String, Object> error = errorPayload(ex);
            if (activeTaskId != null) {
                emitTaskFail(events, activeTaskId, error, ex);
            }

            Map<String, Object> runError = new LinkedHashMap<>();
            runError.put("runId", runId);
            runError.put("error", error);
            events.add(next("run.error", runError, ex));
            return events;
        }

        private void bootstrap() {
            if (request instanceof StreamRequest.Query query) {
                Map<String, Object> visibleParams = filterVisibleQueryParams(query.params());
                Map<String, Object> requestPayload = new LinkedHashMap<>();
                requestPayload.put("requestId", query.requestId());
                requestPayload.put("chatId", query.chatId());
                requestPayload.put("role", query.role());
                requestPayload.put("message", query.message());
                putIfNonNull(requestPayload, "agentKey", query.agentKey());
                putIfNonNull(requestPayload, "references", query.references());
                putIfNonNull(requestPayload, "params", visibleParams);
                putIfNonNull(requestPayload, "scene", query.scene());
                putIfNonNull(requestPayload, "stream", query.stream());
                bootstrapEvents.add(next("request.query", requestPayload, query));

                if (emitChatStart) {
                    Map<String, Object> chatPayload = new LinkedHashMap<>();
                    chatPayload.put("chatId", query.chatId());
                    putIfNonNull(chatPayload, "chatName", chatName);
                    bootstrapEvents.add(next("chat.start", chatPayload, query));
                }

                Map<String, Object> runPayload = new LinkedHashMap<>();
                runPayload.put("runId", runId);
                runPayload.put("chatId", query.chatId());
                bootstrapEvents.add(next("run.start", runPayload, query));
                return;
            }

            if (request instanceof StreamRequest.Upload upload) {
                Map<String, Object> requestPayload = new LinkedHashMap<>();
                requestPayload.put("requestId", upload.requestId());
                putIfNonNull(requestPayload, "chatId", upload.chatId());
                Map<String, Object> uploadPayload = new LinkedHashMap<>();
                uploadPayload.put("type", upload.uploadType());
                uploadPayload.put("name", upload.uploadName());
                uploadPayload.put("sizeBytes", upload.sizeBytes());
                uploadPayload.put("mimeType", upload.mimeType());
                putIfNonNull(uploadPayload, "sha256", upload.sha256());
                requestPayload.put("upload", uploadPayload);
                bootstrapEvents.add(next("request.upload", requestPayload, upload));
                return;
            }

            StreamRequest.Submit submit = (StreamRequest.Submit) request;
            Map<String, Object> requestPayload = new LinkedHashMap<>();
            requestPayload.put("requestId", submit.requestId());
            requestPayload.put("chatId", submit.chatId());
            requestPayload.put("runId", submit.runId());
            requestPayload.put("toolId", submit.toolId());
            requestPayload.put("payload", submit.payload());
            putIfNonNull(requestPayload, "viewId", submit.viewId());
            bootstrapEvents.add(next("request.submit", requestPayload, submit));
        }

        private void dispatch(List<StreamEvent> events, StreamInput input) {
            if (input instanceof StreamInput.PlanCreate value) {
                ensureChatContext();
                if (!chatId.equals(value.chatId())) {
                    throw new IllegalStateException("plan.create chatId does not match stream chatId");
                }
                planId = value.planId();
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("planId", value.planId());
                payload.put("chatId", value.chatId());
                payload.put("plan", value.plan());
                events.add(next("plan.create", payload, value));
                return;
            }
            if (input instanceof StreamInput.PlanUpdate value) {
                ensureChatContext();
                if (value.chatId() != null && !chatId.equals(value.chatId())) {
                    throw new IllegalStateException("plan.update chatId does not match stream chatId");
                }
                if (planId != null && !planId.equals(value.planId())) {
                    throw new IllegalStateException("plan.update planId does not match active planId");
                }
                planId = value.planId();
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("planId", value.planId());
                payload.put("plan", value.plan());
                putIfNonNull(payload, "chatId", value.chatId() != null ? value.chatId() : chatId);
                events.add(next("plan.update", payload, value));
                return;
            }
            if (input instanceof StreamInput.TaskStart value) {
                ensureRunContext();
                if (planId == null) {
                    throw new IllegalStateException("task.start requires an active plan");
                }
                if (!runId.equals(value.runId())) {
                    throw new IllegalStateException("task.start runId does not match stream runId");
                }
                if (activeTaskId != null) {
                    throw new IllegalStateException("task.start is not allowed while another task is active");
                }
                emitTaskStart(events, value.taskId(), value.taskName(), value.description(), value);
                return;
            }
            if (input instanceof StreamInput.TaskComplete value) {
                ensureRunContext();
                ensureActiveTask(value.taskId(), "task.complete");
                closeOpenBlocks(events);
                emitTaskComplete(events, value.taskId(), value);
                return;
            }
            if (input instanceof StreamInput.TaskCancel value) {
                ensureRunContext();
                ensureActiveTask(value.taskId(), "task.cancel");
                closeOpenBlocks(events);
                emitTaskCancel(events, value.taskId(), value);
                return;
            }
            if (input instanceof StreamInput.TaskFail value) {
                ensureRunContext();
                ensureActiveTask(value.taskId(), "task.fail");
                closeOpenBlocks(events);
                emitTaskFail(events, value.taskId(), value.error(), value);
                return;
            }
            if (input instanceof StreamInput.ReasoningDelta value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "reasoning.delta");
                closeContent(events);
                ensureReasoning(events, value.reasoningId(), taskId, value);
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("reasoningId", value.reasoningId());
                payload.put("delta", value.delta());
                events.add(next("reasoning.delta", payload, value));
                return;
            }
            if (input instanceof StreamInput.ReasoningSnapshot value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "reasoning.snapshot");
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("reasoningId", value.reasoningId());
                payload.put("text", value.text());
                putIfNonNull(payload, "taskId", taskId);
                events.add(next("reasoning.snapshot", payload, value));
                return;
            }
            if (input instanceof StreamInput.ContentDelta value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "content.delta");
                closeReasoning(events);
                ensureContent(events, value.contentId(), taskId, value);
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("contentId", value.contentId());
                payload.put("delta", value.delta());
                events.add(next("content.delta", payload, value));
                return;
            }
            if (input instanceof StreamInput.ContentSnapshot value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "content.snapshot");
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("contentId", value.contentId());
                payload.put("text", value.text());
                putIfNonNull(payload, "taskId", taskId);
                events.add(next("content.snapshot", payload, value));
                return;
            }
            if (input instanceof StreamInput.ToolArgs value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "tool.args");
                closeTextBlocks(events);
                if (!knownToolIds.contains(value.toolId())) {
                    knownToolIds.add(value.toolId());
                    openToolIds.add(value.toolId());
                    Map<String, Object> payload = new LinkedHashMap<>();
                    payload.put("toolId", value.toolId());
                    payload.put("runId", runId);
                    putIfNonNull(payload, "taskId", taskId);
                    putIfNonNull(payload, "toolName", value.toolName());
                    putIfNonNull(payload, "toolType", value.toolType());
                    putIfNonNull(payload, "toolApi", value.toolApi());
                    putIfNonNull(payload, "toolParams", value.toolParams());
                    putIfNonNull(payload, "description", value.description());
                    events.add(next("tool.start", payload, value));
                } else if (!openToolIds.contains(value.toolId())) {
                    throw new IllegalStateException("tool.args for closed toolId: " + value.toolId());
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("toolId", value.toolId());
                payload.put("delta", value.delta());
                payload.put("chunkIndex", value.chunkIndex() != null
                        ? value.chunkIndex()
                        : toolArgChunkCounters.computeIfAbsent(value.toolId(), k -> new AtomicInteger(0)).getAndIncrement());
                events.add(next("tool.args", payload, value));
                return;
            }
            if (input instanceof StreamInput.ToolEnd value) {
                ensureRunContext();
                closeTextBlocks(events);
                if (!openToolIds.remove(value.toolId())) {
                    throw new IllegalStateException("tool.end for unknown or already closed toolId: " + value.toolId());
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("toolId", value.toolId());
                events.add(next("tool.end", payload, value));
                return;
            }
            if (input instanceof StreamInput.ToolResult value) {
                ensureRunContext();
                closeTextBlocks(events);
                if (!knownToolIds.contains(value.toolId())) {
                    throw new IllegalStateException("tool.result references unknown toolId: " + value.toolId());
                }
                if (openToolIds.remove(value.toolId())) {
                    Map<String, Object> endPayload = new LinkedHashMap<>();
                    endPayload.put("toolId", value.toolId());
                    events.add(next("tool.end", endPayload, value));
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("toolId", value.toolId());
                payload.put("result", value.result());
                events.add(next("tool.result", payload, value));
                return;
            }
            if (input instanceof StreamInput.ToolSnapshot value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "tool.snapshot");
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("toolId", value.toolId());
                putIfNonNull(payload, "toolName", value.toolName());
                putIfNonNull(payload, "taskId", taskId);
                putIfNonNull(payload, "toolType", value.toolType());
                putIfNonNull(payload, "toolApi", value.toolApi());
                putIfNonNull(payload, "toolParams", value.toolParams());
                putIfNonNull(payload, "description", value.description());
                putIfNonNull(payload, "arguments", value.arguments());
                events.add(next("tool.snapshot", payload, value));
                return;
            }
            if (input instanceof StreamInput.ActionArgs value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "action.args");
                closeTextBlocks(events);
                if (!knownActionIds.contains(value.actionId())) {
                    knownActionIds.add(value.actionId());
                    openActionIds.add(value.actionId());
                    Map<String, Object> startPayload = new LinkedHashMap<>();
                    startPayload.put("actionId", value.actionId());
                    startPayload.put("runId", runId);
                    putIfNonNull(startPayload, "taskId", taskId);
                    putIfNonNull(startPayload, "actionName", value.actionName());
                    putIfNonNull(startPayload, "description", value.description());
                    events.add(next("action.start", startPayload, value));
                } else if (!openActionIds.contains(value.actionId())) {
                    throw new IllegalStateException("action.args for closed actionId: " + value.actionId());
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("actionId", value.actionId());
                payload.put("delta", value.delta());
                events.add(next("action.args", payload, value));
                return;
            }
            if (input instanceof StreamInput.ActionEnd value) {
                ensureRunContext();
                closeTextBlocks(events);
                if (!openActionIds.remove(value.actionId())) {
                    throw new IllegalStateException("action.end for unknown or already closed actionId: " + value.actionId());
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("actionId", value.actionId());
                events.add(next("action.end", payload, value));
                return;
            }
            if (input instanceof StreamInput.ActionParam value) {
                ensureRunContext();
                closeTextBlocks(events);
                if (!knownActionIds.contains(value.actionId())) {
                    throw new IllegalStateException("action.param references unknown actionId: " + value.actionId());
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("actionId", value.actionId());
                payload.put("param", value.param());
                events.add(next("action.param", payload, value));
                return;
            }
            if (input instanceof StreamInput.ActionResult value) {
                ensureRunContext();
                closeTextBlocks(events);
                if (!knownActionIds.contains(value.actionId())) {
                    throw new IllegalStateException("action.result references unknown actionId: " + value.actionId());
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("actionId", value.actionId());
                payload.put("result", value.result());
                events.add(next("action.result", payload, value));
                return;
            }
            if (input instanceof StreamInput.ActionSnapshot value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "action.snapshot");
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("actionId", value.actionId());
                putIfNonNull(payload, "actionName", value.actionName());
                putIfNonNull(payload, "taskId", taskId);
                putIfNonNull(payload, "description", value.description());
                putIfNonNull(payload, "arguments", value.arguments());
                events.add(next("action.snapshot", payload, value));
                return;
            }
            if (input instanceof StreamInput.SourceSnapshot value) {
                ensureRunContext();
                String taskId = resolveTaskContext(value.taskId(), "source.snapshot");
                if (value.runId() != null && !runId.equals(value.runId())) {
                    throw new IllegalStateException("source.snapshot runId does not match stream runId");
                }
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("sourceId", value.sourceId());
                putIfNonNull(payload, "runId", value.runId() != null ? value.runId() : runId);
                putIfNonNull(payload, "taskId", taskId);
                putIfNonNull(payload, "icon", value.icon());
                putIfNonNull(payload, "title", value.title());
                putIfNonNull(payload, "url", value.url());
                events.add(next("source.snapshot", payload, value));
                return;
            }
            if (input instanceof StreamInput.RunComplete value) {
                ensureRunContext();
                completeRun(events, value.finishReason());
                return;
            }
            if (input instanceof StreamInput.RunCancel value) {
                ensureRunContext();
                cancelRun(events, value);
                return;
            }
            throw new IllegalStateException("Unknown input type: " + input.getClass().getName());
        }

        private String resolveTaskContext(String requestedTaskId, String eventType) {
            ensureRunContext();
            if (requestedTaskId != null && requestedTaskId.isBlank()) {
                throw new IllegalStateException("taskId must not be blank");
            }
            if (requestedTaskId == null) {
                return activeTaskId;
            }
            if (activeTaskId == null) {
                throw new IllegalStateException(eventType + " taskId requires an active task");
            }
            if (!activeTaskId.equals(requestedTaskId)) {
                throw new IllegalStateException(eventType + " taskId does not match active task");
            }
            return activeTaskId;
        }

        private void ensureReasoning(List<StreamEvent> events, String reasoningId, String taskId, Object rawEvent) {
            if (activeReasoningId == null) {
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("reasoningId", reasoningId);
                payload.put("runId", runId);
                putIfNonNull(payload, "taskId", taskId);
                events.add(next("reasoning.start", payload, rawEvent));
                activeReasoningId = reasoningId;
                return;
            }
            if (!activeReasoningId.equals(reasoningId)) {
                closeReasoning(events);
                ensureReasoning(events, reasoningId, taskId, rawEvent);
            }
        }

        private void ensureContent(List<StreamEvent> events, String contentId, String taskId, Object rawEvent) {
            if (activeContentId == null) {
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("contentId", contentId);
                payload.put("runId", runId);
                putIfNonNull(payload, "taskId", taskId);
                events.add(next("content.start", payload, rawEvent));
                activeContentId = contentId;
                return;
            }
            if (!activeContentId.equals(contentId)) {
                closeContent(events);
                ensureContent(events, contentId, taskId, rawEvent);
            }
        }

        private void completeRun(List<StreamEvent> events, String finishReason) {
            if (terminated) {
                return;
            }
            closeOpenBlocks(events);
            if (activeTaskId != null) {
                emitTaskComplete(events, activeTaskId, request);
            }
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("runId", runId);
            payload.put("finishReason", defaultIfBlank(finishReason, "end_turn"));
            events.add(next("run.complete", payload, request));
            terminated = true;
        }

        private void cancelRun(List<StreamEvent> events, Object rawEvent) {
            if (terminated) {
                return;
            }
            closeOpenBlocks(events);
            if (activeTaskId != null) {
                emitTaskCancel(events, activeTaskId, rawEvent);
            }
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("runId", runId);
            events.add(next("run.cancel", payload, rawEvent));
            terminated = true;
        }

        private void closeOpenBlocks(List<StreamEvent> events) {
            closeTextBlocks(events);
            closeOpenTools(events);
            closeOpenActions(events);
        }

        private void closeTextBlocks(List<StreamEvent> events) {
            closeReasoning(events);
            closeContent(events);
        }

        private void closeReasoning(List<StreamEvent> events) {
            if (activeReasoningId == null) {
                return;
            }
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("reasoningId", activeReasoningId);
            events.add(next("reasoning.end", payload, request));
            activeReasoningId = null;
        }

        private void closeContent(List<StreamEvent> events) {
            if (activeContentId == null) {
                return;
            }
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("contentId", activeContentId);
            events.add(next("content.end", payload, request));
            activeContentId = null;
        }

        private void closeOpenTools(List<StreamEvent> events) {
            if (openToolIds.isEmpty()) {
                return;
            }
            for (String toolId : List.copyOf(openToolIds)) {
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("toolId", toolId);
                events.add(next("tool.end", payload, request));
                openToolIds.remove(toolId);
            }
        }

        private void closeOpenActions(List<StreamEvent> events) {
            if (openActionIds.isEmpty()) {
                return;
            }
            for (String actionId : List.copyOf(openActionIds)) {
                Map<String, Object> payload = new LinkedHashMap<>();
                payload.put("actionId", actionId);
                events.add(next("action.end", payload, request));
                openActionIds.remove(actionId);
            }
        }

        private void emitTaskStart(List<StreamEvent> events, String taskId, String taskName, String description, Object rawEvent) {
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("taskId", taskId);
            payload.put("runId", runId);
            putIfNonNull(payload, "taskName", taskName);
            putIfNonNull(payload, "description", description);
            events.add(next("task.start", payload, rawEvent));
            activeTaskId = taskId;
        }

        private void emitTaskComplete(List<StreamEvent> events, String taskId, Object rawEvent) {
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("taskId", taskId);
            events.add(next("task.complete", payload, rawEvent));
            activeTaskId = null;
        }

        private void emitTaskCancel(List<StreamEvent> events, String taskId, Object rawEvent) {
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("taskId", taskId);
            events.add(next("task.cancel", payload, rawEvent));
            activeTaskId = null;
        }

        private void emitTaskFail(List<StreamEvent> events, String taskId, Map<String, Object> error, Object rawEvent) {
            Map<String, Object> payload = new LinkedHashMap<>();
            payload.put("taskId", taskId);
            payload.put("error", error);
            events.add(next("task.fail", payload, rawEvent));
            activeTaskId = null;
        }

        private void ensureActiveTask(String taskId, String eventType) {
            if (activeTaskId == null) {
                throw new IllegalStateException(eventType + " requires an active task");
            }
            if (!activeTaskId.equals(taskId)) {
                throw new IllegalStateException(eventType + " taskId does not match active task");
            }
        }

        private void ensureRunContext() {
            if (!hasRunContext()) {
                throw new IllegalStateException("current request type does not provide run context");
            }
        }

        private void ensureChatContext() {
            if (chatId == null) {
                throw new IllegalStateException("current request type does not provide chat context");
            }
        }

        private boolean hasRunContext() {
            return runId != null;
        }

        private String nextId(String prefix) {
            return generateIdInternal(prefix);
        }

        private Map<String, Object> errorPayload(Throwable ex) {
            Map<String, Object> error = new LinkedHashMap<>();
            error.put("code", "RUN_ERROR");
            error.put("message", resolveErrorMessage(ex));
            error.put("retriable", false);
            return error;
        }

        private String resolveErrorMessage(Throwable ex) {
            if (ex == null) {
                return "Unknown run error";
            }
            String message = ex.getMessage();
            if (message != null && !message.isBlank()) {
                return message;
            }
            return ex.getClass().getSimpleName();
        }

        private StreamEvent next(String type, Map<String, Object> payload, Object rawEvent) {
            return new StreamEvent(seq.incrementAndGet(), type, Instant.now().toEpochMilli(), payload, rawEvent);
        }

        private String resolveChatId(StreamRequest request) {
            if (request instanceof StreamRequest.Query q) {
                return q.chatId();
            }
            if (request instanceof StreamRequest.Submit s) {
                return s.chatId();
            }
            return ((StreamRequest.Upload) request).chatId();
        }

        private String resolveRunId(StreamRequest request) {
            if (request instanceof StreamRequest.Query query) {
                if (query.runId() != null && !query.runId().isBlank()) {
                    return query.runId();
                }
                return nextId("run");
            }
            if (request instanceof StreamRequest.Submit submit) {
                return submit.runId();
            }
            return null;
        }
    }

    public String generateId(String prefix) {
        return generateIdInternal(prefix);
    }

    private static String generateIdInternal(String prefix) {
        String actualPrefix = prefix == null || prefix.isBlank() ? "id" : prefix;
        return actualPrefix + "_" + UUID.randomUUID().toString().replace("-", "").substring(0, 8);
    }

    private static boolean resolveEmitChatStart(Map<String, Object> params) {
        if (params == null || !params.containsKey(INTERNAL_PARAM_EMIT_CHAT_START)) {
            return true;
        }
        Object value = params.get(INTERNAL_PARAM_EMIT_CHAT_START);
        if (value instanceof Boolean boolValue) {
            return boolValue;
        }
        if (value instanceof Number number) {
            return number.intValue() != 0;
        }
        if (value instanceof String text) {
            return Boolean.parseBoolean(text.trim());
        }
        return true;
    }

    private static Map<String, Object> filterVisibleQueryParams(Map<String, Object> params) {
        if (params == null || params.isEmpty()) {
            return null;
        }
        Map<String, Object> filtered = new LinkedHashMap<>();
        params.forEach((key, value) -> {
            if (!INTERNAL_PARAM_EMIT_CHAT_START.equals(key)) {
                filtered.put(key, value);
            }
        });
        return filtered.isEmpty() ? null : filtered;
    }

    private static String defaultIfBlank(String value, String fallback) {
        if (value == null || value.isBlank()) {
            return fallback;
        }
        return value;
    }

    private static void putIfNonNull(Map<String, Object> payload, String key, Object value) {
        if (value != null) {
            payload.put(key, value);
        }
    }
}
