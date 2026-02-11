package com.linlay.springaiagw.service;

import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.springaiagw.agent.Agent;
import com.linlay.springaiagw.agent.AgentRegistry;
import com.linlay.springaiagw.model.AgentDelta;
import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.model.SseChunk;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.stereotype.Service;
import org.springframework.util.StringUtils;
import reactor.core.publisher.Flux;

import java.time.Instant;
import java.util.List;
import java.util.UUID;

@Service
public class RawAgentSseStreamService {

    private final AgentRegistry agentRegistry;
    private final ObjectMapper objectMapper;

    public RawAgentSseStreamService(AgentRegistry agentRegistry, ObjectMapper objectMapper) {
        this.agentRegistry = agentRegistry;
        this.objectMapper = objectMapper;
    }

    public Flux<ServerSentEvent<String>> stream(String agentId, AgentRequest request) {
        Agent agent = agentRegistry.get(agentId);
        AgentRequest normalizedRequest = normalizeRequest(request);

        String streamId = "chatcmpl-" + UUID.randomUUID().toString().replace("-", "");
        String model = agent.model();

        Flux<ServerSentEvent<String>> body = agent.stream(normalizedRequest)
                .map(delta -> toSse(streamId, model, delta));

        ServerSentEvent<String> done = ServerSentEvent.<String>builder()
                .event("done")
                .data("[DONE]")
                .build();

        return body.concatWithValues(done);
    }

    private AgentRequest normalizeRequest(AgentRequest request) {
        String runId = StringUtils.hasText(request.runId())
                ? request.runId().trim()
                : UUID.randomUUID().toString();
        String requestId = StringUtils.hasText(request.requestId())
                ? request.requestId().trim()
                : runId;
        return new AgentRequest(
                request.message(),
                request.city(),
                request.date(),
                request.chatId(),
                request.chatName(),
                requestId,
                runId
        );
    }

    private ServerSentEvent<String> toSse(String streamId, String model, AgentDelta delta) {
        SseChunk chunk = new SseChunk(
                streamId,
                model,
                Instant.now().getEpochSecond(),
                "chat.completion.chunk",
                List.of(new SseChunk.Choice(
                        0,
                        new SseChunk.Delta(delta.role(), delta.content(), delta.thinking(), delta.toolCalls()),
                        delta.finishReason()
                ))
        );

        return ServerSentEvent.<String>builder()
                .event("message")
                .data(toJson(chunk))
                .build();
    }

    private String toJson(SseChunk chunk) {
        try {
            return objectMapper.writeValueAsString(chunk);
        } catch (JsonProcessingException ex) {
            throw new IllegalStateException("Cannot serialize SSE chunk", ex);
        }
    }
}
