package artifactpusher

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"agent-platform-runner-go/internal/contracts"
)

// Pusher forwards platform-hosted artifact files to the gateway's upload
// endpoint over plain HTTP multipart. Stream events keep travelling through
// WS, but the binary bytes go out-of-band so they never block the WS
// connection.
//
// A zero-value or nil-configured Pusher is safe; Push is a no-op when the
// gateway upload URL is not configured (e.g. webclient-only deployments).
// Resolver 按 chatId 查 gateway 的 BaseURL + Token；由 internal/gateway.Registry 提供实现。
// 接口拆到这里避免 artifactpusher → gateway 的直接 import（gateway 已 import artifactpusher 间接依赖）。
type Resolver interface {
	Resolve(chatID string) (baseURL string, token string, ok bool)
}

type Pusher struct {
	resolver      Resolver
	uploadPath    string
	chatsDir      string
	http          *http.Client
	notifications contracts.NotificationSink
}

type Config struct {
	Resolver      Resolver
	UploadPath    string
	ChatsDir      string
	Notifications contracts.NotificationSink
}

func New(cfg Config) *Pusher {
	p := &Pusher{
		resolver:      cfg.Resolver,
		uploadPath:    strings.TrimSpace(cfg.UploadPath),
		chatsDir:      strings.TrimSpace(cfg.ChatsDir),
		http:          &http.Client{Timeout: 60 * time.Second},
		notifications: cfg.Notifications,
	}
	if p.resolver == nil || p.uploadPath == "" {
		log.Printf("[artifact-pusher] disabled: no gateway resolver or upload path (uploadPath=%q); produced artifacts will stay local", p.uploadPath)
	} else {
		log.Printf("[artifact-pusher] enabled: uploadPath=%s chatsDir=%s (gateway routed per chatId)", p.uploadPath, p.chatsDir)
	}
	return p
}

// Push forwards one published artifact to the gateway. The artifact map uses
// the same fields `artifact_publish` emits on the event payload:
// {artifactId, name, mimeType, sizeBytes, sha256, url, type}. Best-effort —
// errors are logged only.
func (p *Pusher) Push(chatID string, artifact map[string]any) {
	if p == nil {
		log.Printf("[artifact-pusher] skip: pusher instance is nil")
		return
	}
	artifactID, _ := artifact["artifactId"].(string)
	name, _ := artifact["name"].(string)
	if p.resolver == nil || p.uploadPath == "" {
		log.Printf("[artifact-pusher] skip: no resolver or upload path chatId=%s artifactId=%s name=%s", chatID, artifactID, name)
		return
	}
	if chatID == "" || artifact == nil {
		log.Printf("[artifact-pusher] skip: empty chatId or artifact artifactId=%s name=%s", artifactID, name)
		return
	}
	log.Printf("[artifact-pusher] queue chatId=%s artifactId=%s name=%s", chatID, artifactID, name)
	go p.pushOne(chatID, artifact)
}

func (p *Pusher) pushOne(chatID string, artifact map[string]any) {
	artifactID, _ := artifact["artifactId"].(string)
	artifactURL, _ := artifact["url"].(string)
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

	relative := extractResourceFileParam(artifactURL)
	if relative == "" {
		log.Printf("[artifact-pusher] skip: cannot extract file param chatId=%s artifactId=%s url=%s", chatID, artifactID, artifactURL)
		return
	}
	localPath := p.resolveLocalPath(relative)
	if localPath == "" {
		log.Printf("[artifact-pusher] skip: path escapes chats dir chatId=%s artifactId=%s file=%s", chatID, artifactID, relative)
		return
	}
	data, err := os.ReadFile(localPath)
	if err != nil {
		log.Printf("[artifact-pusher] skip: read failed chatId=%s artifactId=%s path=%s err=%v", chatID, artifactID, localPath, err)
		return
	}

	mimeType, _ := artifact["mimeType"].(string)
	if mimeType == "" {
		mimeType = guessMimeType(fileName)
	}
	sha := sha256Hex(data)

	// 先发 push frame 给网关做预告（纯 metadata，字节走 HTTP POST）。
	// push 失败不阻塞 POST —— Broadcast 是 best-effort 的 fan-out。
	p.notifyArtifactOutgoing(chatID, artifactID, fileName, mimeType, sha, len(data))

	baseURL, token, ok := p.resolver.Resolve(chatID)
	if !ok || strings.TrimSpace(baseURL) == "" {
		log.Printf("[artifact-pusher] skip: no gateway route for chatId=%s artifactId=%s", chatID, artifactID)
		return
	}
	baseURL = strings.TrimRight(baseURL, "/")
	uploadURL := baseURL + "/" + strings.TrimLeft(path.Clean("/"+p.uploadPath), "/")
	respBody, err := p.postMultipart(uploadURL, token, chatID, fileName, fileType, artifactID, data)
	if err != nil {
		log.Printf("[artifact-pusher] upload failed chatId=%s artifactId=%s url=%s err=%v", chatID, artifactID, uploadURL, err)
		return
	}
	log.Printf("[artifact-pusher] upload ok chatId=%s artifactId=%s name=%s bytes=%d response=%s",
		chatID, artifactID, fileName, len(data), truncate(string(respBody), 256))
}

func (p *Pusher) notifyArtifactOutgoing(chatID, artifactID, name, mimeType, sha string, sizeBytes int) {
	if p.notifications == nil {
		return
	}
	p.notifications.Broadcast("/api/push", map[string]any{
		"chatId":     chatID,
		"artifactId": artifactID,
		"name":       name,
		"mimeType":   mimeType,
		"sha256":     sha,
		"sizeBytes":  sizeBytes,
		"timestamp":  time.Now().UnixMilli(),
	})
	log.Printf("[artifact-pusher] push sent chatId=%s artifactId=%s name=%s", chatID, artifactID, name)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func guessMimeType(fileName string) string {
	ext := strings.ToLower(filepath.Ext(fileName))
	if ext == "" {
		return "application/octet-stream"
	}
	if t := mime.TypeByExtension(ext); t != "" {
		return t
	}
	return "application/octet-stream"
}

func (p *Pusher) postMultipart(uploadURL, authToken, chatID, fileName, fileType, requestID string, data []byte) ([]byte, error) {
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
	if _, err := part.Write(data); err != nil {
		return nil, fmt.Errorf("write form file: %w", err)
	}
	if err := writer.Close(); err != nil {
		return nil, fmt.Errorf("close writer: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, uploadURL, &buf)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if authToken != "" {
		req.Header.Set("Authorization", "Bearer "+authToken)
	}
	resp, err := p.http.Do(req)
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

// resolveLocalPath joins chatsDir with the chat-relative file param and
// rejects any result that escapes chatsDir.
func (p *Pusher) resolveLocalPath(relative string) string {
	if p.chatsDir == "" {
		return ""
	}
	cleanRel := filepath.Clean(strings.TrimPrefix(filepath.FromSlash(relative), string(os.PathSeparator)))
	if strings.HasPrefix(cleanRel, "..") {
		return ""
	}
	abs := filepath.Join(p.chatsDir, cleanRel)
	rel, err := filepath.Rel(p.chatsDir, abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return ""
	}
	return abs
}

// extractResourceFileParam parses "/api/resource?file=<relative>" and returns
// the decoded file parameter. Returns "" when rawURL is absolute or not
// /api/resource-shaped.
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

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
