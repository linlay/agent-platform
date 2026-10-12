package reference

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"agent-platform/internal/chat"
	"agent-platform/internal/contracts"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/httpclient"

	runtimetypes "agent-platform/internal/runtime/types"
)

const (
	remoteReferenceMaxBytes = 50 * 1024 * 1024
	remoteReferenceTimeout  = 30 * time.Second
)

func (s *Service) Prepare(ctx context.Context, currentChatID string, references []runtimetypes.Reference) ([]runtimetypes.Reference, error) {
	if len(references) == 0 {
		return references, nil
	}

	prepared := make([]runtimetypes.Reference, 0, len(references))
	seen := map[string]struct{}{}
	for _, reference := range references {
		switch strings.ToLower(strings.TrimSpace(reference.Type)) {
		case "skill", "kbase":
			prepared = append(prepared, runtimetypes.Reference{Type: strings.ToLower(strings.TrimSpace(reference.Type)), ID: strings.TrimSpace(reference.ID)})
		case "selection":
			normalized, err := queryinput.NormalizeSelectionReference(reference)
			if err != nil {
				return nil, queryReferenceStatusError(http.StatusBadRequest, "invalid_reference", err.Error())
			}
			prepared = append(prepared, normalized)
		case "chat":
			normalized, err := s.PrepareChatReference(ctx, currentChatID, reference)
			if err != nil {
				return nil, err
			}
			identity := "chat\x00" + normalized.ID
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			prepared = append(prepared, normalized)
		case "site":
			normalized, err := PrepareSiteReference(reference)
			if err != nil {
				return nil, err
			}
			identity := "site\x00" + normalized.ID
			if _, exists := seen[identity]; exists {
				continue
			}
			seen[identity] = struct{}{}
			prepared = append(prepared, normalized)
		default:
			normalized, err := s.prepareFileResourceReference(ctx, currentChatID, reference)
			if err != nil {
				return nil, err
			}
			prepared = append(prepared, normalized)
		}
	}
	return prepared, nil
}

func (s *Service) prepareFileResourceReference(ctx context.Context, currentChatID string, reference runtimetypes.Reference) (runtimetypes.Reference, error) {
	rawURL := strings.TrimSpace(reference.URL)
	fileParam := ResourceFileParamForChat(currentChatID, rawURL)
	if fileParam == "" {
		return reference, nil
	}
	// The literal @chat/ path may hold characters that are not URL syntax.
	parsed, err := url.Parse(chat.BareChatScopeRef(rawURL))
	if err != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusBadRequest, "resource_reference_unavailable", "invalid resource reference URL")
	}
	if !parsed.IsAbs() {
		if s == nil || s.deps.Chats == nil {
			return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusServiceUnavailable, "resource_reference_unavailable", "resource references are unavailable")
		}
		if _, err := s.deps.Chats.ResolveResource(fileParam); err != nil {
			return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusBadRequest, "resource_reference_unavailable", "local resource reference was not found")
		}
		return reference, nil
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusBadRequest, "resource_reference_unavailable", "remote resource reference must use http or https")
	}
	return s.materializeRemoteResourceReference(ctx, currentChatID, reference, parsed)
}

func (s *Service) materializeRemoteResourceReference(
	ctx context.Context,
	currentChatID string,
	reference runtimetypes.Reference,
	parsed *url.URL,
) (runtimetypes.Reference, error) {
	if s == nil || s.deps.Chats == nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusServiceUnavailable, "resource_reference_unavailable", "resource references are unavailable")
	}
	if !chat.ValidChatID(strings.TrimSpace(currentChatID)) {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusBadRequest, "resource_reference_unavailable", "current chat is invalid")
	}
	requestCtx, cancel := context.WithTimeout(ctx, remoteReferenceTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusBadRequest, "resource_reference_unavailable", "invalid remote resource request")
	}
	resp, err := httpclient.NewClient(0).Do(req)
	if err != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusBadGateway, "resource_reference_unavailable", "remote resource download failed: "+err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusBadGateway,
			"resource_reference_unavailable",
			fmt.Sprintf("remote resource returned HTTP %d", resp.StatusCode),
		)
	}

	rawName := strings.TrimSpace(reference.Name)
	if rawName == "" {
		rawName = filepath.Base(filepath.FromSlash(ResourceFileParam(parsed.String())))
	}
	fileName := safeFilename(rawName)
	urlHash := sha256.Sum256([]byte(parsed.String()))
	relativePath := filepath.Join("references", fmt.Sprintf("%x-%s", urlHash[:8], fileName))
	targetPath := filepath.Join(s.deps.Chats.ChatDir(currentChatID), relativePath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusInternalServerError, "resource_reference_unavailable", err.Error())
	}
	temp, err := os.CreateTemp(filepath.Dir(targetPath), ".remote-reference-*")
	if err != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusInternalServerError, "resource_reference_unavailable", err.Error())
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	hasher := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(temp, hasher), io.LimitReader(resp.Body, remoteReferenceMaxBytes+1))
	closeErr := temp.Close()
	if copyErr != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusBadGateway, "resource_reference_unavailable", "remote resource download failed: "+copyErr.Error())
	}
	if closeErr != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusInternalServerError, "resource_reference_unavailable", closeErr.Error())
	}
	if size > remoteReferenceMaxBytes {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusRequestEntityTooLarge, "resource_reference_too_large", "remote resource exceeds 50 MiB")
	}
	if err := os.Chmod(tempPath, 0o644); err != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusInternalServerError, "resource_reference_unavailable", err.Error())
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(http.StatusInternalServerError, "resource_reference_unavailable", err.Error())
	}

	reference.Path = ""
	reference.Name = fileName
	reference.SizeBytes = &size
	reference.SHA256 = fmt.Sprintf("%x", hasher.Sum(nil))
	if mimeType := strings.TrimSpace(resp.Header.Get("Content-Type")); mimeType != "" {
		reference.MimeType = mimeType
	}
	reference.URL = "/api/resource?file=" + url.QueryEscape(filepath.ToSlash(filepath.Join(currentChatID, relativePath)))
	return reference, nil
}

func (s *Service) PrepareChatReference(ctx context.Context, currentChatID string, reference runtimetypes.Reference) (runtimetypes.Reference, error) {
	referencedChatID := strings.TrimSpace(reference.ID)
	if referencedChatID == "" {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusBadRequest,
			"chat_reference_unavailable",
			"chat reference id is required",
		)
	}
	if referencedChatID == strings.TrimSpace(currentChatID) {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusBadRequest,
			"chat_reference_self",
			"a chat cannot reference itself",
		)
	}
	if s == nil || s.deps.Chats == nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusServiceUnavailable,
			"chat_reference_unavailable",
			"chat references are unavailable",
		)
	}

	summary, err := s.deps.Chats.Summary(referencedChatID)
	if err != nil {
		return runtimetypes.Reference{}, err
	}
	if summary == nil {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusNotFound,
			"chat_reference_unavailable",
			"referenced chat was not found",
		)
	}
	if !QueryPrincipalCanReferenceChat(ctx, *summary) {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusForbidden,
			"chat_reference_forbidden",
			"referenced chat is not accessible",
		)
	}

	meta := map[string]any{
		"updatedAt": summary.UpdatedAt,
	}
	if agentKey := strings.TrimSpace(summary.AgentKey); agentKey != "" {
		meta["agentKey"] = agentKey
	}
	name := strings.TrimSpace(summary.ChatName)
	if name == "" {
		name = referencedChatID
	}
	return runtimetypes.Reference{
		ID:   referencedChatID,
		Type: "chat",
		Name: name,
		Meta: meta,
	}, nil
}

func QueryPrincipalCanReferenceChat(ctx context.Context, summary chat.Summary) bool {
	principal := runtimetypes.IdentityFromContext(ctx)
	if principal == nil || strings.TrimSpace(principal.Subject) == "" {
		return !strings.HasPrefix(strings.TrimSpace(summary.Source), queryinput.ChatSourceQueryPrefix)
	}
	const querySourcePrefix = queryinput.ChatSourceQueryPrefix
	source := strings.TrimSpace(summary.Source)
	if !strings.HasPrefix(source, querySourcePrefix) {
		return true
	}
	owner := strings.TrimSpace(strings.TrimPrefix(source, querySourcePrefix))
	return owner == "" || owner == strings.TrimSpace(principal.Subject)
}

func PrepareSiteReference(reference runtimetypes.Reference) (runtimetypes.Reference, error) {
	entryKey := strings.TrimSpace(reference.ID)
	name := strings.TrimSpace(reference.Name)
	if entryKey == "" || name == "" {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusBadRequest,
			"site_reference_unavailable",
			"site reference id and name are required",
		)
	}
	kind := ""
	if reference.Meta != nil {
		kind = strings.ToLower(strings.TrimSpace(fmt.Sprint(reference.Meta["kind"])))
	}
	if kind != "website" && kind != "webapp" {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusBadRequest,
			"site_reference_unavailable",
			"site reference kind must be website or webapp",
		)
	}
	prefix, suffix, ok := strings.Cut(entryKey, ":")
	if !ok || strings.TrimSpace(prefix) != kind || strings.TrimSpace(suffix) == "" {
		return runtimetypes.Reference{}, queryReferenceStatusError(
			http.StatusBadRequest,
			"site_reference_unavailable",
			"site reference entry key must match its kind",
		)
	}
	meta := map[string]any{"kind": kind}
	if updatedAt, ok := normalizedReferenceTimestamp(reference.Meta["updatedAt"]); ok {
		meta["updatedAt"] = updatedAt
	}
	normalized := runtimetypes.Reference{
		ID:   entryKey,
		Type: "site",
		Name: name,
		Meta: meta,
	}
	if rawURL := normalizedSiteReferenceURL(reference.URL); rawURL != "" {
		normalized.URL = rawURL
	}
	return normalized, nil
}

func normalizedReferenceTimestamp(value any) (int64, bool) {
	switch typed := value.(type) {
	case int64:
		return typed, typed >= 0
	case int:
		return int64(typed), typed >= 0
	case float64:
		normalized := int64(typed)
		return normalized, typed >= 0 && float64(normalized) == typed
	case string:
		normalized, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return normalized, err == nil && normalized >= 0
	default:
		return 0, false
	}
}

func normalizedSiteReferenceURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "http", "https":
		return parsed.String()
	default:
		return ""
	}
}

func queryReferenceStatusError(status int, code string, message string) *runtimetypes.RequestError {
	return &runtimetypes.RequestError{
		Status:  status,
		Code:    code,
		Message: message,
		Data: map[string]any{
			"error": map[string]any{
				"code":    code,
				"message": message,
			},
		},
	}
}

func safeFilename(name string) string {
	name = filepath.Base(strings.TrimSpace(name))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "upload.bin"
	}
	return name
}

type Dependencies struct{ Chats chat.Store }
type Service struct{ deps Dependencies }

func New(chats chat.Store) *Service { return &Service{deps: Dependencies{Chats: chats}} }

func WithSubject(ctx context.Context, subject string) context.Context {
	return runtimetypes.WithIdentity(ctx, &contracts.AuthIdentity{Subject: subject})
}
