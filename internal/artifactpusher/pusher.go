package artifactpusher

import (
	"bytes"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

// Pusher forwards platform-hosted artifact files to the gateway's upload
// endpoint over plain HTTP multipart. It mirrors the side-channel wecom-bridge
// used to run: stream events keep travelling through WS, but the binary bytes
// go out-of-band so they never block the WS connection.
//
// A zero-value or nil-configured Pusher is safe; Push is a no-op when the
// gateway upload URL is not configured (e.g. webclient-only deployments).
type Pusher struct {
	baseURL    string
	uploadPath string
	authToken  string
	chatsDir   string
	http       *http.Client
}

type Config struct {
	BaseURL    string
	UploadPath string
	AuthToken  string
	ChatsDir   string
}

func New(cfg Config) *Pusher {
	p := &Pusher{
		baseURL:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		uploadPath: strings.TrimSpace(cfg.UploadPath),
		authToken:  strings.TrimSpace(cfg.AuthToken),
		chatsDir:   strings.TrimSpace(cfg.ChatsDir),
		http:       &http.Client{Timeout: 60 * time.Second},
	}
	if p.baseURL == "" || p.uploadPath == "" {
		log.Printf("[artifact-pusher] disabled: gateway upload endpoint not configured (GATEWAY_BASE_URL=%q GATEWAY_UPLOAD_PATH=%q); produced artifacts will stay local", p.baseURL, p.uploadPath)
	} else {
		log.Printf("[artifact-pusher] enabled: baseURL=%s uploadPath=%s chatsDir=%s authToken=%s",
			p.baseURL, p.uploadPath, p.chatsDir, maskToken(p.authToken))
	}
	return p
}

func maskToken(token string) string {
	if token == "" {
		return "(empty)"
	}
	if len(token) <= 4 {
		return "***"
	}
	return token[:2] + "***" + token[len(token)-2:]
}

// Push forwards one published artifact to the gateway. The artifact map uses
// the same fields `_artifact_publish_` emits on the event payload:
// {artifactId, name, mimeType, sizeBytes, sha256, url, type}. Best-effort —
// errors are logged only.
func (p *Pusher) Push(chatID string, artifact map[string]any) {
	if p == nil {
		log.Printf("[artifact-pusher] skip: pusher instance is nil")
		return
	}
	artifactID, _ := artifact["artifactId"].(string)
	name, _ := artifact["name"].(string)
	if p.baseURL == "" || p.uploadPath == "" {
		log.Printf("[artifact-pusher] skip: endpoint not configured chatId=%s artifactId=%s name=%s", chatID, artifactID, name)
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

	uploadURL := p.baseURL + "/" + strings.TrimLeft(path.Clean("/"+p.uploadPath), "/")
	respBody, err := p.postMultipart(uploadURL, chatID, fileName, fileType, artifactID, data)
	if err != nil {
		log.Printf("[artifact-pusher] upload failed chatId=%s artifactId=%s url=%s err=%v", chatID, artifactID, uploadURL, err)
		return
	}
	log.Printf("[artifact-pusher] upload ok chatId=%s artifactId=%s name=%s bytes=%d response=%s",
		chatID, artifactID, fileName, len(data), truncate(string(respBody), 256))
}

func (p *Pusher) postMultipart(uploadURL, chatID, fileName, fileType, requestID string, data []byte) ([]byte, error) {
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
	if p.authToken != "" {
		req.Header.Set("Authorization", "Bearer "+p.authToken)
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
