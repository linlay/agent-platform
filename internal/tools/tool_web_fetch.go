package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	neturl "net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	. "agent-platform/internal/contracts"
	"agent-platform/internal/models"
	"golang.org/x/net/html"
)

const webFetchMaxRedirects = 10

type webFetchContent struct {
	Content       string
	Bytes         int
	Code          int
	CodeText      string
	ContentType   string
	FinalURL      string
	PersistedPath string
	PersistedSize int
}

type webFetchRedirect struct {
	OriginalURL string
	RedirectURL string
	StatusCode  int
	StatusText  string
}

func (t *RuntimeToolExecutor) invokeWebFetch(ctx context.Context, args map[string]any, execCtx *ExecutionContext) (ToolExecutionResult, error) {
	cfg := t.cfg.WebFetch
	if !cfg.Enabled {
		return webFetchToolError("web_fetch_disabled", "web_fetch is disabled by configs/ai-tools.yml", nil), nil
	}
	if t.models == nil {
		return webFetchToolError("web_fetch_model_registry_unavailable", "model registry is not configured for web_fetch", nil), nil
	}
	rawURL := strings.TrimSpace(AnyStringNode(args["url"]))
	if rawURL == "" {
		return webFetchToolError("web_fetch_url_required", "url is required", nil), nil
	}
	prompt := strings.TrimSpace(AnyStringNode(args["prompt"]))
	if prompt == "" {
		return webFetchToolError("web_fetch_prompt_required", "prompt is required", nil), nil
	}
	profileName := strings.TrimSpace(AnyStringNode(args["profile"]))
	if profileName == "" {
		profileName = cfg.DefaultProfile
	}
	if profileName == "" {
		profileName = "general"
	}
	profile, ok := cfg.Profiles[profileName]
	if !ok {
		return webFetchToolError("web_fetch_profile_not_found", "web_fetch profile not found: "+profileName, map[string]any{"profile": profileName}), nil
	}
	if strings.TrimSpace(profile.ModelKey) == "" {
		return webFetchToolError("web_fetch_profile_model_missing", "web_fetch profile model-key is required: "+profileName, map[string]any{"profile": profileName}), nil
	}
	model, provider, err := t.models.Get(profile.ModelKey)
	if err != nil {
		return webFetchToolError("web_fetch_model_not_found", err.Error(), map[string]any{"modelKey": profile.ModelKey}), nil
	}
	if strings.TrimSpace(provider.BaseURL) == "" || strings.TrimSpace(provider.APIKey) == "" {
		return webFetchToolError("web_fetch_provider_config_invalid", "provider baseUrl and apiKey are required", map[string]any{"provider": provider.Key}), nil
	}

	start := time.Now()
	fetchCtx, cancel := context.WithTimeout(ctx, time.Duration(maxInt(profile.FetchTimeout, 60))*time.Second)
	defer cancel()
	response, redirect, err := t.fetchWebFetchContent(fetchCtx, rawURL, profile, execCtx)
	if err != nil {
		return webFetchToolError("web_fetch_request_failed", err.Error(), map[string]any{"url": rawURL}), nil
	}
	if redirect != nil {
		result := webFetchRedirectMessage(*redirect, prompt)
		payload := map[string]any{
			"ok":         true,
			"url":        rawURL,
			"finalUrl":   redirect.OriginalURL,
			"bytes":      len(result),
			"code":       redirect.StatusCode,
			"codeText":   redirect.StatusText,
			"durationMs": time.Since(start).Milliseconds(),
			"result":     result,
			"redirect": map[string]any{
				"originalUrl": redirect.OriginalURL,
				"redirectUrl": redirect.RedirectURL,
				"statusCode":  redirect.StatusCode,
				"statusText":  redirect.StatusText,
			},
		}
		return structuredResult(payload), nil
	}

	content := response.Content
	truncated := false
	if profile.MaxMarkdownChars > 0 && len(content) > profile.MaxMarkdownChars {
		content = content[:profile.MaxMarkdownChars] + "\n\n[Content truncated due to length...]"
		truncated = true
	}

	isPreapproved := webFetchHostPreapproved(response.FinalURL, cfg.PreapprovedHosts)
	directReturn := isPreapproved && strings.Contains(strings.ToLower(response.ContentType), "text/markdown") && len(content) < maxInt(profile.MaxMarkdownChars, 100000)
	result := content
	var usage map[string]any
	if !directReturn {
		callCtx, callCancel := context.WithTimeout(ctx, time.Duration(maxInt(profile.Timeout, 60))*time.Second)
		defer callCancel()
		result, usage, err = t.applyWebFetchPrompt(callCtx, model, provider, profile, prompt, content, response.FinalURL, isPreapproved)
		if err != nil {
			return webFetchToolError("web_fetch_model_request_failed", err.Error(), map[string]any{"modelKey": model.Key, "profile": profileName}), nil
		}
	}

	payload := map[string]any{
		"ok":          true,
		"url":         rawURL,
		"finalUrl":    response.FinalURL,
		"bytes":       response.Bytes,
		"code":        response.Code,
		"codeText":    response.CodeText,
		"contentType": response.ContentType,
		"durationMs":  time.Since(start).Milliseconds(),
		"result":      result,
		"profile":     profileName,
		"modelKey":    model.Key,
	}
	if truncated {
		payload["contentTruncated"] = true
		payload["contentChars"] = len(response.Content)
	}
	if directReturn {
		payload["directReturn"] = true
	}
	if len(usage) > 0 {
		payload["usage"] = usage
	}
	if response.PersistedPath != "" {
		payload["persistedPath"] = response.PersistedPath
		payload["persistedSize"] = response.PersistedSize
	}
	return structuredResult(payload), nil
}

func (t *RuntimeToolExecutor) fetchWebFetchContent(ctx context.Context, rawURL string, profile config.WebFetchProfileConfig, execCtx *ExecutionContext) (*webFetchContent, *webFetchRedirect, error) {
	parsed, err := validateWebFetchURL(rawURL, profile)
	if err != nil {
		return nil, nil, err
	}
	if parsed.Scheme == "http" {
		parsed.Scheme = "https"
	}
	current := parsed
	client := t.webFetchHTTPClient()
	for redirectCount := 0; redirectCount <= webFetchMaxRedirects; redirectCount++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, current.String(), nil)
		if err != nil {
			return nil, nil, err
		}
		req.Header.Set("User-Agent", "agent-platform-web-fetch/1.0")
		req.Header.Set("Accept", "text/html,text/markdown,text/plain,application/json,application/xml,*/*;q=0.8")
		resp, err := client.Do(req)
		if err != nil {
			return nil, nil, err
		}
		if isWebFetchRedirectStatus(resp.StatusCode) {
			location := strings.TrimSpace(resp.Header.Get("Location"))
			_ = resp.Body.Close()
			if location == "" {
				return nil, nil, fmt.Errorf("redirect status %d missing Location header", resp.StatusCode)
			}
			next, err := current.Parse(location)
			if err != nil {
				return nil, nil, fmt.Errorf("invalid redirect URL: %w", err)
			}
			if err := validateParsedWebFetchURL(next, profile); err != nil {
				return nil, nil, fmt.Errorf("redirect target rejected: %w", err)
			}
			if next.Scheme == "http" {
				next.Scheme = "https"
			}
			if !strings.EqualFold(current.Hostname(), next.Hostname()) {
				return nil, &webFetchRedirect{
					OriginalURL: current.String(),
					RedirectURL: next.String(),
					StatusCode:  resp.StatusCode,
					StatusText:  webFetchStatusText(resp.StatusCode, resp.Status),
				}, nil
			}
			current = next
			continue
		}
		content, err := readWebFetchResponse(resp, profile, current.String(), execCtx)
		if err != nil {
			return nil, nil, err
		}
		return content, nil, nil
	}
	return nil, nil, fmt.Errorf("too many redirects")
}

func (t *RuntimeToolExecutor) webFetchHTTPClient() *http.Client {
	base := t.httpClient
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &client
}

func readWebFetchResponse(resp *http.Response, profile config.WebFetchProfileConfig, finalURL string, execCtx *ExecutionContext) (*webFetchContent, error) {
	defer resp.Body.Close()
	limit := int64(maxInt(profile.MaxResponseBytes, 10*1024*1024))
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds max-response-bytes: %d", limit)
	}
	contentType := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Type")))
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	persistedPath := ""
	persistedSize := 0
	if isWebFetchBinaryContentType(contentType) {
		if path, err := persistWebFetchBinary(data, contentType, execCtx); err == nil {
			persistedPath = path
			persistedSize = len(data)
		}
	}
	content := string(data)
	if strings.Contains(contentType, "text/html") || strings.Contains(contentType, "application/xhtml") {
		content = htmlToMarkdownLike(bytes.NewReader(data))
	}
	return &webFetchContent{
		Content:       strings.TrimSpace(content),
		Bytes:         len(data),
		Code:          resp.StatusCode,
		CodeText:      webFetchStatusText(resp.StatusCode, resp.Status),
		ContentType:   contentType,
		FinalURL:      finalURL,
		PersistedPath: persistedPath,
		PersistedSize: persistedSize,
	}, nil
}

func (t *RuntimeToolExecutor) applyWebFetchPrompt(ctx context.Context, model models.ModelDefinition, provider models.ProviderDefinition, profile config.WebFetchProfileConfig, prompt string, content string, finalURL string, preapproved bool) (string, map[string]any, error) {
	userPrompt := webFetchModelPrompt(finalURL, prompt, content, preapproved)
	systemPrompt := strings.TrimSpace(profile.SystemPrompt)
	if systemPrompt == "" {
		systemPrompt = "You extract and summarize fetched web content according to the user's prompt."
	}
	return t.completeTextModel(ctx, model, provider, textModelRequest{
		SystemPrompt:    systemPrompt,
		UserPrompt:      userPrompt,
		MaxOutputTokens: maxInt(profile.MaxOutputTokens, 1200),
	})
}

func webFetchModelPrompt(finalURL string, prompt string, content string, preapproved bool) string {
	approvalLine := "The URL is not on the direct-return preapproved host list."
	if preapproved {
		approvalLine = "The URL is on the direct-return preapproved host list."
	}
	return strings.TrimSpace(fmt.Sprintf(`Fetched URL: %s
%s

User prompt:
%s

Fetched content:
%s

Answer the user prompt using only the fetched content. If the content does not contain enough information, say so.`, finalURL, approvalLine, strings.TrimSpace(prompt), strings.TrimSpace(content)))
}

func validateWebFetchURL(rawURL string, profile config.WebFetchProfileConfig) (*neturl.URL, error) {
	if len(rawURL) > maxInt(profile.MaxURLLength, 2000) {
		return nil, fmt.Errorf("url exceeds max-url-length: %d", maxInt(profile.MaxURLLength, 2000))
	}
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateParsedWebFetchURL(parsed, profile); err != nil {
		return nil, err
	}
	return parsed, nil
}

func validateParsedWebFetchURL(parsed *neturl.URL, profile config.WebFetchProfileConfig) error {
	if parsed == nil {
		return errors.New("url is required")
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "http", "https":
	default:
		return fmt.Errorf("unsupported url scheme: %s", parsed.Scheme)
	}
	if parsed.User != nil {
		return errors.New("url userinfo is not allowed")
	}
	if len(parsed.String()) > maxInt(profile.MaxURLLength, 2000) {
		return fmt.Errorf("url exceeds max-url-length: %d", maxInt(profile.MaxURLLength, 2000))
	}
	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	host = strings.TrimSuffix(host, ".")
	if host == "" {
		return errors.New("url host is required")
	}
	if isBlockedWebFetchHost(host) {
		return fmt.Errorf("blocked host: %s", host)
	}
	return nil
}

func isBlockedWebFetchHost(host string) bool {
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified()
	}
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".lan") {
		return true
	}
	return !strings.Contains(host, ".")
}

func isWebFetchRedirectStatus(code int) bool {
	switch code {
	case http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect:
		return true
	default:
		return false
	}
}

func webFetchStatusText(code int, raw string) string {
	if text := strings.TrimSpace(raw); text != "" {
		parts := strings.SplitN(text, " ", 2)
		if len(parts) == 2 {
			return strings.TrimSpace(parts[1])
		}
		return text
	}
	if text := http.StatusText(code); text != "" {
		return text
	}
	return strconv.Itoa(code)
}

func webFetchRedirectMessage(redirect webFetchRedirect, prompt string) string {
	return fmt.Sprintf(`REDIRECT DETECTED: The URL redirects to a different host.

Original URL: %s
Redirect URL: %s
Status: %d %s

To complete your request, call web_fetch again with:
- url: "%s"
- prompt: "%s"`, redirect.OriginalURL, redirect.RedirectURL, redirect.StatusCode, redirect.StatusText, redirect.RedirectURL, strings.TrimSpace(prompt))
}

func isWebFetchBinaryContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if contentType == "" {
		return false
	}
	if strings.HasPrefix(contentType, "text/") {
		return false
	}
	switch contentType {
	case "application/json", "application/xml", "application/xhtml+xml", "application/javascript", "application/x-javascript", "application/ld+json", "image/svg+xml":
		return false
	default:
		return true
	}
}

func persistWebFetchBinary(data []byte, contentType string, execCtx *ExecutionContext) (string, error) {
	chatDir := webFetchChatDir(execCtx)
	if chatDir == "" {
		return "", errors.New("chat context unavailable")
	}
	dir := filepath.Join(chatDir, chat.ToolRootDirName, "web-fetch")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	ext := webFetchExtension(contentType)
	name := "webfetch-" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(sum[:])[:12] + ext
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func webFetchChatDir(execCtx *ExecutionContext) string {
	if execCtx == nil {
		return ""
	}
	if dir := strings.TrimSpace(execCtx.Session.RuntimeContext.LocalPaths.ChatAttachmentsDir); dir != "" {
		return dir
	}
	return ""
}

func webFetchExtension(contentType string) string {
	contentType = strings.TrimSpace(strings.Split(contentType, ";")[0])
	extensions, err := mime.ExtensionsByType(contentType)
	if err == nil && len(extensions) > 0 {
		return extensions[0]
	}
	switch strings.ToLower(contentType) {
	case "application/pdf":
		return ".pdf"
	default:
		return ".bin"
	}
}

func webFetchHostPreapproved(rawURL string, hosts []string) bool {
	parsed, err := neturl.Parse(rawURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(parsed.Hostname()), "."))
	if host == "" {
		return false
	}
	for _, allowed := range hosts {
		allowed = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(allowed), "."))
		if allowed == "" {
			continue
		}
		if strings.HasPrefix(allowed, "*.") {
			suffix := strings.TrimPrefix(allowed, "*.")
			if host == suffix || strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == allowed {
			return true
		}
	}
	return false
}

func htmlToMarkdownLike(reader io.Reader) string {
	data, readErr := io.ReadAll(reader)
	if readErr != nil {
		return ""
	}
	root, err := html.Parse(bytes.NewReader(data))
	if err != nil {
		return strings.TrimSpace(string(data))
	}
	var builder strings.Builder
	renderHTMLNode(&builder, root)
	return cleanupMarkdownWhitespace(builder.String())
}

func renderHTMLNode(builder *strings.Builder, node *html.Node) {
	if node == nil {
		return
	}
	if node.Type == html.TextNode {
		appendMarkdownText(builder, node.Data)
		return
	}
	if node.Type != html.ElementNode && node.Type != html.DocumentNode {
		for child := node.FirstChild; child != nil; child = child.NextSibling {
			renderHTMLNode(builder, child)
		}
		return
	}
	tag := strings.ToLower(node.Data)
	switch tag {
	case "script", "style", "noscript", "svg", "canvas":
		return
	case "br":
		ensureMarkdownNewline(builder)
		return
	case "p", "div", "section", "article", "main", "header", "footer", "aside", "blockquote", "table", "tr":
		ensureMarkdownBlock(builder)
	case "li":
		ensureMarkdownNewline(builder)
		builder.WriteString("- ")
	case "h1", "h2", "h3", "h4", "h5", "h6":
		ensureMarkdownBlock(builder)
		level := int(tag[1] - '0')
		builder.WriteString(strings.Repeat("#", level))
		builder.WriteString(" ")
	}
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		renderHTMLNode(builder, child)
	}
	if tag == "a" {
		if href := htmlAttr(node, "href"); strings.TrimSpace(href) != "" {
			builder.WriteString(" (")
			builder.WriteString(strings.TrimSpace(href))
			builder.WriteString(")")
		}
	}
	switch tag {
	case "p", "div", "section", "article", "main", "header", "footer", "aside", "blockquote", "table", "tr", "li", "h1", "h2", "h3", "h4", "h5", "h6":
		ensureMarkdownBlock(builder)
	}
}

func htmlAttr(node *html.Node, key string) string {
	for _, attr := range node.Attr {
		if strings.EqualFold(attr.Key, key) {
			return attr.Val
		}
	}
	return ""
}

func appendMarkdownText(builder *strings.Builder, text string) {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return
	}
	if needsMarkdownSpace(builder) {
		builder.WriteByte(' ')
	}
	builder.WriteString(strings.Join(fields, " "))
}

func needsMarkdownSpace(builder *strings.Builder) bool {
	if builder.Len() == 0 {
		return false
	}
	text := builder.String()
	last := rune(text[len(text)-1])
	return !unicode.IsSpace(last) && last != '(' && last != '[' && last != '-'
}

func ensureMarkdownNewline(builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	text := builder.String()
	if !strings.HasSuffix(text, "\n") {
		builder.WriteByte('\n')
	}
}

func ensureMarkdownBlock(builder *strings.Builder) {
	if builder.Len() == 0 {
		return
	}
	text := builder.String()
	if strings.HasSuffix(text, "\n\n") {
		return
	}
	if strings.HasSuffix(text, "\n") {
		builder.WriteByte('\n')
		return
	}
	builder.WriteString("\n\n")
}

func cleanupMarkdownWhitespace(text string) string {
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	punctuation := strings.NewReplacer(" .", ".", " ,", ",", " ;", ";", " :", ":", " !", "!", " ?", "?")
	blank := false
	for _, line := range lines {
		line = strings.TrimSpace(line)
		line = punctuation.Replace(line)
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
				blank = true
			}
			continue
		}
		out = append(out, line)
		blank = false
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func webFetchToolError(code string, message string, diagnostics map[string]any) ToolExecutionResult {
	payload := map[string]any{
		"ok":      false,
		"error":   strings.TrimSpace(code),
		"message": strings.TrimSpace(message),
	}
	for key, value := range diagnostics {
		payload[key] = value
	}
	result := structuredResultWithExit(payload, -1)
	result.Error = strings.TrimSpace(code)
	return result
}
