package com.linlay.agentplatform.stream.service;

import com.linlay.agentplatform.stream.model.StreamEvent;
import com.linlay.agentplatform.stream.model.StreamInput;
import com.linlay.agentplatform.stream.model.StreamRequest;
import com.fasterxml.jackson.core.JsonProcessingException;
import com.fasterxml.jackson.databind.ObjectMapper;
import org.springframework.http.codec.ServerSentEvent;
import reactor.core.Disposable;
import reactor.core.publisher.Flux;

import java.time.Duration;
import java.util.Objects;
import java.util.concurrent.TimeoutException;

public class StreamSseStreamer {

    private static final String SSE_EVENT_MESSAGE = "message";
    private static final Duration DEFAULT_STREAM_TIMEOUT = Duration.ofMinutes(5);
    private static final Duration DEFAULT_HEARTBEAT_INTERVAL = Duration.ofSeconds(15);
    private static final ServerSentEvent<String> HEARTBEAT = ServerSentEvent.<String>builder()
            .comment("heartbeat")
            .build();

    private final StreamEventAssembler eventAssembler;
    private final ObjectMapper objectMapper;
    private final Duration streamTimeout;
    private final Duration heartbeatInterval;

    public StreamSseStreamer(StreamEventAssembler eventAssembler, ObjectMapper objectMapper) {
        this(eventAssembler, objectMapper, DEFAULT_STREAM_TIMEOUT, DEFAULT_HEARTBEAT_INTERVAL);
    }

    public StreamSseStreamer(StreamEventAssembler eventAssembler, ObjectMapper objectMapper,
                          Duration streamTimeout, Duration heartbeatInterval) {
        this.eventAssembler = Objects.requireNonNull(eventAssembler, "eventAssembler cannot be null");
        this.objectMapper = Objects.requireNonNull(objectMapper, "objectMapper cannot be null");
        this.streamTimeout = streamTimeout != null ? streamTimeout : DEFAULT_STREAM_TIMEOUT;
        this.heartbeatInterval = heartbeatInterval != null ? heartbeatInterval : DEFAULT_HEARTBEAT_INTERVAL;
    }

    public StreamRequest.Query createQueryRequest(String message) {
        return new StreamRequest.Query(
                generateId("req"),
                generateId("chat"),
                "user",
                normalizeMessage(message),
                null,
                null,
                null,
                null,
                Boolean.TRUE,
                "default"
        );
    }

    public Flux<ServerSentEvent<String>> stream(StreamRequest request, Flux<StreamInput> inputs) {
        Objects.requireNonNull(request, "request cannot be null");
        Objects.requireNonNull(inputs, "inputs cannot be null");

        StreamEventAssembler.EventStreamState state = eventAssembler.begin(request);

        Flux<ServerSentEvent<String>> bootstrapFlux = Flux.fromIterable(state.bootstrapEvents())
                .map(this::toSse);

        Flux<ServerSentEvent<String>> bodyFlux = inputs
                .concatMap(input -> Flux.fromIterable(state.consume(input)))
                .map(this::toSse);

        Flux<ServerSentEvent<String>> bodyWithHeartbeat = Flux.create(sink -> {
            Disposable heartbeat = Flux.interval(heartbeatInterval)
                    .subscribe(tick -> sink.next(HEARTBEAT));
            bodyFlux.subscribe(
                    sink::next,
                    err -> {
                        heartbeat.dispose();
                        sink.error(err);
                    },
                    () -> {
                        heartbeat.dispose();
                        sink.complete();
                    }
            );
            sink.onDispose(heartbeat::dispose);
        });

        Flux<ServerSentEvent<String>> completeFlux = Flux.defer(() ->
                Flux.fromIterable(state.complete()).map(this::toSse)
        );

        return bootstrapFlux
                .concatWith(bodyWithHeartbeat)
                .concatWith(completeFlux)
                .timeout(streamTimeout)
                .onErrorResume(ex -> {
                    try {
                        Throwable cause = ex instanceof TimeoutException
                                ? new RuntimeException("Stream timed out after " + streamTimeout)
                                : ex;
                        return Flux.fromIterable(state.fail(cause)).map(this::toSse);
                    } catch (Exception fallbackEx) {
                        return Flux.just(ServerSentEvent.<String>builder()
                                .event(SSE_EVENT_MESSAGE)
                                .data("{\"type\":\"run.error\",\"error\":{\"message\":\"Internal serialization failure\"}}")
                                .build());
                    }
                });
    }

    public String generateId(String prefix) {
        return eventAssembler.generateId(prefix);
    }

    private ServerSentEvent<String> toSse(StreamEvent event) {
        return ServerSentEvent.<String>builder()
                .event(SSE_EVENT_MESSAGE)
                .data(toJson(event.toData()))
                .build();
    }

    private String normalizeMessage(String message) {
        return message == null ? "" : message;
    }

    private String toJson(Object value) {
        try {
            return objectMapper.writeValueAsString(value);
        } catch (JsonProcessingException ex) {
            throw new IllegalStateException("Cannot serialize stream SSE event", ex);
        }
    }
}
