package com.linlay.springaiagw.controller;

import com.aiagent.agw.sdk.service.SseFlushWriter;
import com.linlay.springaiagw.agent.Agent;
import com.linlay.springaiagw.agent.AgentRegistry;
import com.linlay.springaiagw.model.api.AgwAgentResponse;
import com.linlay.springaiagw.model.api.AgwAgentsResponse;
import com.linlay.springaiagw.model.api.AgwChatDetailResponse;
import com.linlay.springaiagw.model.api.AgwChatSummaryResponse;
import com.linlay.springaiagw.model.api.AgwQueryRequest;
import com.linlay.springaiagw.model.api.AgwSubmitRequest;
import com.linlay.springaiagw.model.api.AgwSubmitResponse;
import com.linlay.springaiagw.model.api.ApiResponse;
import com.linlay.springaiagw.service.AgwQueryService;
import com.linlay.springaiagw.service.AgwQueryService.QuerySession;
import com.linlay.springaiagw.service.ChatRecordStore;
import com.linlay.springaiagw.service.FrontendSubmitCoordinator;
import com.linlay.springaiagw.service.ViewportRegistryService;
import jakarta.validation.Valid;
import org.slf4j.Logger;
import org.slf4j.LoggerFactory;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
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
import reactor.core.publisher.Mono;

import java.util.List;
import java.util.Locale;
import java.util.Map;

@RestController
@RequestMapping("/api")
public class AgwController {

    private static final Logger log = LoggerFactory.getLogger(AgwController.class);

    private final AgentRegistry agentRegistry;
    private final AgwQueryService agwQueryService;
    private final ChatRecordStore chatRecordStore;
    private final SseFlushWriter sseFlushWriter;
    private final ViewportRegistryService viewportRegistryService;
    private final FrontendSubmitCoordinator frontendSubmitCoordinator;

    public AgwController(
            AgentRegistry agentRegistry,
            AgwQueryService agwQueryService,
            ChatRecordStore chatRecordStore,
            SseFlushWriter sseFlushWriter,
            ViewportRegistryService viewportRegistryService,
            FrontendSubmitCoordinator frontendSubmitCoordinator
    ) {
        this.agentRegistry = agentRegistry;
        this.agwQueryService = agwQueryService;
        this.chatRecordStore = chatRecordStore;
        this.sseFlushWriter = sseFlushWriter;
        this.viewportRegistryService = viewportRegistryService;
        this.frontendSubmitCoordinator = frontendSubmitCoordinator;
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

    @GetMapping("/chats")
    public ApiResponse<List<AgwChatSummaryResponse>> chats() {
        return ApiResponse.success(chatRecordStore.listChats());
    }

    @GetMapping("/chat")
    public ApiResponse<AgwChatDetailResponse> chat(
            @RequestParam String chatId,
            @RequestParam(defaultValue = "false") boolean includeRawMessages,
            @RequestParam(required = false) Boolean includeEvents
    ) {
        if (includeEvents != null) {
            throw new IllegalArgumentException("includeEvents is deprecated; use includeRawMessages=true to include messages");
        }
        return ApiResponse.success(chatRecordStore.loadChat(chatId, includeRawMessages));
    }

    @PostMapping(value = "/query", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    public Mono<Void> query(@Valid @RequestBody AgwQueryRequest request, ServerHttpResponse response) {
        QuerySession session = agwQueryService.prepare(request);
        Flux<ServerSentEvent<String>> stream = agwQueryService.stream(session);
        return sseFlushWriter.write(response, stream);
    }

    @PostMapping("/submit")
    public ApiResponse<AgwSubmitResponse> submit(@Valid @RequestBody AgwSubmitRequest request) {
        boolean accepted = frontendSubmitCoordinator.submit(request.runId(), request.toolId(), request.payload());
        log.info(
                "Received human-in-the-loop submit requestId={}, runId={}, toolId={}, viewId={}, accepted={}",
                request.requestId(),
                request.runId(),
                request.toolId(),
                request.viewId(),
                accepted
        );
        return ApiResponse.success(new AgwSubmitResponse(
                request.requestId(),
                accepted,
                request.runId(),
                request.toolId()
        ));
    }

    @GetMapping("/viewport")
    public ResponseEntity<ApiResponse<Object>> viewport(
            @RequestParam String viewportKey,
            @RequestParam(required = false) String chatId,
            @RequestParam(required = false) String runId
    ) {
        if (!StringUtils.hasText(viewportKey)) {
            throw new IllegalArgumentException("viewportKey is required");
        }
        return viewportRegistryService.find(viewportKey)
                .<ResponseEntity<ApiResponse<Object>>>map(viewport -> {
                    Object data = viewport.payload();
                    if ("html".equalsIgnoreCase(viewport.viewportType().value())) {
                        data = Map.of("html", String.valueOf(viewport.payload()));
                    }
                    return ResponseEntity.ok(ApiResponse.success(data));
                })
                .orElseGet(() -> ResponseEntity.status(HttpStatus.NOT_FOUND)
                        .body(ApiResponse.failure(
                                HttpStatus.NOT_FOUND.value(),
                                "Viewport not found: " + viewportKey,
                                (Object) Map.of()
                        )));
    }

    private boolean matchesTag(Agent agent, String tag) {
        if (!StringUtils.hasText(tag)) {
            return true;
        }
        String normalized = tag.toLowerCase();
        return agent.id().toLowerCase().contains(normalized)
                || agent.description().toLowerCase().contains(normalized)
                || agent.tools().stream().anyMatch(tool -> tool.toLowerCase().contains(normalized))
                || agent.skills().stream().anyMatch(skill -> skill.toLowerCase().contains(normalized));
    }

    private AgwAgentsResponse.AgentSummary toSummary(Agent agent) {
        return new AgwAgentsResponse.AgentSummary(
                agent.id(),
                agent.name(),
                agent.description(),
                null,
                buildSummaryMeta(agent)
        );
    }

    private AgwAgentResponse.AgentDetail toDetail(Agent agent) {
        return new AgwAgentResponse.AgentDetail(
                agent.id(),
                agent.name(),
                agent.description(),
                agent.systemPrompt(),
                null,
                buildDetailMeta(agent)
        );
    }

    private Map<String, Object> buildSummaryMeta(Agent agent) {
        Map<String, Object> meta = new java.util.LinkedHashMap<>();
        meta.put("model", agent.model());
        meta.put("mode", agent.mode().name());
        meta.put("icon", agent.icon());
        meta.put("tools", agent.tools());
        meta.put("skills", agent.skills());
        return meta;
    }

    private Map<String, Object> buildDetailMeta(Agent agent) {
        Map<String, Object> meta = new java.util.LinkedHashMap<>();
        meta.put("providerType", agent.providerKey().toUpperCase(Locale.ROOT));
        meta.put("model", agent.model());
        meta.put("mode", agent.mode().name());
        meta.put("icon", agent.icon());
        meta.put("tools", agent.tools());
        meta.put("skills", agent.skills());
        return meta;
    }
}
