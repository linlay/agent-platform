package com.linlay.agentplatform.controller;

import com.linlay.agentplatform.stream.service.SseFlushWriter;
import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.fasterxml.jackson.databind.node.ObjectNode;
import com.linlay.agentplatform.agent.Agent;
import com.linlay.agentplatform.agent.AgentRegistry;
import com.linlay.agentplatform.model.api.AgentDetailResponse;
import com.linlay.agentplatform.model.api.AgentChatSummaryResponse;
import com.linlay.agentplatform.model.api.AgentListResponse;
import com.linlay.agentplatform.model.api.ApiResponse;
import com.linlay.agentplatform.model.api.ChatDetailResponse;
import com.linlay.agentplatform.model.api.ChatSummaryResponse;
import com.linlay.agentplatform.model.api.MarkAgentReadRequest;
import com.linlay.agentplatform.model.api.MarkAgentReadResponse;
import com.linlay.agentplatform.model.api.QueryRequest;
import com.linlay.agentplatform.model.api.SubmitRequest;
import com.linlay.agentplatform.model.api.SubmitResponse;
import com.linlay.agentplatform.security.ApiJwtAuthWebFilter;
import com.linlay.agentplatform.security.ChatImageTokenService;
import com.linlay.agentplatform.security.JwksJwtVerifier.JwtPrincipal;
import com.linlay.agentplatform.service.AgentQueryService;
import com.linlay.agentplatform.service.AgentQueryService.QuerySession;
import com.linlay.agentplatform.service.ChatRecordStore;
import com.linlay.agentplatform.service.FrontendSubmitCoordinator;
import com.linlay.agentplatform.service.ViewportRegistryService;
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
import org.springframework.web.server.ServerWebExchange;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.util.List;
import java.util.Map;

@RestController
@RequestMapping("/api/ap")
public class AgentController {

    private static final Logger log = LoggerFactory.getLogger(AgentController.class);

    private final AgentRegistry agentRegistry;
    private final AgentQueryService agentQueryService;
    private final ChatRecordStore chatRecordStore;
    private final SseFlushWriter sseFlushWriter;
    private final ViewportRegistryService viewportRegistryService;
    private final FrontendSubmitCoordinator frontendSubmitCoordinator;
    private final ChatImageTokenService chatImageTokenService;
    private final ObjectMapper objectMapper;

    public AgentController(
            AgentRegistry agentRegistry,
            AgentQueryService agentQueryService,
            ChatRecordStore chatRecordStore,
            SseFlushWriter sseFlushWriter,
            ViewportRegistryService viewportRegistryService,
            FrontendSubmitCoordinator frontendSubmitCoordinator,
            ChatImageTokenService chatImageTokenService,
            ObjectMapper objectMapper
    ) {
        this.agentRegistry = agentRegistry;
        this.agentQueryService = agentQueryService;
        this.chatRecordStore = chatRecordStore;
        this.sseFlushWriter = sseFlushWriter;
        this.viewportRegistryService = viewportRegistryService;
        this.frontendSubmitCoordinator = frontendSubmitCoordinator;
        this.chatImageTokenService = chatImageTokenService;
        this.objectMapper = objectMapper;
    }

    @GetMapping("/agents")
    public ApiResponse<List<AgentListResponse.AgentSummary>> agents(
            @RequestParam(required = false) String tag
    ) {
        List<AgentListResponse.AgentSummary> items = agentRegistry.list().stream()
                .filter(agent -> matchesTag(agent, tag))
                .map(this::toSummary)
                .toList();
        return ApiResponse.success(items);
    }

    @GetMapping("/agent")
    public ApiResponse<AgentDetailResponse.AgentDetail> agent(@RequestParam String agentKey) {
        Agent agent = agentRegistry.get(agentKey);
        return ApiResponse.success(toDetail(agent));
    }

    @GetMapping("/chats")
    public ApiResponse<List<ChatSummaryResponse>> chats() {
        return ApiResponse.success(chatRecordStore.listChats());
    }

    @GetMapping("/agent-chats")
    public ApiResponse<List<AgentChatSummaryResponse>> agentChats(
            @RequestParam(required = false) String sort
    ) {
        return ApiResponse.success(chatRecordStore.listAgentChats(sort));
    }

    @PostMapping("/agent-reads")
    public ApiResponse<MarkAgentReadResponse> markAgentReads(@Valid @RequestBody MarkAgentReadRequest request) {
        ChatRecordStore.MarkReadResult ack = chatRecordStore.markAgentRead(request.agentKey());
        return ApiResponse.success(new MarkAgentReadResponse(
                ack.agentKey(),
                ack.ackedEvents(),
                ack.ackedChats(),
                ack.unreadChatCount()
        ));
    }

    @GetMapping("/chat")
    public ApiResponse<ChatDetailResponse> chat(
            @RequestParam String chatId,
            @RequestParam(defaultValue = "false") boolean includeRawMessages,
            ServerWebExchange exchange
    ) {
        ChatDetailResponse detail = chatRecordStore.loadChat(chatId, includeRawMessages);
        String chatImageToken = issueChatImageToken(resolvePrincipal(exchange), detail.chatId());
        return ApiResponse.success(new ChatDetailResponse(
                detail.chatId(),
                detail.chatName(),
                chatImageToken,
                detail.rawMessages(),
                detail.events(),
                detail.references()
        ));
    }

    @PostMapping(value = "/query", produces = MediaType.TEXT_EVENT_STREAM_VALUE)
    public Mono<Void> query(
            @Valid @RequestBody QueryRequest request,
            ServerHttpResponse response,
            ServerWebExchange exchange
    ) {
        QuerySession session = agentQueryService.prepare(request);
        String chatImageToken = issueChatImageToken(resolvePrincipal(exchange), session.request().chatId());
        Flux<ServerSentEvent<String>> stream = agentQueryService.stream(session);
        if (StringUtils.hasText(chatImageToken)) {
            stream = stream.map(event -> attachChatImageTokenForChatStart(event, chatImageToken));
        }
        return sseFlushWriter.write(response, stream);
    }

    @PostMapping("/submit")
    public ApiResponse<SubmitResponse> submit(@Valid @RequestBody SubmitRequest request) {
        FrontendSubmitCoordinator.SubmitAck ack = frontendSubmitCoordinator.submit(
                request.runId(),
                request.toolId(),
                request.params()
        );
        log.info(
                "Received human-in-the-loop submit runId={}, toolId={}, accepted={}, status={}",
                request.runId(),
                request.toolId(),
                ack.accepted(),
                ack.status()
        );
        return ApiResponse.success(new SubmitResponse(
                ack.accepted(),
                ack.status(),
                request.runId(),
                request.toolId(),
                ack.detail()
        ));
    }

    @GetMapping("/viewport")
    public ResponseEntity<ApiResponse<Object>> viewport(@RequestParam String viewportKey) {
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

    private AgentListResponse.AgentSummary toSummary(Agent agent) {
        return new AgentListResponse.AgentSummary(
                agent.id(),
                agent.name(),
                agent.description(),
                null,
                buildMeta(agent)
        );
    }

    private AgentDetailResponse.AgentDetail toDetail(Agent agent) {
        return new AgentDetailResponse.AgentDetail(
                agent.id(),
                agent.name(),
                agent.description(),
                agent.systemPrompt(),
                null,
                buildMeta(agent)
        );
    }

    private Map<String, Object> buildMeta(Agent agent) {
        Map<String, Object> meta = new java.util.LinkedHashMap<>();
        meta.put("model", agent.model());
        meta.put("mode", agent.mode().name());
        meta.put("icon", agent.icon());
        meta.put("tools", agent.tools());
        meta.put("skills", agent.skills());
        return meta;
    }

    private JwtPrincipal resolvePrincipal(ServerWebExchange exchange) {
        if (exchange == null) {
            return null;
        }
        Object raw = exchange.getAttribute(ApiJwtAuthWebFilter.JWT_PRINCIPAL_ATTR);
        if (raw instanceof JwtPrincipal principal) {
            return principal;
        }
        return null;
    }

    private String issueChatImageToken(JwtPrincipal principal, String chatId) {
        if (principal == null || !StringUtils.hasText(principal.subject()) || !StringUtils.hasText(chatId)) {
            return null;
        }
        return chatImageTokenService.issueToken(principal.subject(), chatId);
    }

    private ServerSentEvent<String> attachChatImageTokenForChatStart(ServerSentEvent<String> event, String chatImageToken) {
        if (event == null || !StringUtils.hasText(event.data())) {
            return event;
        }
        try {
            JsonNode root = objectMapper.readTree(event.data());
            if (!(root instanceof ObjectNode objectNode)) {
                return event;
            }
            if (!"chat.start".equals(objectNode.path("type").asText())) {
                return event;
            }
            objectNode.put("chatImageToken", chatImageToken);
            return rebuildEvent(event, objectNode);
        } catch (Exception ignored) {
            return event;
        }
    }

    private ServerSentEvent<String> rebuildEvent(ServerSentEvent<String> original, ObjectNode data) {
        ServerSentEvent.Builder<String> builder = ServerSentEvent.builder(data.toString());
        if (StringUtils.hasText(original.event())) {
            builder.event(original.event());
        }
        if (StringUtils.hasText(original.id())) {
            builder.id(original.id());
        }
        if (StringUtils.hasText(original.comment())) {
            builder.comment(original.comment());
        }
        if (original.retry() != null) {
            builder.retry(original.retry());
        }
        return builder.build();
    }
}
