package gatewaybridge

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"agent-platform-runner-go/internal/api"

	gws "github.com/gorilla/websocket"
)

// QueryRunner 是 gatewaybridge 对 server 的最小依赖面，
// 由 server.Server 满足。
type QueryRunner interface {
	ExecuteInternalQueryStream(
		ctx context.Context,
		req api.QueryRequest,
		onEvent func(eventJSON []byte) error,
	) error
	ExecuteInternalSubmit(ctx context.Context, req api.SubmitRequest) (int, []byte, error)
	ExecuteInternalUpload(ctx context.Context, chatID, requestID, fileName string, fileData []byte) (int, []byte, error)
	ResolveResourcePath(fileParam string) (string, error)
}

type Config struct {
	URL              string
	UserID           string
	Ticket           string
	AgentKey         string
	Channel          string
	DefaultAgentKey  string
	OverrideAgentKey string
	HandshakeTimeout time.Duration
	ReconnectMin     time.Duration
	ReconnectMax     time.Duration

	// 网关 HTTP 旁路配置（artifact 外发 + userUpload 下载）。
	// 任一为空时对应能力自动跳过。
	BaseURL      string
	UploadPath   string
	DownloadPath string
	AuthToken    string
}

// buildDialURL 和 agent-wecom-ws-bridge BuildGatewayURL 完全一致：
// 不 URL encode，按 userId/ticket/agentKey/channel 顺序拼接。
func (c Config) buildDialURL() string {
	base := strings.TrimRight(c.URL, "/")
	if base == "" {
		return base
	}
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	var b strings.Builder
	b.WriteString(base)
	appendParam := func(key, value string) {
		if value == "" {
			return
		}
		b.WriteString(sep)
		b.WriteString(key)
		b.WriteString("=")
		b.WriteString(value)
		sep = "&"
	}
	appendParam("userId", c.UserID)
	appendParam("ticket", c.Ticket)
	appendParam("agentKey", c.AgentKey)
	appendParam("channel", c.Channel)
	return b.String()
}

type Client struct {
	cfg    Config
	runner QueryRunner

	writeMu    sync.Mutex
	connMu     sync.Mutex
	activeConn *gws.Conn

	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}

	curSocket atomic.Pointer[gws.Conn]

	startOnce sync.Once
	stopOnce  sync.Once
	rng       *rand.Rand
	rngMu     sync.Mutex

	httpClient *http.Client

	seenMu sync.Mutex
	seen   map[string]time.Time
}

func New(cfg Config, runner QueryRunner) *Client {
	if cfg.HandshakeTimeout <= 0 {
		cfg.HandshakeTimeout = 10 * time.Second
	}
	if cfg.ReconnectMin <= 0 {
		cfg.ReconnectMin = time.Second
	}
	if cfg.ReconnectMax <= 0 {
		cfg.ReconnectMax = 30 * time.Second
	}
	if cfg.ReconnectMax < cfg.ReconnectMin {
		cfg.ReconnectMax = cfg.ReconnectMin
	}
	return &Client{
		cfg:        cfg,
		runner:     runner,
		done:       make(chan struct{}),
		rng:        rand.New(rand.NewSource(time.Now().UnixNano())),
		httpClient: &http.Client{Timeout: 60 * time.Second},
		seen:       make(map[string]time.Time),
	}
}

func (c *Client) Start(parent context.Context) {
	if c == nil {
		return
	}
	c.startOnce.Do(func() {
		if parent == nil {
			parent = context.Background()
		}
		c.ctx, c.cancel = context.WithCancel(parent)
		log.Printf("gatewaybridge starting: url=%s", c.cfg.URL)
		go c.run()
	})
}

func (c *Client) Stop() error {
	if c == nil {
		return nil
	}
	c.stopOnce.Do(func() {
		started := true
		c.startOnce.Do(func() { started = false })
		if !started {
			close(c.done)
			return
		}
		if c.cancel != nil {
			c.cancel()
		}
		if sock := c.curSocket.Load(); sock != nil {
			_ = sock.Close()
		}
		<-c.done
	})
	return nil
}

// Broadcast 实现 schedule.Broadcaster 接口；仅处理 schedule.push 类型，
// 其他类型（如 webclient 订阅的 run.started / chat.updated）不往网关送。
func (c *Client) Broadcast(eventType string, data map[string]any) {
	if c == nil {
		return
	}
	if eventType != "schedule.push" {
		return
	}
	targetID, _ := data["targetId"].(string)
	markdown, _ := data["markdown"].(string)
	if strings.TrimSpace(targetID) == "" || strings.TrimSpace(markdown) == "" {
		return
	}
	if err := c.WritePush(targetID, markdown); err != nil {
		log.Printf("gatewaybridge push failed targetId=%s err=%v", targetID, err)
	} else {
		log.Printf("gatewaybridge push ok targetId=%s markdownLen=%d", targetID, len(markdown))
	}
}

// WritePush 对外暴露 agentPush 写入能力，供 HTTP /api/push 调试端点复用。
// 沿用 wecom-bridge `Bridge.Push` 语义。无活跃连接时返回错误。
func (c *Client) WritePush(targetID, markdown string) error {
	conn := c.getActiveConn()
	if conn == nil {
		return fmt.Errorf("no active gateway connection")
	}
	body, err := json.Marshal(AgentPushBody{Markdown: markdown})
	if err != nil {
		return err
	}
	return c.writeJSON(conn, WSMessage{
		Cmd:      CmdAgentPush,
		TargetID: targetID,
		Body:     body,
	})
}

func (c *Client) writeResponse(conn *gws.Conn, requestID string, body json.RawMessage) error {
	return c.writeJSON(conn, WSMessage{
		Cmd:       CmdAgentResponse,
		RequestID: requestID,
		Body:      body,
	})
}

// writeJSON 保证单写者：gorilla/websocket 的 WriteJSON 不是并发安全的，
// 多个 handleUserMessage goroutine 可能同时写回同一个 socket。
func (c *Client) writeJSON(conn *gws.Conn, msg WSMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(msg)
}

func (c *Client) getActiveConn() *gws.Conn {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	return c.activeConn
}

func (c *Client) setActiveConn(conn *gws.Conn) {
	c.connMu.Lock()
	defer c.connMu.Unlock()
	c.activeConn = conn
}

// dedup 返回 true 表示 requestId 在最近 5 分钟内出现过，应跳过。
func (c *Client) dedup(requestID string) bool {
	if requestID == "" {
		return false
	}
	c.seenMu.Lock()
	defer c.seenMu.Unlock()
	if _, ok := c.seen[requestID]; ok {
		return true
	}
	c.seen[requestID] = time.Now()
	for k, t := range c.seen {
		if time.Since(t) > 5*time.Minute {
			delete(c.seen, k)
		}
	}
	return false
}

func (c *Client) run() {
	defer close(c.done)

	backoff := c.cfg.ReconnectMin
	for {
		if c.ctx.Err() != nil {
			log.Printf("gatewaybridge stopped: url=%s", c.cfg.URL)
			return
		}

		dialURL := c.cfg.buildDialURL()
		dialer := &gws.Dialer{HandshakeTimeout: c.cfg.HandshakeTimeout}
		dialCtx, cancel := context.WithTimeout(c.ctx, c.cfg.HandshakeTimeout)
		socket, resp, err := dialer.DialContext(dialCtx, dialURL, nil)
		cancel()
		if err != nil {
			if resp != nil {
				log.Printf("gatewaybridge handshake failed: url=%s status=%d err=%v", dialURL, resp.StatusCode, err)
			} else {
				log.Printf("gatewaybridge handshake failed: url=%s err=%v", dialURL, err)
			}
			delay := c.jitter(backoff)
			log.Printf("gatewaybridge reconnect scheduled: delay=%s", delay.Round(time.Millisecond))
			if !c.sleep(delay) {
				return
			}
			backoff = c.nextBackoff(backoff)
			continue
		}

		c.curSocket.Store(socket)
		c.setActiveConn(socket)
		if c.ctx.Err() != nil {
			c.curSocket.Store(nil)
			c.setActiveConn(nil)
			_ = socket.Close()
			return
		}

		log.Printf("gatewaybridge connected: url=%s", dialURL)
		startedAt := time.Now()
		c.handleConn(socket)

		c.setActiveConn(nil)
		c.curSocket.Store(nil)
		_ = socket.Close()

		if c.ctx.Err() != nil {
			return
		}
		uptime := time.Since(startedAt)
		log.Printf("gatewaybridge disconnected: uptime=%s", uptime.Round(time.Millisecond))
		if uptime >= c.cfg.ReconnectMax {
			backoff = c.cfg.ReconnectMin
		}
		delay := c.jitter(backoff)
		if !c.sleep(delay) {
			return
		}
		backoff = c.nextBackoff(backoff)
	}
}

func (c *Client) handleConn(socket *gws.Conn) {
	for {
		_, raw, err := socket.ReadMessage()
		if err != nil {
			log.Printf("gatewaybridge read closed: err=%v", err)
			return
		}
		log.Printf("gatewaybridge recv raw=%s", string(raw))
		var msg WSMessage
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("gatewaybridge parse failed: err=%v", err)
			continue
		}
		log.Printf("gatewaybridge recv cmd=%s requestId=%s targetId=%s bodyLen=%d", msg.Cmd, msg.RequestID, msg.TargetID, len(msg.Body))
		switch msg.Cmd {
		case CmdUserMessage:
			go c.handleUserMessage(socket, msg.Body, msg.RequestID)
		case CmdUserUpload:
			go c.handleUpload(socket, msg.Body)
		default:
			log.Printf("gatewaybridge unknown cmd=%s", msg.Cmd)
		}
	}
}

func (c *Client) handleUserMessage(socket *gws.Conn, body json.RawMessage, envelopeRequestID string) {
	if IsSubmit(body) {
		c.handleSubmit(socket, body, envelopeRequestID)
		return
	}
	c.handleQuery(socket, body, envelopeRequestID)
}

func (c *Client) handleQuery(socket *gws.Conn, body json.RawMessage, envelopeRequestID string) {
	var qb QueryBody
	if err := json.Unmarshal(body, &qb); err != nil {
		log.Printf("gatewaybridge query parse failed: err=%v body=%s", err, string(body))
		c.sendError(socket, envelopeRequestID, "invalid query body")
		return
	}

	agentKey := strings.TrimSpace(qb.AgentKey)
	if c.cfg.OverrideAgentKey != "" {
		agentKey = c.cfg.OverrideAgentKey
	} else if agentKey == "" {
		agentKey = c.cfg.DefaultAgentKey
	}

	requestID := strings.TrimSpace(envelopeRequestID)
	if requestID == "" {
		requestID = strings.TrimSpace(qb.RequestID)
	}
	if requestID == "" {
		requestID = strings.TrimSpace(qb.RunID)
	}

	req := api.QueryRequest{
		RequestID: strings.TrimSpace(qb.RequestID),
		ChatID:    strings.TrimSpace(qb.ChatID),
		AgentKey:  agentKey,
		Role:      strings.TrimSpace(qb.Role),
		Message:   qb.Message,
	}

	log.Printf("gatewaybridge query start chatId=%s agentKey=%s requestId=%s runId=%s", req.ChatID, agentKey, requestID, qb.RunID)

	eventCount := 0
	err := c.runner.ExecuteInternalQueryStream(c.ctx, req, func(eventJSON []byte) error {
		eventCount++
		forward := c.normalizeRunnerEvent(req.ChatID, eventJSON)
		return c.writeResponse(socket, requestID, json.RawMessage(forward))
	})
	if err != nil {
		log.Printf("gatewaybridge query failed chatId=%s eventsReceived=%d err=%v", req.ChatID, eventCount, err)
		c.sendError(socket, requestID, "query failed: "+err.Error())
		return
	}
	log.Printf("gatewaybridge query done chatId=%s totalEvents=%d", req.ChatID, eventCount)
}

func (c *Client) handleSubmit(socket *gws.Conn, body json.RawMessage, envelopeRequestID string) {
	var sb SubmitBody
	if err := json.Unmarshal(body, &sb); err != nil {
		log.Printf("gatewaybridge submit parse failed: err=%v body=%s", err, string(body))
		c.sendError(socket, envelopeRequestID, "invalid submit body")
		return
	}

	awaitingID := strings.TrimSpace(sb.AwaitingID)
	if awaitingID == "" {
		awaitingID = strings.TrimSpace(sb.ToolID)
	}
	req := api.SubmitRequest{
		RunID:      strings.TrimSpace(sb.RunID),
		AwaitingID: awaitingID,
	}
	if len(sb.Params) > 0 {
		if err := json.Unmarshal(sb.Params, &req.Params); err != nil {
			log.Printf("gatewaybridge submit params parse failed runId=%s err=%v", req.RunID, err)
			c.sendError(socket, envelopeRequestID, "invalid submit params: "+err.Error())
			return
		}
	}
	requestID := strings.TrimSpace(envelopeRequestID)
	if requestID == "" {
		requestID = req.RunID
	}
	log.Printf("gatewaybridge submit start runId=%s awaitingId=%s requestId=%s", req.RunID, awaitingID, requestID)

	status, respBody, err := c.runner.ExecuteInternalSubmit(c.ctx, req)
	if err != nil {
		log.Printf("gatewaybridge submit failed runId=%s err=%v", req.RunID, err)
		c.sendError(socket, requestID, "submit failed: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		log.Printf("gatewaybridge submit status=%d runId=%s body=%s", status, req.RunID, string(respBody))
		c.sendError(socket, requestID, fmt.Sprintf("submit status=%d", status))
		return
	}

	log.Printf("gatewaybridge submit done runId=%s status=%d respLen=%d", req.RunID, status, len(respBody))
	if err := c.writeResponse(socket, requestID, json.RawMessage(respBody)); err != nil {
		log.Printf("gatewaybridge submit response write failed err=%v", err)
	}
}

func (c *Client) handleUpload(socket *gws.Conn, body json.RawMessage) {
	var ub UserUploadBody
	if err := json.Unmarshal(body, &ub); err != nil {
		log.Printf("gatewaybridge upload parse failed: err=%v body=%s", err, string(body))
		c.sendError(socket, "", "invalid upload body")
		return
	}

	if c.dedup(ub.RequestID) {
		log.Printf("gatewaybridge upload dedup requestId=%s", ub.RequestID)
		return
	}

	log.Printf("gatewaybridge upload start requestId=%s chatId=%s fileId=%s fileName=%s mimeType=%s sizeBytes=%d url=%s",
		ub.RequestID, ub.ChatID, ub.Upload.ID, ub.Upload.Name, ub.Upload.MimeType, ub.Upload.SizeBytes, ub.Upload.URL)

	downloadURL := c.buildDownloadURL(ub.Upload.URL)
	if downloadURL == "" {
		log.Printf("gatewaybridge upload skip: empty download url requestId=%s", ub.RequestID)
		c.sendError(socket, ub.RequestID, "empty download url")
		return
	}
	log.Printf("gatewaybridge upload download url=%s", downloadURL)

	fileData, err := c.downloadFile(downloadURL)
	if err != nil {
		log.Printf("gatewaybridge upload download failed url=%s err=%v", downloadURL, err)
		c.sendError(socket, ub.RequestID, "download failed: "+err.Error())
		return
	}
	log.Printf("gatewaybridge upload downloaded fileName=%s bytes=%d", ub.Upload.Name, len(fileData))

	status, respBody, err := c.runner.ExecuteInternalUpload(c.ctx, ub.ChatID, ub.RequestID, ub.Upload.Name, fileData)
	if err != nil {
		log.Printf("gatewaybridge upload runner failed chatId=%s err=%v", ub.ChatID, err)
		c.sendError(socket, ub.RequestID, "upload to runner failed: "+err.Error())
		return
	}
	if status < 200 || status >= 300 {
		log.Printf("gatewaybridge upload runner status=%d chatId=%s body=%s", status, ub.ChatID, string(respBody))
		c.sendError(socket, ub.RequestID, fmt.Sprintf("upload status=%d", status))
		return
	}

	log.Printf("gatewaybridge upload done chatId=%s requestId=%s respLen=%d", ub.ChatID, ub.RequestID, len(respBody))
	if err := c.writeResponse(socket, ub.RequestID, json.RawMessage(respBody)); err != nil {
		log.Printf("gatewaybridge upload response write failed err=%v", err)
	}
}

// buildDownloadURL 构造从网关下载 userUpload 文件的完整 URL。
// - 绝对 http(s) URL 原样返回
// - 以 "/" 开头视作网关相对路径，拼上 BaseURL
// - 其他（如 objectPath）拼上 BaseURL + DownloadPath
func (c *Client) buildDownloadURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return raw
	}
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if base == "" {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return base + raw
	}
	downloadPath := strings.Trim(c.cfg.DownloadPath, "/")
	if downloadPath == "" {
		return base + "/" + raw
	}
	return base + "/" + downloadPath + "/" + raw
}

func (c *Client) downloadFile(u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(c.ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.AuthToken)
	}
	resp, err := c.httpClient.Do(req)
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

// normalizeRunnerEvent 识别 artifact.publish 事件，改写 URL 为网关可达地址，
// 并异步将本地文件 multipart POST 到网关 upload 接口。其他事件原样返回。
func (c *Client) normalizeRunnerEvent(chatID string, raw []byte) []byte {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return raw
	}
	eventType, _ := payload["type"].(string)
	if eventType != "artifact.publish" {
		return raw
	}

	artifacts := collectArtifacts(payload)
	if len(artifacts) == 0 {
		return raw
	}

	for _, art := range artifacts {
		originalURL, _ := art["url"].(string)
		rewritten := c.rewriteArtifactURL(originalURL)
		art["url"] = rewritten
		go c.forwardArtifactToGateway(chatID, payload, art, originalURL)
	}

	normalized, err := json.Marshal(payload)
	if err != nil {
		return raw
	}
	return normalized
}

// collectArtifacts 同时兼容单对象 (payload.artifact) 与数组 (payload.artifacts)
// 两种形态，统一返回可变 map 列表供就地改写。
func collectArtifacts(payload map[string]any) []map[string]any {
	var result []map[string]any
	if single, ok := payload["artifact"].(map[string]any); ok {
		result = append(result, single)
	}
	if arr, ok := payload["artifacts"].([]any); ok {
		for _, item := range arr {
			if m, ok := item.(map[string]any); ok {
				result = append(result, m)
			}
		}
	}
	return result
}

func (c *Client) rewriteArtifactURL(rawURL string) string {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return rawURL
	}
	lower := strings.ToLower(u)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return u
	}
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if base == "" {
		return u
	}
	if strings.HasPrefix(u, "/") {
		return base + u
	}
	return base + "/" + strings.TrimLeft(u, "/")
}

// forwardArtifactToGateway 从 platform 本地 chats 目录读文件，multipart POST
// 给网关 upload 接口。best-effort：失败只记日志不影响 stream。
func (c *Client) forwardArtifactToGateway(chatID string, event map[string]any, artifact map[string]any, originalArtifactURL string) {
	baseURL := strings.TrimSpace(c.cfg.BaseURL)
	uploadPath := strings.TrimSpace(c.cfg.UploadPath)
	if baseURL == "" || uploadPath == "" {
		log.Printf("gatewaybridge artifact upload skip: gateway upload endpoint not configured")
		return
	}

	artifactID, _ := event["artifactId"].(string)
	if id, ok := artifact["artifactId"].(string); ok && id != "" {
		artifactID = id
	}
	fileName, _ := artifact["name"].(string)
	fileType, _ := artifact["type"].(string)
	if fileType == "" {
		fileType = "file"
	}
	if artifactID == "" {
		artifactID = fmt.Sprintf("%s-%d", fileName, time.Now().UnixMilli())
	}
	if fileName == "" {
		fileName = "artifact.bin"
	}

	fileParam := extractResourceFileParam(originalArtifactURL)
	if fileParam == "" {
		log.Printf("gatewaybridge artifact upload skip: cannot resolve local file chatId=%s artifactId=%s url=%s", chatID, artifactID, originalArtifactURL)
		return
	}
	localPath, err := c.runner.ResolveResourcePath(fileParam)
	if err != nil {
		log.Printf("gatewaybridge artifact upload skip: resolve failed chatId=%s artifactId=%s file=%s err=%v", chatID, artifactID, fileParam, err)
		return
	}
	fileData, err := os.ReadFile(localPath)
	if err != nil {
		log.Printf("gatewaybridge artifact upload skip: read failed chatId=%s artifactId=%s path=%s err=%v", chatID, artifactID, localPath, err)
		return
	}

	uploadURL := baseURL + "/" + strings.TrimLeft(path.Clean("/"+uploadPath), "/")
	respBody, err := c.uploadToGateway(uploadURL, chatID, fileName, fileType, artifactID, fileData)
	if err != nil {
		log.Printf("gatewaybridge artifact upload failed chatId=%s artifactId=%s url=%s err=%v", chatID, artifactID, uploadURL, err)
		return
	}
	log.Printf("gatewaybridge artifact upload ok chatId=%s artifactId=%s fileName=%s sizeBytes=%d response=%s",
		chatID, artifactID, fileName, len(fileData), string(respBody))
}

// extractResourceFileParam 从 "/api/resource?file=xxx" 形态的 URL 抽取 file 值。
// 非 /api/resource 链接返回空。
func extractResourceFileParam(rawURL string) string {
	raw := strings.TrimSpace(rawURL)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	if parsed.Path != "" && !strings.HasSuffix(parsed.Path, "/api/resource") {
		return ""
	}
	return parsed.Query().Get("file")
}

func (c *Client) uploadToGateway(uploadURL, chatID, fileName, fileType, requestID string, fileData []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)

	_ = writer.WriteField("chatId", chatID)
	if fileName != "" {
		_ = writer.WriteField("name", fileName)
	}
	if fileType != "" {
		_ = writer.WriteField("type", fileType)
	}
	if requestID != "" {
		_ = writer.WriteField("requestId", requestID)
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		return nil, fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(fileData); err != nil {
		return nil, fmt.Errorf("write form file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close writer: %w", err)
	}

	req, err := http.NewRequestWithContext(c.ctx, http.MethodPost, uploadURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if c.cfg.AuthToken != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.AuthToken)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http post: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 128*1024))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return body, fmt.Errorf("gateway upload status=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return body, nil
}

// sendError 向网关返回一个 agentResponse 形态的错误帧，body 为
// {"type":"bridge.error","error":"..."}，沿用 wecom-bridge 约定。
func (c *Client) sendError(conn *gws.Conn, requestID, message string) {
	log.Printf("gatewaybridge send error requestId=%s message=%s", requestID, message)
	body, err := json.Marshal(BridgeErrorBody{Type: "bridge.error", Error: message})
	if err != nil {
		return
	}
	_ = c.writeJSON(conn, WSMessage{
		Cmd:       CmdAgentResponse,
		RequestID: strings.TrimSpace(requestID),
		Body:      body,
	})
}

func (c *Client) sleep(delay time.Duration) bool {
	if delay <= 0 {
		return c.ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (c *Client) nextBackoff(current time.Duration) time.Duration {
	if current <= 0 {
		return c.cfg.ReconnectMin
	}
	next := current * 2
	if next > c.cfg.ReconnectMax {
		return c.cfg.ReconnectMax
	}
	return next
}

func (c *Client) jitter(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	delta := int64(base) / 5
	if delta <= 0 {
		return base
	}
	c.rngMu.Lock()
	offset := c.rng.Int63n(2*delta+1) - delta
	c.rngMu.Unlock()
	return time.Duration(int64(base) + offset)
}
