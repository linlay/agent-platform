package documentpreview

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"agent-platform/internal/httpclient"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func digest(values ...string) string {
	data, _ := json.Marshal(values)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

type hub struct {
	config       Config
	client       *http.Client
	token, scope string
}

func newHub(c Config) (*hub, error) {
	token := ""
	if c.AuthMode == "bearer-token-file" {
		f, err := os.Open(c.TokenFile)
		if err != nil {
			return nil, failure("preview_credentials_unavailable", "预览服务凭据不可用", 503)
		}
		defer f.Close()
		data, err := io.ReadAll(io.LimitReader(f, 16385))
		if err != nil || len(data) > 16384 {
			return nil, failure("preview_credentials_unavailable", "预览服务凭据不可用", 503)
		}
		token = strings.TrimSpace(string(data))
		if token == "" || strings.ContainsAny(token, "\r\n\t ") {
			return nil, failure("preview_credentials_unavailable", "预览服务凭据无效", 503)
		}
	}
	client := httpclient.NewClient(c.RequestTimeout)
	// Credentials and uploaded bytes must never follow redirects to another origin.
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hub{config: c, client: client, token: token, scope: digest(c.Provider, c.APIBaseURL, c.PublicBaseURL, c.AuthMode, token)}, nil
}

func (h *hub) request(ctx context.Context, method, path, contentType string, body io.Reader, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, h.config.APIBaseURL+path, body)
	if err != nil {
		return failure("preview_service_unavailable", "无法请求预览服务", 502)
	}
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	res, err := h.client.Do(req)
	if err != nil {
		return failure("preview_service_unavailable", "预览服务请求失败或超时", 502)
	}
	defer res.Body.Close()
	if res.StatusCode == 404 || res.StatusCode == 410 {
		return failure("preview_remote_missing", "远端预览副本已不存在", 404)
	}
	if res.StatusCode == 401 || res.StatusCode == 403 {
		return failure("preview_service_unauthorized", "预览服务拒绝访问，请检查服务认证配置", 502)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return failure("preview_service_error", "预览服务返回错误", 502)
	}
	if out != nil {
		data, err := io.ReadAll(io.LimitReader(res.Body, 1<<20+1))
		if err != nil || len(data) > 1<<20 || json.Unmarshal(data, out) != nil {
			return failure("preview_invalid_response", "预览服务响应无效", 502)
		}
	} else {
		_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	}
	return nil
}

func (h *hub) upload(ctx context.Context, s snapshot) (string, error) {
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	done := make(chan struct{})
	go func() {
		defer close(done)
		part, err := form.CreateFormFile("file", s.name)
		if err == nil {
			_, err = io.Copy(part, bytes.NewReader(s.data))
		}
		if err == nil {
			err = form.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	var result struct {
		ID string `json:"id"`
	}
	err := h.request(ctx, http.MethodPost, "/api/v1/documents/upload", form.FormDataContentType(), reader, &result)
	_ = reader.Close()
	<-done
	if err != nil {
		return "", err
	}
	if !uuidPattern.MatchString(result.ID) {
		return "", failure("preview_invalid_response", "预览服务没有返回有效文档 ID", 502)
	}
	return result.ID, nil
}

func (h *hub) share(ctx context.Context, id string) (string, string, int64, error) {
	body := strings.NewReader(`{"expiry":"1d","password":"","allowDownload":false,"allowPrint":false,"allowCopy":true}`)
	var result struct {
		URL  string `json:"url"`
		Link struct {
			ID        string    `json:"id"`
			ExpiresAt time.Time `json:"expiresAt"`
		} `json:"link"`
	}
	err := h.request(ctx, http.MethodPost, "/api/v1/documents/"+id+"/share-links", "application/json", body, &result)
	if err != nil {
		return "", "", 0, err
	}
	u, err := url.Parse(result.URL)
	origin, _ := url.Parse(h.config.PublicBaseURL)
	if err != nil || u.User != nil || u.Scheme != origin.Scheme || !strings.EqualFold(u.Host, origin.Host) || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/s/") || strings.TrimPrefix(u.Path, "/s/") == "" || strings.Contains(strings.TrimPrefix(u.Path, "/s/"), "/") || !uuidPattern.MatchString(result.Link.ID) || !result.Link.ExpiresAt.After(time.Now()) || result.Link.ExpiresAt.After(time.Now().Add(25*time.Hour)) {
		return "", "", 0, failure("preview_invalid_url", "预览地址或有效期与服务配置不匹配", 502)
	}
	return result.Link.ID, result.URL, result.Link.ExpiresAt.UnixMilli(), nil
}

// Check the remote list before recycling: a share creation response may have
// been lost after the service issued a valid link.
func (h *hub) hasActiveShare(ctx context.Context, id string) (bool, error) {
	var response struct {
		Items *[]struct {
			ExpiresAt *time.Time `json:"expiresAt"`
			RevokedAt *time.Time `json:"revokedAt"`
		} `json:"items"`
	}
	if err := h.request(ctx, "GET", "/api/v1/documents/"+id+"/share-links", "", nil, &response); err != nil {
		return false, err
	}
	if response.Items == nil {
		return false, failure("preview_invalid_response", "预览服务分享列表无效", 502)
	}
	for _, link := range *response.Items {
		if link.RevokedAt == nil && (link.ExpiresAt == nil || link.ExpiresAt.After(time.Now())) {
			return true, nil
		}
	}
	return false, nil
}
