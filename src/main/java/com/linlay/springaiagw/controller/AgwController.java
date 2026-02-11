package com.linlay.springaiagw.controller;

import com.linlay.springaiagw.agent.Agent;
import com.linlay.springaiagw.agent.AgentRegistry;
import com.linlay.springaiagw.model.agw.AgwAgentResponse;
import com.linlay.springaiagw.model.agw.AgwAgentsResponse;
import com.linlay.springaiagw.model.agw.AgwQueryRequest;
import com.linlay.springaiagw.model.agw.AgwSubmitRequest;
import com.linlay.springaiagw.model.agw.AgwSubmitResponse;
import com.linlay.springaiagw.model.agw.ApiResponse;
import com.linlay.springaiagw.service.AgwQueryService;
import com.linlay.springaiagw.service.AgwQueryService.QuerySession;
import jakarta.validation.Valid;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.MediaType;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.util.StringUtils;
import org.springframework.web.bind.annotation.GetMapping;
import org.springframework.web.bind.annotation.PostMapping;
import org.springframework.web.bind.annotation.RequestBody;
import org.springframework.web.bind.annotation.RequestMapping;
import org.springframework.web.bind.annotation.RequestParam;
import org.springframework.web.bind.annotation.RestController;
import org.springframework.http.server.reactive.ServerHttpResponse;
import reactor.core.publisher.Flux;

import java.util.List;
import java.util.Map;

@RestController
@RequestMapping("/api")
public class AgwController {

    private static final Logger log = LoggerFactory.getLogger(AgwController.class);

    private final AgentRegistry agentRegistry;
    private final AgwQueryService agwQueryService;

    public AgwController(AgentRegistry agentRegistry, AgwQueryService agwQueryService) {
        this.agentRegistry = agentRegistry;
        this.agwQueryService = agwQueryService;
    }

    @GetMapping("/agents")
    public ApiResponse<List<AgwAgentsResponse.AgentSummary>> agents(
            @RequestParam(required = false) Boolean includeHidden,
            @RequestParam(required = false) String tag
    ) {
        List<AgwAgentsResponse.AgentSummary> items = agentRegistry.list().stream()
                .filter(agent -> matchesTag(agent, tag))
                .map(this::toSummary)
                .toList();
        return ApiResponse.success(items);
    }

    @GetMapping("/agent")
    public ApiResponse<AgwAgentResponse.AgentDetail> agent(@RequestParam String agentKey) {
        Agent agent = agentRegistry.get(agentKey);
        return ApiResponse.success(toDetail(agent));
    }

    @PostMapping(value = "/query", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    public Flux<ServerSentEvent<String>> query(@Valid @RequestBody AgwQueryRequest request, ServerHttpResponse response) {
        response.getHeaders().set("X-Accel-Buffering", "no");
        response.getHeaders().set("Cache-Control", "no-cache");
        QuerySession session = agwQueryService.prepare(request);
        return agwQueryService.stream(session);
    }

    @PostMapping("/submit")
    public ApiResponse<AgwSubmitResponse> submit(@Valid @RequestBody AgwSubmitRequest request) {
        log.info(
                "Received human-in-the-loop submit requestId={}, runId={}, toolId={}, viewId={}",
                request.requestId(),
                request.runId(),
                request.toolId(),
                request.viewId()
        );
        return ApiResponse.success(new AgwSubmitResponse(
                request.requestId(),
                true,
                request.runId(),
                request.toolId()
        ));
    }

    private boolean matchesTag(Agent agent, String tag) {
        if (!StringUtils.hasText(tag)) {
            return true;
        }
        String normalized = tag.toLowerCase();
        return agent.id().toLowerCase().contains(normalized)
                || agent.description().toLowerCase().contains(normalized)
                || agent.tools().stream().anyMatch(tool -> tool.toLowerCase().contains(normalized));
    }

    private AgwAgentsResponse.AgentSummary toSummary(Agent agent) {
        return new AgwAgentsResponse.AgentSummary(
                agent.id(),
                agent.id(),
                agent.description(),
                null,
                buildMeta(agent)
        );
    }

    private AgwAgentResponse.AgentDetail toDetail(Agent agent) {
        return new AgwAgentResponse.AgentDetail(
                agent.id(),
                agent.id(),
                agent.description(),
                agent.systemPrompt(),
                null,
                buildMeta(agent)
        );
    }

    private Map<String, Object> buildMeta(Agent agent) {
        return Map.of(
                "providerType", agent.providerType().name(),
                "model", agent.model(),
                "mode", agent.mode().name(),
                "tools", agent.tools()
        );
    }
}
