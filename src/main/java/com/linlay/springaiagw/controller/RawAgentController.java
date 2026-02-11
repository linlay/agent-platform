package com.linlay.springaiagw.controller;

import com.linlay.springaiagw.model.AgentRequest;
import com.linlay.springaiagw.service.RawAgentSseStreamService;
import jakarta.validation.Valid;
import org.springframework.http.MediaType;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.http.server.reactive.ServerHttpResponse;
import org.springframework.web.bind.annotation.PathVariable;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RestController;
import reactor.core.publisher.Flux;

@RestController
@RequestMapping("/raw-api")
public class RawAgentController {

    private final RawAgentSseStreamService rawAgentSseStreamService;

    public RawAgentController(RawAgentSseStreamService rawAgentSseStreamService) {
        this.rawAgentSseStreamService = rawAgentSseStreamService;
    }

    @PostMapping(value = "/{agentId}", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    public Flux<ServerSentEvent<String>> stream(
            @PathVariable String agentId,
            @Valid @RequestBody AgentRequest request,
            ServerHttpResponse response
    ) {
        response.getHeaders().set("X-Accel-Buffering", "no");
        response.getHeaders().set("Cache-Control", "no-cache");
        return rawAgentSseStreamService.stream(agentId, request);
    }
}
