package com.linlay.agentplatform.controller;

import com.linlay.agentplatform.service.LlmService;
import org.junit.jupiter.api.BeforeEach;
import org.junit.jupiter.api.Test;
import org.springframework.beans.factory.annotation.Autowired;
import org.springframework.boot.test.autoconfigure.web.reactive.AutoConfigureWebTestClient;
import org.springframework.boot.test.context.SpringBootTest;
import org.springframework.boot.test.context.TestConfiguration;
import org.springframework.context.annotation.Bean;
import org.springframework.context.annotation.Import;
import org.springframework.context.annotation.Primary;
import org.springframework.http.HttpHeaders;
import org.springframework.test.web.reactive.server.WebTestClient;
import org.springframework.web.util.UriComponentsBuilder;
import reactor.core.publisher.Flux;
import reactor.core.publisher.Mono;

import java.io.IOException;
import java.nio.file.Files;
import java.nio.file.Path;

import static org.assertj.core.api.Assertions.assertThat;

@SpringBootTest(
        webEnvironment = SpringBootTest.WebEnvironment.MOCK,
        properties = {
                "agent.providers.bailian.base-url=https://example.com/v1",
                "agent.providers.bailian.api-key=test-bailian-key",
                "agent.providers.bailian.model=test-bailian-model",
                "agent.providers.siliconflow.base-url=https://example.com/v1",
                "agent.providers.siliconflow.api-key=test-siliconflow-key",
                "agent.providers.siliconflow.model=test-siliconflow-model",
                "agent.auth.enabled=false",
                "memory.chat.dir=${java.io.tmpdir}/springai-agent-platform-test-data-chats-${random.uuid}",
                "memory.chat.index.sqlite-file=${java.io.tmpdir}/springai-agent-platform-test-data-chats-db-${random.uuid}/chats.db",
                "agent.viewport.external-dir=${java.io.tmpdir}/springai-agent-platform-test-data-viewports-${random.uuid}",
                "agent.capability.tools-external-dir=${java.io.tmpdir}/springai-agent-platform-test-data-tools-${random.uuid}",
                "agent.skill.external-dir=${java.io.tmpdir}/springai-agent-platform-test-data-skills-${random.uuid}",
                "agent.data.external-dir=${java.io.tmpdir}/springai-agent-platform-test-datafiles-${random.uuid}"
        }
)
@AutoConfigureWebTestClient
@Import(DataFileControllerTest.TestLlmServiceConfig.class)
class DataFileControllerTest {

    @Autowired
    private WebTestClient webTestClient;

    @Autowired
    private com.linlay.agentplatform.config.DataCatalogProperties dataCatalogProperties;

    @TestConfiguration
    static class TestLlmServiceConfig {
        @Bean
        @Primary
        LlmService llmService() {
            return new LlmService(null, null) {
                @Override
                public Flux<String> streamContent(String providerKey, String model, String systemPrompt, String userPrompt) {
                    return Flux.just("test");
                }

                @Override
                public Flux<String> streamContent(String providerKey, String model, String systemPrompt, String userPrompt, String stage) {
                    return streamContent(providerKey, model, systemPrompt, userPrompt);
                }

                @Override
                public Mono<String> completeText(String providerKey, String model, String systemPrompt, String userPrompt) {
                    return Mono.just("{}");
                }

                @Override
                public Mono<String> completeText(String providerKey, String model, String systemPrompt, String userPrompt, String stage) {
                    return completeText(providerKey, model, systemPrompt, userPrompt);
                }
            };
        }
    }

    @BeforeEach
    void setUp() throws IOException {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        Files.createDirectories(dataDir);
    }

    @Test
    void shouldServePngImageInline() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        // Minimal 1x1 PNG
        byte[] png = createMinimalPng();
        Files.write(dataDir.resolve("aaa.jpg"), png);

        webTestClient.get()
                .uri(dataApiUri("aaa.jpg"))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "image/jpeg")
                .expectHeader().value(HttpHeaders.CONTENT_DISPOSITION, value ->
                        assertThat(value).startsWith("inline"));
    }

    @Test
    void shouldServeImageFromDataPrefixedPath() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        byte[] png = createMinimalPng();
        Files.createDirectories(dataDir.resolve("data"));
        Files.write(dataDir.resolve("data").resolve("sample_photo.jpg"), png);

        webTestClient.get()
                .uri(dataApiUri("/data/sample_photo.jpg"))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "image/jpeg")
                .expectHeader().value(HttpHeaders.CONTENT_DISPOSITION, value ->
                        assertThat(value).startsWith("inline"));
    }

    @Test
    void shouldServeImageWithEncodedFileParam() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        byte[] png = createMinimalPng();
        Files.createDirectories(dataDir.resolve("data"));
        Files.write(dataDir.resolve("data").resolve("encoded_photo.jpg"), png);

        webTestClient.get()
                .uri(dataApiUri("/data/encoded_photo.jpg"))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "image/jpeg");
    }

    @Test
    void shouldServePdfAsAttachment() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        Files.writeString(dataDir.resolve("report.pdf"), "%PDF-1.4 minimal");

        webTestClient.get()
                .uri(dataApiUri("report.pdf"))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "application/pdf")
                .expectHeader().value(HttpHeaders.CONTENT_DISPOSITION, value ->
                        assertThat(value).startsWith("attachment"));
    }

    @Test
    void shouldReturn404WhenFileNotFound() {
        webTestClient.get()
                .uri(dataApiUri("nonexistent.txt"))
                .exchange()
                .expectStatus().isNotFound()
                .expectBody()
                .jsonPath("$.code").isEqualTo(404)
                .jsonPath("$.msg").isEqualTo("File not found");
    }

    @Test
    void shouldReturn400ForPathTraversal() {
        webTestClient.get()
                .uri(dataApiUri("../application.yml"))
                .exchange()
                .expectStatus().isBadRequest()
                .expectBody()
                .jsonPath("$.code").isEqualTo(400);
    }

    @Test
    void shouldReturn400ForFilenameThatIsDotDot() {
        webTestClient.get()
                .uri(dataApiUri(".."))
                .exchange()
                .expectStatus().isBadRequest()
                .expectBody()
                .jsonPath("$.code").isEqualTo(400);
    }

    @Test
    void shouldServeImageFromSubDirectoryPath() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        byte[] png = createMinimalPng();
        Files.createDirectories(dataDir.resolve("data").resolve("sub"));
        Files.write(dataDir.resolve("data").resolve("sub").resolve("a.png"), png);

        webTestClient.get()
                .uri(dataApiUri("/data/sub/a.png"))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "image/png");
    }

    @Test
    void shouldReturn400WhenFileParamIsEmpty() {
        webTestClient.get()
                .uri(dataApiUri(""))
                .exchange()
                .expectStatus().isBadRequest()
                .expectBody()
                .jsonPath("$.code").isEqualTo(400);
    }

    @Test
    void shouldReturn400WhenFileParamEscapesWithEncodedTraversal() {
        webTestClient.get()
                .uri("/api/ap/data?file=..%2Fa.png")
                .exchange()
                .expectStatus().isBadRequest()
                .expectBody()
                .jsonPath("$.code").isEqualTo(400);
    }

    @Test
    void shouldForceDownloadWhenParameterIsTrue() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        byte[] png = createMinimalPng();
        Files.write(dataDir.resolve("sample_photo.jpg"), png);

        webTestClient.get()
                .uri(uriBuilder -> uriBuilder
                        .path("/api/ap/data")
                        .queryParam("file", "sample_photo.jpg")
                        .queryParam("download", "true")
                        .build())
                .exchange()
                .expectStatus().isOk()
                .expectHeader().value(HttpHeaders.CONTENT_DISPOSITION, value ->
                        assertThat(value).startsWith("attachment"));
    }

    @Test
    void shouldServeCsvAsAttachment() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        Files.writeString(dataDir.resolve("data.csv"), "name,value\nfoo,1\nbar,2\n");

        webTestClient.get()
                .uri(dataApiUri("data.csv"))
                .exchange()
                .expectStatus().isOk()
                .expectHeader().valueEquals(HttpHeaders.CONTENT_TYPE, "text/csv")
                .expectHeader().value(HttpHeaders.CONTENT_DISPOSITION, value ->
                        assertThat(value).startsWith("attachment"));
    }

    @Test
    void shouldReturn404ForLegacyApiPath() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        byte[] png = createMinimalPng();
        Files.write(dataDir.resolve("legacy_image.png"), png);

        webTestClient.get()
                .uri("/api/ap/data/legacy_image.png")
                .exchange()
                .expectStatus().isNotFound();
    }

    @Test
    void shouldReturn404ForDeprecatedDataPath() throws Exception {
        Path dataDir = Path.of(dataCatalogProperties.getExternalDir());
        byte[] png = createMinimalPng();
        Files.write(dataDir.resolve("legacy_path_image.png"), png);

        webTestClient.get()
                .uri("/data/legacy_path_image.png")
                .exchange()
                .expectStatus().isNotFound();
    }

    private String dataApiUri(String file) {
        return UriComponentsBuilder.fromPath("/api/ap/data")
                .queryParam("file", file)
                .build()
                .encode()
                .toUriString();
    }

    private byte[] createMinimalPng() {
        // Minimal valid 1x1 red pixel PNG
        return new byte[]{
                (byte) 0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
                0x00, 0x00, 0x00, 0x0D, 0x49, 0x48, 0x44, 0x52, // IHDR chunk
                0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, // 1x1
                0x08, 0x02, 0x00, 0x00, 0x00, (byte) 0x90, 0x77, 0x53,
                (byte) 0xDE, 0x00, 0x00, 0x00, 0x0C, 0x49, 0x44, 0x41, // IDAT chunk
                0x54, 0x08, (byte) 0xD7, 0x63, (byte) 0xF8, (byte) 0xCF, (byte) 0xC0, 0x00,
                0x00, 0x00, 0x02, 0x00, 0x01, (byte) 0xE2, 0x21, (byte) 0xBC,
                0x33, 0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4E, // IEND chunk
                0x44, (byte) 0xAE, 0x42, 0x60, (byte) 0x82
        };
    }
}
