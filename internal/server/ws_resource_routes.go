package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	neturl "net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"agent-platform/internal/api"
	"agent-platform/internal/config"
	"agent-platform/internal/ws"
)

// wsDownload 处理网关通过 WS /api/upload 通知 platform "有一份用户传到企微仓库的文件、
// 请按 upload.url 拉取" 的场景。upload.url 可以指向网关 HTTP /api/pull/...。
// 接受两种 payload 形状：
//
//   - nested（网关当前使用的形状）：
//     {requestId, chatId, upload:{id,type,name,mimeType,sizeBytes,sha256,url?}}
//   - flat：{chatId, requestId, fileName, sha256?, url?, mimeType?, sizeBytes?}
//
// 下载 key 由网关在 upload.url 中下发。下载完的字节复用 /api/upload
// 内部管线落盘到 {ChatsDir}/{chatId}/，sandbox 会把该目录挂进容器 /workspace。
func (s *Server) wsDownload(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payloadData, statusErr := s.rewriteChannelRequestPayload(ctx, req.Type, req.Payload)
	if statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	req.Payload = payloadData
	payload, err := ws.DecodePayload[struct {
		ChatID    string `json:"chatId"`
		RequestID string `json:"requestId"`
		FileName  string `json:"fileName"`
		MimeType  string `json:"mimeType"`
		SizeBytes int64  `json:"sizeBytes"`
		URL       string `json:"url"`
		SHA256    string `json:"sha256"`
		Upload    struct {
			ID        string `json:"id"`
			Type      string `json:"type"`
			Name      string `json:"name"`
			MimeType  string `json:"mimeType"`
			SizeBytes int64  `json:"sizeBytes"`
			URL       string `json:"url"`
			SHA256    string `json:"sha256"`
		} `json:"upload"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid upload payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}

	chatID := strings.TrimSpace(payload.ChatID)
	requestID := strings.TrimSpace(payload.RequestID)
	fileName := strings.TrimSpace(payload.Upload.Name)
	mimeType := strings.TrimSpace(payload.Upload.MimeType)
	sizeBytes := payload.Upload.SizeBytes
	sha256Value := strings.TrimSpace(payload.Upload.SHA256)
	// 契约：网关在 upload.url 里下发完整 https://.../api/pull/...?ticket=... URL。
	// platform 直接用它发 HTTP GET，不做路径拼接、不做猜测。
	rawURL := strings.TrimSpace(payload.Upload.URL)
	if fileName == "" {
		fileName = strings.TrimSpace(payload.FileName)
	}
	if mimeType == "" {
		mimeType = strings.TrimSpace(payload.MimeType)
	}
	if sizeBytes == 0 {
		sizeBytes = payload.SizeBytes
	}
	if sha256Value == "" {
		sha256Value = strings.TrimSpace(payload.SHA256)
	}
	if rawURL == "" {
		rawURL = strings.TrimSpace(payload.URL)
	}
	if chatID == "" || fileName == "" || rawURL == "" {
		log.Printf("[ws-download] reject: missing fields chatId=%q fileName=%q url=%q rawPayload=%s",
			chatID, fileName, rawURL, string(req.Payload))
		conn.SendError(req.ID, "invalid_request", 400, "chatId, fileName and upload.url are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	log.Printf("[ws-download] recv chatId=%s requestId=%s fileName=%s size=%d",
		chatID, requestID, fileName, sizeBytes)

	data, err := s.fetchGatewayDownload(ctx, chatID, rawURL)
	if err != nil {
		log.Printf("[ws-download] fetch failed chatId=%s url=%s err=%v", chatID, rawURL, err)
		conn.SendError(req.ID, "download_failed", 502, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if err := validateDownloadedUpload(data, sizeBytes, sha256Value); err != nil {
		log.Printf("[ws-download] invalid metadata chatId=%s fileName=%s err=%v", chatID, fileName, err)
		conn.SendError(req.ID, "invalid_upload_metadata", 400, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	log.Printf("[ws-download] ok chatId=%s fileName=%s bytes=%d", chatID, fileName, len(data))

	status, body, err := s.ExecuteInternalUpload(ctx, chatID, requestID, fileName, mimeType, data)
	if err != nil {
		conn.SendError(req.ID, "internal_error", 500, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	if status < 200 || status >= 300 {
		conn.SendError(req.ID, "upload_failed", status, strings.TrimSpace(string(body)), nil)
		conn.CompleteRequest(req.ID)
		return
	}

	var parsed api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(body, &parsed); err != nil || parsed.Data.Upload.Name == "" {
		conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{"raw": string(body)})
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", parsed.Data)
	conn.CompleteRequest(req.ID)
}

// wsResource 处理 WS /api/resource 控制帧：gateway 要求 platform 将本地资源
// 通过 HTTP POST 推送到 pushURL。pushURL 通常指向 gateway HTTP /api/push/...。
func (s *Server) wsResource(ctx context.Context, conn *ws.Conn, req ws.RequestFrame) {
	payloadData, statusErr := s.rewriteChannelRequestPayload(ctx, req.Type, req.Payload)
	if statusErr != nil {
		s.sendWSStatusError(conn, req.ID, statusErr)
		conn.CompleteRequest(req.ID)
		return
	}
	req.Payload = payloadData
	payload, err := ws.DecodePayload[struct {
		File    string `json:"file"`
		PushURL string `json:"pushURL"`
	}](req)
	if err != nil {
		conn.SendError(req.ID, "invalid_request", 400, "invalid resource payload", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	fileParam := strings.TrimSpace(payload.File)
	pushURL := strings.TrimSpace(payload.PushURL)
	if err := validateWSResourceFileParam(fileParam); err != nil || pushURL == "" {
		conn.SendError(req.ID, "invalid_request", 400, "file and pushURL are required", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	chatID := resourceChatID(fileParam)
	if chatID == "" {
		conn.SendError(req.ID, "invalid_request", 400, "file must include chatId", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	resourcePath, err := s.deps.Chats.ResolveResource(fileParam)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			conn.SendError(req.ID, "resource_forbidden", 403, "resource access denied", nil)
		} else {
			conn.SendError(req.ID, "resource_not_found", 404, "resource not found", nil)
		}
		conn.CompleteRequest(req.ID)
		return
	}
	file, err := os.Open(resourcePath)
	if err != nil {
		conn.SendError(req.ID, "resource_not_found", 404, "resource not found", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	defer file.Close()
	mimeType := detectResourceMIME(resourcePath, file)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		conn.SendError(req.ID, "resource_read_failed", 500, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	baseURL, token := s.resolveGatewayForChat(chatID)
	if gateway, ok := ws.GatewayFromContext(ctx); ok {
		baseURL = strings.TrimSpace(gateway.BaseURL)
		token = strings.TrimSpace(gateway.Token)
	}
	uploadURL := s.buildGatewayPushURL(baseURL, pushURL)
	if uploadURL == "" {
		conn.SendError(req.ID, "invalid_request", 400, "empty pushURL", nil)
		conn.CompleteRequest(req.ID)
		return
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadURL, file)
	if err != nil {
		conn.SendError(req.ID, "resource_push_failed", 500, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	httpReq.Header.Set("Content-Type", mimeType)
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		conn.SendError(req.ID, "resource_push_failed", 502, err.Error(), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		conn.SendError(req.ID, "resource_push_failed", resp.StatusCode, strings.TrimSpace(string(body)), nil)
		conn.CompleteRequest(req.ID)
		return
	}
	conn.SendResponse(req.Type, req.ID, 0, "success", map[string]any{
		"file":     fileParam,
		"mimeType": mimeType,
		"status":   resp.StatusCode,
	})
	conn.CompleteRequest(req.ID)
}

func validateWSResourceFileParam(fileParam string) error {
	if fileParam == "" || strings.Contains(fileParam, "\x00") || strings.Contains(fileParam, "\\") || strings.HasPrefix(fileParam, "/") {
		return fmt.Errorf("invalid file path")
	}
	clean := path.Clean(fileParam)
	if clean == "." || clean != fileParam || clean == ".." || strings.HasPrefix(clean, "../") || strings.Contains(clean, "/../") {
		return fmt.Errorf("unsafe file path")
	}
	return nil
}

func resourceChatID(fileParam string) string {
	fileParam = strings.Trim(fileParam, "/")
	if fileParam == "" {
		return ""
	}
	parts := strings.SplitN(fileParam, "/", 2)
	return strings.TrimSpace(parts[0])
}

func detectResourceMIME(resourcePath string, file *os.File) string {
	if resourcePath != "" {
		if mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(resourcePath))); mimeType != "" {
			return mimeType
		}
	}
	if file != nil {
		var head [512]byte
		n, _ := file.Read(head[:])
		if n > 0 {
			return http.DetectContentType(head[:n])
		}
	}
	return "application/octet-stream"
}

// fetchGatewayDownload 把下载 key（绝对 URL 或仅 sha256）解析成完整的
// gateway BaseURL/api/pull/... 路径，带对应 gateway 的 JWT Bearer 做 GET。
// chatID 用于按前缀路由到正确的 gateway（多 channel 部署下必需）。
func (s *Server) fetchGatewayDownload(ctx context.Context, chatID string, rawURL string) ([]byte, error) {
	baseURL, token := s.resolveGatewayForChat(chatID)
	downloadURL := s.buildGatewayURL(baseURL, rawURL)
	if downloadURL == "" {
		return nil, fmt.Errorf("empty download url")
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("http get: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("download status=%d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 100*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return data, nil
}

// resolveGatewayForChat 按 chatId 查对应 gateway 的 BaseURL/Token。
func (s *Server) resolveGatewayForChat(chatID string) (baseURL string, token string) {
	if s.deps.GatewayResolver != nil {
		if b, t, ok := s.deps.GatewayResolver.Resolve(chatID); ok {
			return b, t
		}
	}
	return "", ""
}

// buildGatewayURL 把网关下发的下载地址规范化到指定 baseURL。
// 不管网关填什么 host（空 / localhost / 外网 IP），platform 都**强制**
// 改用 baseURL 作为 scheme+host，只保留 path + query。
// 这样跨机部署时不会因为网关那端写死 localhost 而打不到。
func (s *Server) buildGatewayURL(base string, raw string) string {
	return s.buildGatewayURLWithPath(base, raw, config.GatewayDownloadPath)
}

// buildGatewayPushURL 把 gateway 的推送地址规范化到指定 baseURL。
// 裸 token 会按 HTTP /api/push/... 拼接；显式路径和完整 URL 保留 path + query。
func (s *Server) buildGatewayPushURL(base string, raw string) string {
	return s.buildGatewayURLWithPath(base, raw, config.GatewayUploadPath)
}

func (s *Server) buildGatewayURLWithPath(base string, raw string, defaultPath string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return raw
	}

	// 解析出 path + query（raw 可能是完整 URL、相对路径、或裸 token）
	var pathAndQuery string
	if parsed, err := neturl.Parse(raw); err == nil && parsed.Path != "" {
		pathAndQuery = parsed.EscapedPath()
		if parsed.RawQuery != "" {
			pathAndQuery += "?" + parsed.RawQuery
		}
	} else {
		pathAndQuery = raw
	}

	if strings.HasPrefix(pathAndQuery, "/") {
		return base + pathAndQuery
	}
	defaultPath = strings.Trim(defaultPath, "/")
	if defaultPath == "" {
		return base + "/" + pathAndQuery
	}
	return base + "/" + defaultPath + "/" + pathAndQuery
}

func validateDownloadedUpload(data []byte, expectedSize int64, expectedSHA256 string) error {
	if expectedSize < 0 {
		return fmt.Errorf("sizeBytes must be >= 0")
	}
	if expectedSize > 0 && int64(len(data)) != expectedSize {
		return fmt.Errorf("sizeBytes mismatch: expected %d got %d", expectedSize, len(data))
	}
	expectedSHA256 = strings.TrimSpace(expectedSHA256)
	if expectedSHA256 == "" {
		return nil
	}
	sum := sha256.Sum256(data)
	actual := hex.EncodeToString(sum[:])
	if !strings.EqualFold(actual, expectedSHA256) {
		return fmt.Errorf("sha256 mismatch: expected %s got %s", strings.ToLower(expectedSHA256), actual)
	}
	return nil
}
