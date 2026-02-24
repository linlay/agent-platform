package com.linlay.agentplatform.controller;

import com.linlay.agentplatform.config.DataCatalogProperties;
import com.linlay.agentplatform.model.api.ApiResponse;
import com.linlay.agentplatform.security.ChatImageTokenService;
import com.linlay.agentplatform.security.ChatImageTokenService.VerifyResult;
import com.linlay.agentplatform.service.ChatAssetAccessService;
import com.linlay.agentplatform.service.DataFilePathNormalizer;
import org.springframework.core.io.Resource;
import org.springframework.core.io.UrlResource;
import org.springframework.http.HttpHeaders;
import org.springframework.http.HttpStatus;
import org.springframework.http.MediaType;
import org.springframework.http.ResponseEntity;
import org.springframework.util.StringUtils;
import org.springframework.web.bind.annotation.*;
import reactor.core.publisher.Mono;

import java.net.URLConnection;
import java.net.URLEncoder;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.nio.file.LinkOption;
import java.nio.file.Path;
import java.util.Map;

@RestController
public class DataFileController {

    private static final String CACHE_CONTROL_NO_STORE = "private, no-store";

    private static final Map<String, String> EXTRA_MIME_TYPES = Map.ofEntries(
            Map.entry(".svg", "image/svg+xml"),
            Map.entry(".webp", "image/webp"),
            Map.entry(".avif", "image/avif"),
            Map.entry(".xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"),
            Map.entry(".xls", "application/vnd.ms-excel"),
            Map.entry(".docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"),
            Map.entry(".doc", "application/msword"),
            Map.entry(".pptx", "application/vnd.openxmlformats-officedocument.presentationml.presentation"),
            Map.entry(".ppt", "application/vnd.ms-powerpoint"),
            Map.entry(".csv", "text/csv"),
            Map.entry(".json", "application/json"),
            Map.entry(".md", "text/markdown"),
            Map.entry(".mp4", "video/mp4"),
            Map.entry(".webm", "video/webm"),
            Map.entry(".mp3", "audio/mpeg"),
            Map.entry(".wav", "audio/wav"),
            Map.entry(".woff2", "font/woff2"),
            Map.entry(".woff", "font/woff")
    );

    private final Path dataDir;
    private final ChatImageTokenService chatImageTokenService;
    private final ChatAssetAccessService chatAssetAccessService;

    public DataFileController(
            DataCatalogProperties properties,
            ChatImageTokenService chatImageTokenService,
            ChatAssetAccessService chatAssetAccessService
    ) {
        this.dataDir = Path.of(properties.getExternalDir()).toAbsolutePath().normalize();
        this.chatImageTokenService = chatImageTokenService;
        this.chatAssetAccessService = chatAssetAccessService;
    }

    @GetMapping("/api/ap/data")
    public Mono<ResponseEntity<?>> serveFile(
            @RequestParam("file") String file,
            @RequestParam(value = "download", required = false, defaultValue = "false") boolean download,
            @RequestParam(value = "t", required = false) String chatImageToken
    ) {
        VerifyResult verifyResult = null;
        String filename = DataFilePathNormalizer.normalizeFileParam(file);
        if (filename == null) {
            return Mono.just(jsonResponse(HttpStatus.BAD_REQUEST, ApiResponse.failure(400, "Invalid file parameter")));
        }

        Path filePath = dataDir.resolve(filename).normalize();
        if (!filePath.startsWith(dataDir)) {
            return Mono.just(jsonResponse(HttpStatus.BAD_REQUEST, ApiResponse.failure(400, "Invalid filename")));
        }

        if (chatImageToken != null) {
            verifyResult = chatImageTokenService.verify(chatImageToken);
            if (!verifyResult.valid()) {
                return Mono.just(forbiddenToken(verifyResult.message(), verifyResult.errorCode()));
            }
            if (!verifyResult.hasScope(ChatImageTokenService.DATA_READ_SCOPE)) {
                return Mono.just(forbiddenToken("chat image token invalid", ChatImageTokenService.ERROR_CODE_INVALID));
            }
        }

        if (!Files.isRegularFile(filePath, LinkOption.NOFOLLOW_LINKS)) {
            return Mono.just(jsonResponse(HttpStatus.NOT_FOUND, ApiResponse.failure(404, "File not found")));
        }

        if (verifyResult != null) {
            boolean canRead = chatAssetAccessService.canRead(
                    verifyResult.claims().uid(),
                    verifyResult.claims().chatId(),
                    filename
            );
            if (!canRead) {
                return Mono.just(forbiddenToken("chat image token invalid", ChatImageTokenService.ERROR_CODE_INVALID));
            }
        }

        try {
            return Mono.just(buildFileResponse(filePath, filename, download));
        } catch (Exception e) {
            return Mono.just(jsonResponse(HttpStatus.INTERNAL_SERVER_ERROR, ApiResponse.failure(500, "Failed to read file")));
        }
    }

    private String guessContentType(String filename) {
        int dotIndex = filename.lastIndexOf('.');
        if (dotIndex >= 0) {
            String ext = filename.substring(dotIndex).toLowerCase();
            String extra = EXTRA_MIME_TYPES.get(ext);
            if (extra != null) {
                return extra;
            }
        }
        String guessed = URLConnection.guessContentTypeFromName(filename);
        return guessed != null ? guessed : "application/octet-stream";
    }

    private ResponseEntity<?> buildFileResponse(Path filePath, String filename, boolean download) throws Exception {
        Resource resource = new UrlResource(filePath.toUri());
        String contentType = guessContentType(filename);
        boolean isImage = contentType.startsWith("image/");
        String disposition;
        String encodedFilename = URLEncoder.encode(filename, StandardCharsets.UTF_8).replace("+", "%20");

        if (download || !isImage) {
            disposition = "attachment; filename*=UTF-8''" + encodedFilename;
        } else {
            disposition = "inline; filename*=UTF-8''" + encodedFilename;
        }

        return ResponseEntity.ok()
                .header(HttpHeaders.CONTENT_TYPE, contentType)
                .header(HttpHeaders.CONTENT_DISPOSITION, disposition)
                .header(HttpHeaders.CACHE_CONTROL, CACHE_CONTROL_NO_STORE)
                .body(resource);
    }

    private ResponseEntity<?> forbiddenToken(String message, String errorCode) {
        String resolvedMessage = StringUtils.hasText(message) ? message : "chat image token invalid";
        String resolvedErrorCode = StringUtils.hasText(errorCode)
                ? errorCode
                : ChatImageTokenService.ERROR_CODE_INVALID;
        return jsonResponse(HttpStatus.FORBIDDEN, ApiResponse.failure(
                HttpStatus.FORBIDDEN.value(),
                resolvedMessage,
                Map.of("errorCode", resolvedErrorCode)
        ));
    }

    private ResponseEntity<?> jsonResponse(HttpStatus status, ApiResponse<?> body) {
        return ResponseEntity.status(status)
                .contentType(MediaType.APPLICATION_JSON)
                .header(HttpHeaders.CACHE_CONTROL, CACHE_CONTROL_NO_STORE)
                .body(body);
    }
}
