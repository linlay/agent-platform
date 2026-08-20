package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	tunnelDocumentVersion       = "1"
	tunnelCreateTimeout         = 15 * time.Second
	tunnelListTimeout           = 10 * time.Second
	tunnelRevokeTimeout         = 10 * time.Second
	maxTunnelShareResponseBytes = 1024 * 1024
	defaultShareExpiration      = "30d"
)

const (
	tunnelOriginHeader          = "X-Conversation-Export-Asset-Origin"
	tunnelAuthorizationHeader   = "X-Conversation-Share-Authorization"
	tunnelExpirationHeader      = "X-Conversation-Share-Expiration"
	tunnelConversationIDHeader  = "X-Conversation-ID"
	tunnelDocumentVersionHeader = "X-Conversation-Document-Version"
)

type tunnelShareTarget struct {
	origin        string
	authorization string
}

type tunnelShareResult struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	CreatedAt      int64  `json:"createdAt"`
	ExpiresAt      *int64 `json:"expiresAt"`
	LastAccessedAt *int64 `json:"lastAccessedAt"`
}

type tunnelShareClient struct {
	httpClient http.Client
}

type tunnelShareError struct {
	status int
	kind   string
}

func (e *tunnelShareError) Error() string {
	return "tunnel share " + e.kind
}

func newTunnelShareClient(base *http.Client) *tunnelShareClient {
	client := http.Client{}
	if base != nil {
		client = *base
	}
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &tunnelShareClient{httpClient: client}
}

func parseTunnelShareTarget(origin string, authorization string) (tunnelShareTarget, error) {
	origin = strings.TrimSpace(origin)
	authorization = strings.TrimSpace(authorization)
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || (parsed.Path != "" && parsed.Path != "/") {
		return tunnelShareTarget{}, errors.New("invalid tunnel origin")
	}
	parsed.Path = ""
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && isLoopbackHostname(parsed.Hostname())) {
		return tunnelShareTarget{}, errors.New("invalid tunnel origin")
	}
	if !strings.HasPrefix(authorization, "Bearer ") || strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")) == "" || strings.ContainsAny(strings.TrimPrefix(authorization, "Bearer "), " \t\r\n") {
		return tunnelShareTarget{}, errors.New("invalid tunnel authorization")
	}
	return tunnelShareTarget{origin: parsed.String(), authorization: authorization}, nil
}

func isLoopbackHostname(hostname string) bool {
	if strings.EqualFold(hostname, "localhost") {
		return true
	}
	ip := net.ParseIP(hostname)
	return ip != nil && ip.IsLoopback()
}

func (c *tunnelShareClient) Create(
	ctx context.Context,
	target tunnelShareTarget,
	conversationID string,
	html []byte,
	expiration string,
) (tunnelShareResult, error) {
	if !validConversationShareExpiration(expiration) {
		return tunnelShareResult{}, &tunnelShareError{kind: "request failed"}
	}
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return tunnelShareResult{}, &tunnelShareError{kind: "request failed"}
	}
	requestContext, cancel := context.WithTimeout(ctx, tunnelCreateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, target.origin+"/api/desktop/shares", bytes.NewReader(html))
	if err != nil {
		return tunnelShareResult{}, &tunnelShareError{kind: "request failed"}
	}
	req.Header.Set("Authorization", target.authorization)
	req.Header.Set("Content-Type", "text/html; charset=utf-8")
	req.Header.Set(tunnelDocumentVersionHeader, tunnelDocumentVersion)
	req.Header.Set(tunnelExpirationHeader, expiration)
	req.Header.Set(tunnelConversationIDHeader, conversationID)
	response, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return tunnelShareResult{}, &tunnelShareError{kind: "timeout"}
		}
		return tunnelShareResult{}, &tunnelShareError{kind: "unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTunnelShareResponseBytes))
		return tunnelShareResult{}, &tunnelShareError{status: response.StatusCode, kind: "rejected"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTunnelShareResponseBytes+1))
	if err != nil || len(body) > maxTunnelShareResponseBytes {
		return tunnelShareResult{}, &tunnelShareError{kind: "invalid response"}
	}
	var wireResult struct {
		ID             string          `json:"id"`
		URL            string          `json:"url"`
		CreatedAt      string          `json:"createdAt"`
		ExpiresAt      json.RawMessage `json:"expiresAt"`
		LastAccessedAt json.RawMessage `json:"lastAccessedAt"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireResult); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return tunnelShareResult{}, &tunnelShareError{kind: "invalid response"}
	}
	result, ok := parseTunnelShareResult(
		wireResult.ID,
		wireResult.URL,
		wireResult.CreatedAt,
		wireResult.ExpiresAt,
		wireResult.LastAccessedAt,
	)
	if !ok || !validTunnelShareResult(result, expiration) {
		return tunnelShareResult{}, &tunnelShareError{kind: "invalid response"}
	}
	return result, nil
}

func (c *tunnelShareClient) List(
	ctx context.Context,
	target tunnelShareTarget,
	conversationID string,
) ([]tunnelShareResult, error) {
	conversationID = strings.TrimSpace(conversationID)
	if conversationID == "" {
		return nil, &tunnelShareError{kind: "request failed"}
	}
	requestContext, cancel := context.WithTimeout(ctx, tunnelListTimeout)
	defer cancel()
	endpoint := target.origin + "/api/desktop/shares?conversationId=" + url.QueryEscape(conversationID)
	req, err := http.NewRequestWithContext(requestContext, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, &tunnelShareError{kind: "request failed"}
	}
	req.Header.Set("Authorization", target.authorization)
	response, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return nil, &tunnelShareError{kind: "timeout"}
		}
		return nil, &tunnelShareError{kind: "unavailable"}
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTunnelShareResponseBytes))
		return nil, &tunnelShareError{status: response.StatusCode, kind: "rejected"}
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxTunnelShareResponseBytes+1))
	if err != nil || len(body) > maxTunnelShareResponseBytes {
		return nil, &tunnelShareError{kind: "invalid response"}
	}
	var wireResult struct {
		Items []struct {
			ID             string          `json:"id"`
			URL            string          `json:"url"`
			CreatedAt      string          `json:"createdAt"`
			ExpiresAt      json.RawMessage `json:"expiresAt"`
			LastAccessedAt json.RawMessage `json:"lastAccessedAt"`
		} `json:"items"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wireResult); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		return nil, &tunnelShareError{kind: "invalid response"}
	}
	items := make([]tunnelShareResult, 0, len(wireResult.Items))
	seen := make(map[string]struct{}, len(wireResult.Items))
	for _, wireItem := range wireResult.Items {
		item, ok := parseTunnelShareResult(
			wireItem.ID,
			wireItem.URL,
			wireItem.CreatedAt,
			wireItem.ExpiresAt,
			wireItem.LastAccessedAt,
		)
		if !ok {
			return nil, &tunnelShareError{kind: "invalid response"}
		}
		if !validTunnelShareMetadata(item) {
			return nil, &tunnelShareError{kind: "invalid response"}
		}
		if _, duplicate := seen[item.ID]; duplicate {
			return nil, &tunnelShareError{kind: "invalid response"}
		}
		seen[item.ID] = struct{}{}
		items = append(items, item)
	}
	return items, nil
}

func (c *tunnelShareClient) Revoke(ctx context.Context, target tunnelShareTarget, shareID string) error {
	requestContext, cancel := context.WithTimeout(ctx, tunnelRevokeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodDelete, target.origin+"/api/desktop/shares/"+url.PathEscape(shareID), nil)
	if err != nil {
		return &tunnelShareError{kind: "request failed"}
	}
	req.Header.Set("Authorization", target.authorization)
	response, err := c.httpClient.Do(req)
	if err != nil {
		if errors.Is(requestContext.Err(), context.DeadlineExceeded) {
			return &tunnelShareError{kind: "timeout"}
		}
		return &tunnelShareError{kind: "unavailable"}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTunnelShareResponseBytes))
	if response.StatusCode != http.StatusNoContent && (response.StatusCode < 200 || response.StatusCode >= 300) {
		return &tunnelShareError{status: response.StatusCode, kind: "rejected"}
	}
	return nil
}

func parseTunnelShareResult(
	id string,
	shareURL string,
	createdAt string,
	expiresAtJSON json.RawMessage,
	lastAccessedAtJSON json.RawMessage,
) (tunnelShareResult, bool) {
	createdTime, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return tunnelShareResult{}, false
	}
	expiresAt, ok := parseNullableTunnelShareTime(expiresAtJSON)
	if !ok {
		return tunnelShareResult{}, false
	}
	lastAccessedAt, ok := parseNullableTunnelShareTime(lastAccessedAtJSON)
	if !ok {
		return tunnelShareResult{}, false
	}
	return tunnelShareResult{
		ID:             id,
		URL:            shareURL,
		CreatedAt:      createdTime.UnixMilli(),
		ExpiresAt:      expiresAt,
		LastAccessedAt: lastAccessedAt,
	}, true
}

func parseNullableTunnelShareTime(value json.RawMessage) (*int64, bool) {
	if len(value) == 0 {
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return nil, true
	}
	var timestamp string
	if err := json.Unmarshal(value, &timestamp); err != nil {
		return nil, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, timestamp)
	if err != nil {
		return nil, false
	}
	milliseconds := parsed.UnixMilli()
	return &milliseconds, true
}

func validTunnelShareResult(result tunnelShareResult, expiration string) bool {
	if !validTunnelShareMetadata(result) || result.LastAccessedAt != nil {
		return false
	}
	if expiration == "permanent" {
		return result.ExpiresAt == nil
	}
	return result.ExpiresAt != nil
}

func validTunnelShareMetadata(result tunnelShareResult) bool {
	if !validConversationShareID(result.ID) {
		return false
	}
	shareURL, err := url.Parse(strings.TrimSpace(result.URL))
	if err != nil || shareURL.User != nil || shareURL.Host == "" ||
		(shareURL.Scheme != "https" && !(shareURL.Scheme == "http" && isLoopbackHostname(shareURL.Hostname()))) {
		return false
	}
	if result.CreatedAt < 1_000_000_000_000 || result.CreatedAt > 9_007_199_254_740_991 {
		return false
	}
	if result.ExpiresAt != nil && *result.ExpiresAt <= result.CreatedAt {
		return false
	}
	if result.LastAccessedAt != nil && *result.LastAccessedAt < result.CreatedAt {
		return false
	}
	return true
}

func validConversationShareExpiration(value string) bool {
	switch value {
	case "5m", "30m", "1h", "3h", "1d", "5d", "15d", "30d", "permanent":
		return true
	default:
		return false
	}
}

func validConversationShareID(value string) bool {
	value = strings.TrimSpace(value)
	if len(value) == 0 || len(value) > 80 {
		return false
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' {
			continue
		}
		return false
	}
	return true
}

func mapTunnelShareError(err error) (int, string) {
	var tunnelErr *tunnelShareError
	if !errors.As(err, &tunnelErr) {
		return http.StatusBadGateway, "tunnel share request failed"
	}
	switch tunnelErr.status {
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusRequestEntityTooLarge:
		return tunnelErr.status, fmt.Sprintf("tunnel share request failed with status %d", tunnelErr.status)
	}
	if tunnelErr.kind == "timeout" {
		return http.StatusGatewayTimeout, "tunnel share request timed out"
	}
	return http.StatusBadGateway, "tunnel share service is unavailable"
}
