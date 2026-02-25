package com.linlay.agentplatform.service;

import com.fasterxml.jackson.databind.JsonNode;
import com.fasterxml.jackson.databind.ObjectMapper;
import com.linlay.agentplatform.config.FrontendToolProperties;
import com.linlay.agentplatform.model.ViewportType;
import com.linlay.agentplatform.tool.CapabilityDescriptor;
import com.linlay.agentplatform.tool.CapabilityKind;
import com.linlay.agentplatform.tool.ToolRegistry;
import org.junit.jupiter.api.Test;
import org.springframework.http.codec.ServerSentEvent;
import org.springframework.test.util.ReflectionTestUtils;

import java.time.Duration;
import java.util.Map;
import java.util.Optional;

import static org.assertj.core.api.Assertions.assertThat;
import static org.mockito.Mockito.mock;
import static org.mockito.Mockito.when;

class AgentQueryServiceTest {

    private final ObjectMapper objectMapper = new ObjectMapper();

    @Test
    void normalizeEventShouldConvertHeartbeatCommentToEventWithoutData() {
        AgentQueryService service = newService();
        ServerSentEvent<String> event = ServerSentEvent.<String>builder()
                .comment("heartbeat")
                .id("hb_1")
                .retry(Duration.ofSeconds(3))
                .build();

        ServerSentEvent<String> normalized = ReflectionTestUtils.invokeMethod(service, "normalizeEvent", event);

        assertThat(normalized.event()).isEqualTo("heartbeat");
        assertThat(normalized.data()).isNull();
        assertThat(normalized.comment()).isNull();
        assertThat(normalized.id()).isEqualTo("hb_1");
        assertThat(normalized.retry()).isEqualTo(Duration.ofSeconds(3));
    }

    @Test
    void normalizeEventShouldKeepNonHeartbeatCommentUnchanged() {
        AgentQueryService service = newService();
        ServerSentEvent<String> event = ServerSentEvent.<String>builder()
                .comment("keepalive")
                .build();

        ServerSentEvent<String> normalized = ReflectionTestUtils.invokeMethod(service, "normalizeEvent", event);

        assertThat(normalized).isSameAs(event);
    }

    @Test
    void normalizeEventShouldKeepNormalDataEventUnchanged() {
        AgentQueryService service = newService();
        String payload = "{\"type\":\"content.delta\",\"delta\":\"hello\"}";
        ServerSentEvent<String> event = ServerSentEvent.builder(payload)
                .event("message")
                .build();

        ServerSentEvent<String> normalized = ReflectionTestUtils.invokeMethod(service, "normalizeEvent", event);

        assertThat(normalized).isSameAs(event);
        assertThat(normalized.data()).isEqualTo(payload);
        assertThat(normalized.event()).isEqualTo("message");
    }

    @Test
    void normalizeEventShouldInjectFrontendFieldsForToolStart() throws Exception {
        ToolRegistry toolRegistry = mock(ToolRegistry.class);
        ViewportRegistryService viewportRegistryService = mock(ViewportRegistryService.class);
        FrontendToolProperties frontendToolProperties = new FrontendToolProperties();
        frontendToolProperties.setSubmitTimeoutMs(120_000);

        when(toolRegistry.capability("confirm_dialog")).thenReturn(Optional.of(frontendCapability()));
        when(viewportRegistryService.find("confirm_dialog")).thenReturn(Optional.of(
                new ViewportRegistryService.ViewportEntry("confirm_dialog", ViewportType.HTML, "<div>ok</div>")
        ));

        AgentQueryService service = new AgentQueryService(
                null,
                null,
                objectMapper,
                null,
                toolRegistry,
                viewportRegistryService,
                frontendToolProperties
        );
        ServerSentEvent<String> event = ServerSentEvent.builder("""
                {"type":"tool.start","toolName":"confirm_dialog","toolId":"call_1","runId":"run_1"}
                """).build();

        ServerSentEvent<String> normalized = ReflectionTestUtils.invokeMethod(service, "normalizeEvent", event);
        JsonNode payload = objectMapper.readTree(normalized.data());

        assertThat(payload.path("type").asText()).isEqualTo("tool.start");
        assertThat(payload.path("toolType").asText()).isEqualTo("html");
        assertThat(payload.path("toolKey").asText()).isEqualTo("confirm_dialog");
        assertThat(payload.path("toolTimeout").asLong()).isEqualTo(120_000L);
    }

    @Test
    void normalizeEventShouldInjectFrontendFieldsForToolSnapshot() throws Exception {
        ToolRegistry toolRegistry = mock(ToolRegistry.class);
        ViewportRegistryService viewportRegistryService = mock(ViewportRegistryService.class);
        FrontendToolProperties frontendToolProperties = new FrontendToolProperties();
        frontendToolProperties.setSubmitTimeoutMs(30_000);

        when(toolRegistry.capability("confirm_dialog")).thenReturn(Optional.of(frontendCapability()));
        when(viewportRegistryService.find("confirm_dialog")).thenReturn(Optional.of(
                new ViewportRegistryService.ViewportEntry("confirm_dialog", ViewportType.QLC, Map.of("schema", Map.of()))
        ));

        AgentQueryService service = new AgentQueryService(
                null,
                null,
                objectMapper,
                null,
                toolRegistry,
                viewportRegistryService,
                frontendToolProperties
        );
        ServerSentEvent<String> event = ServerSentEvent.builder("""
                {"type":"tool.snapshot","toolName":"confirm_dialog","toolId":"call_2","runId":"run_2"}
                """).build();

        ServerSentEvent<String> normalized = ReflectionTestUtils.invokeMethod(service, "normalizeEvent", event);
        JsonNode payload = objectMapper.readTree(normalized.data());

        assertThat(payload.path("type").asText()).isEqualTo("tool.snapshot");
        assertThat(payload.path("toolType").asText()).isEqualTo("qlc");
        assertThat(payload.path("toolKey").asText()).isEqualTo("confirm_dialog");
        assertThat(payload.path("toolTimeout").asLong()).isEqualTo(30_000L);
    }

    private CapabilityDescriptor frontendCapability() {
        return new CapabilityDescriptor(
                "confirm_dialog",
                "confirm",
                "",
                Map.of("type", "object"),
                false,
                CapabilityKind.FRONTEND,
                "frontend",
                null,
                "confirm_dialog",
                "/tmp/confirm_dialog.frontend"
        );
    }

    private AgentQueryService newService() {
        return new AgentQueryService(
                null,
                null,
                objectMapper,
                null,
                mock(ToolRegistry.class),
                mock(ViewportRegistryService.class),
                new FrontendToolProperties()
        );
    }
}
