package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"agent-platform/internal/api"
	"agent-platform/internal/chat"
	"agent-platform/internal/config"
	"agent-platform/internal/temppaths"
	"agent-platform/internal/ws"

	gws "github.com/gorilla/websocket"
)

type fixedGatewayResolver struct {
	baseURL string
	token   string
}

func (r fixedGatewayResolver) Resolve(chatID string) (string, string, bool) {
	return r.baseURL, r.token, r.baseURL != ""
}

func TestUploadAndResourceRoundTrip(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader("hello world")); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if err := writer.WriteField("requestId", "req_upload"); err != nil {
		t.Fatalf("write requestId: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	assertUUIDLike(t, response.Data.ChatID)
	summary, err := fixture.chats.Summary(response.Data.ChatID)
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if summary == nil || summary.Source != "" {
		t.Fatalf("expected upload-created chat to omit source, got %#v", summary)
	}
	if summary.ChatName != chat.PendingChatName {
		t.Fatalf("expected upload-created chat to use pending name, got %#v", summary)
	}
	wantUploadPath := filepath.Join(fixture.cfg.Paths.ChatsDir, response.Data.ChatID, "notes.txt")
	if response.Data.Upload.Path != wantUploadPath {
		t.Fatalf("upload path = %q, want %q", response.Data.Upload.Path, wantUploadPath)
	}
	if response.Data.Upload.URL != "notes.txt" {
		t.Fatalf("upload public URL = %q, want ChatScope relative path", response.Data.Upload.URL)
	}
	resourceKey, err := chat.BuildResourceKey(response.Data.ChatID, response.Data.Upload.URL)
	if err != nil {
		t.Fatalf("build resource key: %v", err)
	}
	resourceReq := httptest.NewRequest(http.MethodGet, "/api/resource?file="+url.QueryEscape(resourceKey), nil)
	resourceRec := httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected 200 resource, got %d", resourceRec.Code)
	}
	if got := resourceRec.Body.String(); got != "hello world" {
		t.Fatalf("unexpected resource content: %q", got)
	}
	if revision := resourceRec.Header().Get("X-Document-Revision"); revision == "" {
		t.Fatal("expected resource revision header")
	}

	matches, err := filepath.Glob(filepath.Join(fixture.cfg.Paths.ChatsDir, "*", "notes.txt"))
	if err != nil {
		t.Fatalf("glob upload path: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected uploaded file under %s, got %v", fixture.cfg.Paths.ChatsDir, matches)
	}
}

func TestResourceServesEncodedImageInlineAndRejectsUnsafeKeys(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-resource-image"
	if _, _, err := fixture.chats.EnsureChat(chatID, "mock-agent", "", "image"); err != nil {
		t.Fatal(err)
	}
	filename := "夏日 海报 #1%.png"
	imageBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 1, 2, 3}
	if err := os.MkdirAll(fixture.chats.ChatDir(chatID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.chats.ChatDir(chatID), filename), imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := chat.BuildResourceKey(chatID, filename)
	if err != nil {
		t.Fatal(err)
	}

	requestURL := "/api/resource?file=" + url.QueryEscape(ref)
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, requestURL, nil))
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), imageBytes) {
		t.Fatalf("resource response status=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") || !strings.Contains(got, "filename") {
		t.Fatalf("Content-Disposition=%q", got)
	}

	legacyRec := httptest.NewRecorder()
	legacyURL := "/api/resource?file=" + url.QueryEscape(chatID+"/"+filename)
	fixture.server.ServeHTTP(legacyRec, httptest.NewRequest(http.MethodGet, legacyURL, nil))
	if legacyRec.Code != http.StatusOK || !bytes.Equal(legacyRec.Body.Bytes(), imageBytes) {
		t.Fatalf("legacy resource response status=%d body=%q", legacyRec.Code, legacyRec.Body.Bytes())
	}

	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(fixture.chats.ChatDir(chatID), "escaped.png")); err == nil {
		symlinkRec := httptest.NewRecorder()
		fixture.server.ServeHTTP(symlinkRec, httptest.NewRequest(http.MethodGet, "/api/resource?file="+url.QueryEscape(chatID+"/escaped.png"), nil))
		if symlinkRec.Code == http.StatusOK {
			t.Fatal("symlink escape unexpectedly succeeded")
		}
	}

	downloadRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(downloadRec, httptest.NewRequest(http.MethodGet, requestURL+"&download=true", nil))
	if got := downloadRec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("download Content-Disposition=%q", got)
	}

	for _, unsafe := range []string{
		"/Users/alice/image.png",
		`C:\Users\alice\image.png`,
		"https://example.com/image.png",
		chatID + "/%2e%2e/secret.png",
	} {
		unsafeRec := httptest.NewRecorder()
		fixture.server.ServeHTTP(unsafeRec, httptest.NewRequest(http.MethodGet, "/api/resource?file="+url.QueryEscape(unsafe), nil))
		if unsafeRec.Code == http.StatusOK {
			t.Fatalf("unsafe resource key %q unexpectedly succeeded", unsafe)
		}
	}
}

func TestAbsoluteResourceEnforcesWorkspaceChatOwnerAndTeamBoundaries(t *testing.T) {
	fixture := newTestFixture(t)
	chatID := "chat-absolute-resource"
	if _, _, err := fixture.chats.EnsureChatWithSource(chatID, "mock-agent", "", "absolute", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	agentDef, ok := fixture.registry.AgentDefinition("mock-agent")
	if !ok {
		t.Fatal("mock-agent definition not found")
	}
	imagePath := filepath.Join(agentDef.Workspace.Root, "夏日 海报 #1%.png")
	imageBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 7, 8, 9}
	if err := os.WriteFile(imagePath, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	requestAbsolute := func(subject string, requestChatID string, path string, suffix string) *httptest.ResponseRecorder {
		t.Helper()
		requestURL := "/api/resource?chatId=" + url.QueryEscape(requestChatID) + "&file=" + url.QueryEscape(path) + suffix
		req := httptest.NewRequest(http.MethodGet, requestURL, nil)
		if subject != "" {
			req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: subject}))
		}
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		return rec
	}

	rec := requestAbsolute("alice", chatID, imagePath, "")
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), imageBytes) {
		t.Fatalf("workspace resource status=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("workspace Content-Type=%q", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "inline") {
		t.Fatalf("workspace Content-Disposition=%q", got)
	}
	downloadRec := requestAbsolute("alice", chatID, imagePath, "&download=true")
	if got := downloadRec.Header().Get("Content-Disposition"); !strings.HasPrefix(got, "attachment") {
		t.Fatalf("workspace download Content-Disposition=%q", got)
	}

	outsidePath, err := filepath.Abs("handler_resource_integration_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if state, _, _, classifyErr := temppaths.System().Classify(outsidePath); classifyErr == nil && state == temppaths.Outside {
		if outsideRec := requestAbsolute("alice", chatID, outsidePath, ""); outsideRec.Code != http.StatusForbidden {
			t.Fatalf("outside workspace status=%d body=%s", outsideRec.Code, outsideRec.Body.String())
		}
	}
	chatAbsolutePath := filepath.Join(fixture.chats.ChatDir(chatID), "chat-internal.png")
	if err := os.MkdirAll(filepath.Dir(chatAbsolutePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(chatAbsolutePath, imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if chatAbsoluteRec := requestAbsolute("alice", chatID, chatAbsolutePath, ""); chatAbsoluteRec.Code != http.StatusForbidden {
		t.Fatalf("chat absolute path status=%d body=%s", chatAbsoluteRec.Code, chatAbsoluteRec.Body.String())
	}
	if unauthenticatedRec := requestAbsolute("", chatID, imagePath, ""); unauthenticatedRec.Code != http.StatusForbidden {
		t.Fatalf("unauthenticated absolute status=%d body=%s", unauthenticatedRec.Code, unauthenticatedRec.Body.String())
	}
	if ticketOnlyRec := requestAbsolute("", chatID, imagePath, "&t=resource-ticket"); ticketOnlyRec.Code != http.StatusForbidden {
		t.Fatalf("ticket-only absolute status=%d body=%s", ticketOnlyRec.Code, ticketOnlyRec.Body.String())
	}
	if otherUserRec := requestAbsolute("bob", chatID, imagePath, ""); otherUserRec.Code != http.StatusForbidden {
		t.Fatalf("other user absolute status=%d body=%s", otherUserRec.Code, otherUserRec.Body.String())
	}

	otherChatID := "chat-absolute-other-owner"
	if _, _, err := fixture.chats.EnsureChatWithSource(otherChatID, "mock-agent", "", "other", api.ChatSourceQueryPrefix+"bob"); err != nil {
		t.Fatal(err)
	}
	if wrongChatRec := requestAbsolute("alice", otherChatID, imagePath, ""); wrongChatRec.Code != http.StatusForbidden {
		t.Fatalf("wrong chat status=%d body=%s", wrongChatRec.Code, wrongChatRec.Body.String())
	}

	teamChatID := "chat-team-absolute"
	if _, _, err := fixture.chats.EnsureChatWithSource(teamChatID, "", "team-1", "team", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	if teamRec := requestAbsolute("alice", teamChatID, imagePath, ""); teamRec.Code != http.StatusForbidden {
		t.Fatalf("team absolute status=%d body=%s", teamRec.Code, teamRec.Body.String())
	}

	tmpFile, err := os.CreateTemp("", "agent-platform-resource-*.png")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmpFile.Name()
	t.Cleanup(func() { _ = os.Remove(tmpPath) })
	if _, err := tmpFile.Write(imageBytes); err != nil {
		_ = tmpFile.Close()
		t.Fatal(err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatal(err)
	}
	if tmpRec := requestAbsolute("alice", chatID, tmpPath, ""); tmpRec.Code != http.StatusOK || !bytes.Equal(tmpRec.Body.Bytes(), imageBytes) {
		t.Fatalf("tmp resource status=%d body=%q", tmpRec.Code, tmpRec.Body.Bytes())
	}
	canonicalTmpPath, err := filepath.EvalSymlinks(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	if canonicalTmpRec := requestAbsolute("alice", chatID, canonicalTmpPath, ""); canonicalTmpRec.Code != http.StatusOK || !bytes.Equal(canonicalTmpRec.Body.Bytes(), imageBytes) {
		t.Fatalf("canonical tmp resource status=%d body=%q", canonicalTmpRec.Code, canonicalTmpRec.Body.Bytes())
	}
	if runtime.GOOS == "darwin" {
		aliasFile, aliasErr := os.CreateTemp("/tmp", "agent-platform-resource-alias-*.png")
		if aliasErr != nil {
			t.Fatal(aliasErr)
		}
		aliasPath := aliasFile.Name()
		t.Cleanup(func() { _ = os.Remove(aliasPath) })
		if _, aliasErr = aliasFile.Write(imageBytes); aliasErr != nil {
			_ = aliasFile.Close()
			t.Fatal(aliasErr)
		}
		if aliasErr = aliasFile.Close(); aliasErr != nil {
			t.Fatal(aliasErr)
		}
		privatePath, aliasErr := filepath.EvalSymlinks(aliasPath)
		if aliasErr != nil {
			t.Fatal(aliasErr)
		}
		for _, candidate := range []string{aliasPath, privatePath} {
			aliasRec := requestAbsolute("alice", chatID, candidate, "")
			if aliasRec.Code != http.StatusOK || !bytes.Equal(aliasRec.Body.Bytes(), imageBytes) {
				t.Fatalf("macOS tmp alias %q status=%d body=%q", candidate, aliasRec.Code, aliasRec.Body.Bytes())
			}
		}
	}

	tmpLink := tmpPath + "-link.png"
	t.Cleanup(func() { _ = os.Remove(tmpLink) })
	escapeTarget, err := filepath.Abs("handler_resource_integration_test.go")
	if err != nil {
		t.Fatal(err)
	}
	if state, _, _, classifyErr := temppaths.System().Classify(escapeTarget); classifyErr == nil && state == temppaths.Outside {
		if err := os.Symlink(escapeTarget, tmpLink); err != nil {
			t.Fatal(err)
		}
		if tmpLinkRec := requestAbsolute("alice", chatID, tmpLink, ""); tmpLinkRec.Code != http.StatusForbidden {
			t.Fatalf("tmp symlink escape status=%d body=%q", tmpLinkRec.Code, tmpLinkRec.Body.Bytes())
		}
	}

	if runtime.GOOS != "windows" {
		traversalPath := "/tmp/../" + strings.TrimPrefix(filepath.ToSlash(outsidePath), "/")
		if traversalRec := requestAbsolute("alice", chatID, traversalPath, ""); traversalRec.Code != http.StatusForbidden {
			t.Fatalf("cleaned tmp traversal status=%d body=%s", traversalRec.Code, traversalRec.Body.String())
		}
	}
}

func TestResourceFallsBackToArchivedChatCopy(t *testing.T) {
	fixture := newTestFixture(t)
	archives, err := chat.NewArchiveStore(fixture.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	fixture.server.deps.Archives = archives

	chatID := "chat-archived-resource"
	filename := "归档 海报.png"
	imageBytes := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n', 4, 5, 6}
	if err := os.MkdirAll(archives.ChatDir(chatID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archives.ChatDir(chatID), filename), imageBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := chat.BuildResourceKey(chatID, filename)
	if err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resource?file="+url.QueryEscape(ref), nil))
	if rec.Code != http.StatusOK || !bytes.Equal(rec.Body.Bytes(), imageBytes) {
		t.Fatalf("archived resource response status=%d body=%q", rec.Code, rec.Body.Bytes())
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Fatalf("Content-Type=%q", got)
	}
}

func TestResourceBearerCookieOwnershipAndInvalidBearerPrecedence(t *testing.T) {
	fixture := newTestFixture(t)
	privateKey, publicKeyPath := writeTestJWTKeyPair(t, fixture.cfg.Paths.ChatsDir)
	fixture.cfg.Auth = config.AuthConfig{Enabled: true, LocalPublicKeyFile: publicKeyPath, Issuer: "agent-platform-local"}
	server := newServerFromFixture(t, fixture)
	chatID := "chat-owned-resource"
	if _, _, err := fixture.chats.EnsureChatWithSource(chatID, "mock-agent", "", "owned", api.ChatSourceQueryPrefix+"alice"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixture.chats.ChatDir(chatID), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fixture.chats.ChatDir(chatID), "image.png"), []byte("owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	resourceURL := "/api/resource?file=" + url.QueryEscape(chatID+"/image.png")
	issue := func(subject string) string {
		return mustSignRS256JWT(t, privateKey, map[string]any{"sub": subject, "iss": "agent-platform-local", "exp": float64(4102444800)})
	}

	bearerReq := httptest.NewRequest(http.MethodGet, resourceURL, nil)
	bearerReq.Header.Set("Authorization", "Bearer "+issue("alice"))
	bearerRec := httptest.NewRecorder()
	server.ServeHTTP(bearerRec, bearerReq)
	if bearerRec.Code != http.StatusOK {
		t.Fatalf("owner bearer status=%d body=%s", bearerRec.Code, bearerRec.Body.String())
	}

	cookieReq := httptest.NewRequest(http.MethodGet, resourceURL, nil)
	cookieReq.AddCookie(&http.Cookie{Name: "access_token", Value: issue("alice")})
	cookieRec := httptest.NewRecorder()
	server.ServeHTTP(cookieRec, cookieReq)
	if cookieRec.Code != http.StatusOK {
		t.Fatalf("owner cookie status=%d body=%s", cookieRec.Code, cookieRec.Body.String())
	}

	agentDef, ok := fixture.registry.AgentDefinition("mock-agent")
	if !ok {
		t.Fatal("mock-agent definition not found")
	}
	workspaceImage := filepath.Join(agentDef.Workspace.Root, "authenticated.png")
	if err := os.WriteFile(workspaceImage, []byte("workspace-owned"), 0o644); err != nil {
		t.Fatal(err)
	}
	absoluteURL := "/api/resource?chatId=" + url.QueryEscape(chatID) + "&file=" + url.QueryEscape(workspaceImage)
	absoluteBearerReq := httptest.NewRequest(http.MethodGet, absoluteURL, nil)
	absoluteBearerReq.Header.Set("Authorization", "Bearer "+issue("alice"))
	absoluteBearerRec := httptest.NewRecorder()
	server.ServeHTTP(absoluteBearerRec, absoluteBearerReq)
	if absoluteBearerRec.Code != http.StatusOK || absoluteBearerRec.Body.String() != "workspace-owned" {
		t.Fatalf("absolute bearer status=%d body=%s", absoluteBearerRec.Code, absoluteBearerRec.Body.String())
	}
	absoluteCookieReq := httptest.NewRequest(http.MethodGet, absoluteURL, nil)
	absoluteCookieReq.AddCookie(&http.Cookie{Name: "access_token", Value: issue("alice")})
	absoluteCookieRec := httptest.NewRecorder()
	server.ServeHTTP(absoluteCookieRec, absoluteCookieReq)
	if absoluteCookieRec.Code != http.StatusOK || absoluteCookieRec.Body.String() != "workspace-owned" {
		t.Fatalf("absolute cookie status=%d body=%s", absoluteCookieRec.Code, absoluteCookieRec.Body.String())
	}

	otherReq := httptest.NewRequest(http.MethodGet, resourceURL, nil)
	otherReq.Header.Set("Authorization", "Bearer "+issue("bob"))
	otherRec := httptest.NewRecorder()
	server.ServeHTTP(otherRec, otherReq)
	if otherRec.Code != http.StatusForbidden {
		t.Fatalf("cross-owner status=%d body=%s", otherRec.Code, otherRec.Body.String())
	}

	invalidReq := httptest.NewRequest(http.MethodGet, resourceURL, nil)
	invalidReq.Header.Set("Authorization", "Bearer invalid")
	invalidReq.AddCookie(&http.Cookie{Name: "access_token", Value: issue("alice")})
	invalidRec := httptest.NewRecorder()
	server.ServeHTTP(invalidRec, invalidReq)
	if invalidRec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid bearer with valid cookie status=%d body=%s", invalidRec.Code, invalidRec.Body.String())
	}
}

func TestUploadReturnsContainerPathWhenAgentUsesContainerRuntime(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w,
			`{"choices":[{"delta":{"content":"Go runtime "}}]}`,
			`{"choices":[{"delta":{"content":"test response"},"finish_reason":"stop"}]}`,
			`[DONE]`,
		)
	}, testFixtureOptions{configure: func(cfg *config.Config) {
		cfg.ContainerHub.ResolvedEngine = "docker"
	}})
	server := fixture.server

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader("hello world")); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if err := writer.WriteField("agentKey", "mock-agent"); err != nil {
		t.Fatalf("write agentKey: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	if response.Data.Upload.Path != "/chat/notes.txt" {
		t.Fatalf("upload path = %q", response.Data.Upload.Path)
	}
	if response.Data.Upload.URL != "notes.txt" {
		t.Fatalf("upload URL = %q", response.Data.Upload.URL)
	}
	if strings.Contains(rec.Body.String(), "sandboxPath") {
		t.Fatalf("upload response must not include sandboxPath: %s", rec.Body.String())
	}
}

func TestQueryAfterUploadDoesNotEmitChatStartInLiveStream(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server
	upload := postTestUpload(t, server, "", "req_upload_before_query", "notes.txt", "hello world")

	size := upload.Upload.SizeBytes
	queryBody, err := json.Marshal(api.QueryRequest{
		ChatID:   upload.ChatID,
		Message:  "summarize the upload",
		AgentKey: "mock-agent",
		References: []api.Reference{{
			ID: upload.Upload.ID, Type: upload.Upload.Type, Name: upload.Upload.Name,
			Path: upload.Upload.Path, MimeType: upload.Upload.MimeType, SizeBytes: &size,
			URL: upload.Upload.URL, SHA256: upload.Upload.SHA256,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/query", bytes.NewReader(queryBody))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 query, got %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `"type":"chat.start"`) {
		t.Fatalf("did not expect chat.start in query live stream after upload-created chat, got %s", body)
	}
	assertSSEEventOrder(t, body, "request.query", "run.start")

	chatRec := httptest.NewRecorder()
	server.ServeHTTP(chatRec, httptest.NewRequest(http.MethodGet, "/api/chat?chatId="+upload.ChatID, nil))
	var detail api.ApiResponse[api.ChatDetailResponse]
	if err := json.Unmarshal(chatRec.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode chat detail: %v", err)
	}
	assertPersistedEventsStartWith(t, detail.Data.Events, "chat.start", "request.query", "run.start")
}

func TestToolResultEndpointServesHiddenResultAndResourceRejectsIt(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server
	chatID := "chat-tool-result"
	resultDir := filepath.Join(fixture.chats.ChatDir(chatID), chat.ToolRootDirName, chat.ToolResultsDirName)
	if err := os.MkdirAll(resultDir, 0o755); err != nil {
		t.Fatalf("mkdir tool result dir: %v", err)
	}
	resultJSON := `{"stdout":"new output","exitCode":0}`
	if err := os.WriteFile(filepath.Join(resultDir, "call_1.json"), []byte(resultJSON), 0o644); err != nil {
		t.Fatalf("write tool result: %v", err)
	}

	resourceRec := httptest.NewRecorder()
	server.ServeHTTP(resourceRec, httptest.NewRequest(http.MethodGet, "/api/resource?file=chat-tool-result%2F.tools%2Fresults%2Fcall_1.json", nil))
	if resourceRec.Code != http.StatusForbidden {
		t.Fatalf("expected .tools result to be forbidden via resource, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}

	resultRec := httptest.NewRecorder()
	server.ServeHTTP(resultRec, httptest.NewRequest(http.MethodGet, "/api/tool-result?chatId=chat-tool-result&path=.tools%2Fresults%2Fcall_1.json", nil))
	if resultRec.Code != http.StatusOK {
		t.Fatalf("expected 200 tool result, got %d: %s", resultRec.Code, resultRec.Body.String())
	}
	if strings.TrimSpace(resultRec.Body.String()) != resultJSON {
		t.Fatalf("unexpected tool result body: %q", resultRec.Body.String())
	}

	oldPathRec := httptest.NewRecorder()
	oldToolResultsPath := "." + "tool-results%2Fcall_1.json"
	server.ServeHTTP(oldPathRec, httptest.NewRequest(http.MethodGet, "/api/tool-result?chatId=chat-tool-result&path="+oldToolResultsPath, nil))
	if oldPathRec.Code != http.StatusBadRequest {
		t.Fatalf("expected old tool result path rejected, got %d: %s", oldPathRec.Code, oldPathRec.Body.String())
	}

	traversalRec := httptest.NewRecorder()
	server.ServeHTTP(traversalRec, httptest.NewRequest(http.MethodGet, "/api/tool-result?chatId=chat-tool-result&path=.tools%2Fresults%2F..%2Fcall_1.json", nil))
	if traversalRec.Code != http.StatusBadRequest {
		t.Fatalf("expected traversal path rejected, got %d", traversalRec.Code)
	}

	archives, err := chat.NewArchiveStore(fixture.cfg.Paths.ChatsDir)
	if err != nil {
		t.Fatalf("new archive store: %v", err)
	}
	server.deps.Archives = archives
	archiveDir := filepath.Join(archives.ChatDir("chat-tool-result-archived"), chat.ToolRootDirName, chat.ToolResultsDirName)
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		t.Fatalf("mkdir archived tool result dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(archiveDir, "call_2.json"), []byte(`{"stdout":"archived"}`), 0o644); err != nil {
		t.Fatalf("write archived tool result: %v", err)
	}
	archiveRec := httptest.NewRecorder()
	server.ServeHTTP(archiveRec, httptest.NewRequest(http.MethodGet, "/api/tool-result?chatId=chat-tool-result-archived&path=.tools%2Fresults%2Fcall_2.json", nil))
	if archiveRec.Code != http.StatusOK {
		t.Fatalf("expected archived tool result 200, got %d: %s", archiveRec.Code, archiveRec.Body.String())
	}
	if strings.TrimSpace(archiveRec.Body.String()) != `{"stdout":"archived"}` {
		t.Fatalf("unexpected archived tool result body: %q", archiveRec.Body.String())
	}
}

func TestUploadIDsIncrementWithinChat(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	first := postTestUpload(t, server, "chat_upload_ids", "upload_1", "first.txt", "first")
	second := postTestUpload(t, server, "chat_upload_ids", "upload_2", "second.txt", "second")
	otherChat := postTestUpload(t, server, "chat_upload_ids_other", "upload_3", "other.txt", "other")

	if first.Upload.ID != "r01" {
		t.Fatalf("expected first upload id r01, got %#v", first.Upload)
	}
	if second.Upload.ID != "r02" {
		t.Fatalf("expected second upload id r02, got %#v", second.Upload)
	}
	if otherChat.Upload.ID != "r01" {
		t.Fatalf("expected other chat to start at r01, got %#v", otherChat.Upload)
	}
}

func TestInternalUploadUsesNextChatUploadID(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	first := postTestUpload(t, server, "chat_internal_upload_ids", "upload_1", "first.txt", "first")
	if first.Upload.ID != "r01" {
		t.Fatalf("expected first upload id r01, got %#v", first.Upload)
	}

	status, body, err := server.ExecuteInternalUpload(context.Background(), "chat_internal_upload_ids", "upload_internal", "internal.txt", "text/plain", []byte("internal"))
	if err != nil {
		t.Fatalf("internal upload: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", status, string(body))
	}
	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatalf("decode internal upload response: %v", err)
	}
	if response.Data.Upload.ID != "r02" {
		t.Fatalf("expected internal upload id r02, got %#v", response.Data.Upload)
	}
}

func TestUploadIDSeedsFromExistingRootUploadFiles(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	_, _, err := fixture.chats.EnsureChat("chat_root_upload_ids", "", "", "")
	if err != nil {
		t.Fatalf("ensure chat: %v", err)
	}
	chatDir := fixture.chats.ChatDir("chat_root_upload_ids")
	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		t.Fatalf("create chat dir: %v", err)
	}
	for name, content := range map[string]string{
		"existing-one.txt": "one",
		"existing-two.txt": "two",
	} {
		if err := os.WriteFile(filepath.Join(chatDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write fixture file %s: %v", name, err)
		}
	}
	if err := os.MkdirAll(filepath.Join(chatDir, "artifacts"), 0o755); err != nil {
		t.Fatalf("write fixture directory: %v", err)
	}

	response := postTestUpload(t, server, "chat_root_upload_ids", "upload_root", "new.txt", "new")
	if response.Upload.ID != "r03" {
		t.Fatalf("expected seeded upload id r03, got %#v", response.Upload)
	}
}

func TestConcurrentUploadsGetUniqueChatUploadIDs(t *testing.T) {
	fixture := newTestFixture(t)
	server := fixture.server

	const total = 8
	ids := make(chan string, total)
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			name := "file-" + strconv.Itoa(i) + ".txt"
			response := postTestUpload(t, server, "chat_concurrent_upload_ids", "upload_concurrent_"+strconv.Itoa(i), name, "body")
			ids <- response.Upload.ID
		}()
	}
	wg.Wait()
	close(ids)

	seen := map[string]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatalf("duplicate upload id %q", id)
		}
		seen[id] = true
	}
	if len(seen) != total {
		t.Fatalf("expected %d ids, got %v", total, seen)
	}
	for i := 1; i <= total; i++ {
		id := "r" + fmt.Sprintf("%02d", i)
		if !seen[id] {
			t.Fatalf("expected id %s in %v", id, seen)
		}
	}
}

func postTestUpload(t *testing.T, server *Server, chatID string, requestID string, fileName string, content string) api.UploadResponse {
	t.Helper()

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader(content)); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if requestID != "" {
		if err := writer.WriteField("requestId", requestID); err != nil {
			t.Fatalf("write requestId: %v", err)
		}
	}
	if chatID != "" {
		if err := writer.WriteField("chatId", chatID); err != nil {
			t.Fatalf("write chatId: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}
	return response.Data
}

func uploadResourceRequestURL(t *testing.T, upload api.UploadResponse) string {
	t.Helper()
	resourceKey, err := chat.BuildResourceKey(upload.ChatID, upload.Upload.URL)
	if err != nil {
		t.Fatalf("build upload resource key: %v", err)
	}
	return "/api/resource?file=" + url.QueryEscape(resourceKey)
}

func TestResourceRoundTripRequiresValidTicketWhenEnabled(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.ResourceTicket = config.ResourceTicketConfig{
				Secret:     "ticket-secret",
				TTLSeconds: 300,
			}
		},
	})
	server := fixture.server

	payload := &bytes.Buffer{}
	writer := multipart.NewWriter(payload)
	part, err := writer.CreateFormFile("file", "notes.txt")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	if _, err := io.Copy(part, strings.NewReader("hello ticket")); err != nil {
		t.Fatalf("write upload body: %v", err)
	}
	if err := writer.WriteField("chatId", "chat_ticket"); err != nil {
		t.Fatalf("write chatId: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/upload", payload)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var response api.ApiResponse[api.UploadResponse]
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode upload response: %v", err)
	}

	resourceURL := uploadResourceRequestURL(t, response.Data)
	resourceReq := httptest.NewRequest(http.MethodGet, resourceURL, nil)
	resourceRec := httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 without ticket, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}

	validTicket := fixture.server.ticketService.Issue("tester", response.Data.ChatID)
	resourceReq = httptest.NewRequest(http.MethodGet, resourceURL+"&t="+url.QueryEscape(validTicket), nil)
	resourceRec = httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected 200 with valid ticket, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}
	if got := resourceRec.Body.String(); got != "hello ticket" {
		t.Fatalf("unexpected resource body %q", got)
	}

	wrongTicket := fixture.server.ticketService.Issue("tester", "chat_other")
	resourceReq = httptest.NewRequest(http.MethodGet, resourceURL+"&t="+url.QueryEscape(wrongTicket), nil)
	resourceRec = httptest.NewRecorder()
	server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusForbidden {
		t.Fatalf("expected 403 with mismatched ticket, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}
}

func TestWebSocketUploadDownloadsGatewayURLAndReturnsUploadTicket(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	fileBody := []byte("gateway upload body")
	expectedSHA := sha256.Sum256(fileBody)
	var downloadAuthorized atomic.Bool
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer gateway-upload-token" {
			http.Error(w, "missing auth", http.StatusUnauthorized)
			return
		}
		downloadAuthorized.Store(true)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write(fileBody)
	}))
	defer gateway.Close()
	fixture.server.deps.GatewayResolver = fixedGatewayResolver{
		baseURL: gateway.URL,
		token:   "gateway-upload-token",
	}

	server := newLoopbackServer(t, fixture.server)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()

	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/upload",
		ID:    "req_upload_ws",
		Payload: ws.MarshalPayload(map[string]any{
			"chatId":    "chat_ws_upload",
			"requestId": "req_upload_ws",
			"upload": map[string]any{
				"id":        "upload_1",
				"type":      "file",
				"name":      "gateway.txt",
				"mimeType":  "text/plain; charset=utf-8",
				"sizeBytes": len(fileBody),
				"sha256":    hex.EncodeToString(expectedSHA[:]),
				"url":       gateway.URL,
			},
		}),
	}); err != nil {
		t.Fatalf("write websocket upload: %v", err)
	}

	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("decode websocket upload frame: %v", err)
		}
		return meta.Frame == ws.FrameResponse && meta.ID == "req_upload_ws"
	})
	var frame struct {
		Frame string             `json:"frame"`
		Type  string             `json:"type"`
		ID    string             `json:"id"`
		Code  int                `json:"code"`
		Msg   string             `json:"msg"`
		Data  api.UploadResponse `json:"data"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket upload response: %v", err)
	}
	if frame.Frame != ws.FrameResponse || frame.Type != "/api/upload" || frame.ID != "req_upload_ws" || frame.Code != 0 {
		t.Fatalf("unexpected websocket upload response: %s", string(raw))
	}
	if !downloadAuthorized.Load() {
		t.Fatalf("expected gateway download request to carry auth token")
	}
	if frame.Data.ChatID != "chat_ws_upload" {
		t.Fatalf("expected chat_ws_upload, got %#v", frame.Data)
	}
	if frame.Data.Upload.Name != "gateway.txt" {
		t.Fatalf("expected uploaded name to be preserved, got %#v", frame.Data.Upload)
	}
	if frame.Data.Upload.MimeType != "text/plain; charset=utf-8" {
		t.Fatalf("expected mime type to be preserved, got %#v", frame.Data.Upload)
	}
	if frame.Data.Upload.SizeBytes != int64(len(fileBody)) {
		t.Fatalf("expected size %d, got %#v", len(fileBody), frame.Data.Upload)
	}
	if frame.Data.Upload.SHA256 != hex.EncodeToString(expectedSHA[:]) {
		t.Fatalf("expected sha256 to match, got %#v", frame.Data.Upload)
	}

	resourceReq := httptest.NewRequest(http.MethodGet, uploadResourceRequestURL(t, frame.Data), nil)
	resourceRec := httptest.NewRecorder()
	fixture.server.ServeHTTP(resourceRec, resourceReq)
	if resourceRec.Code != http.StatusOK {
		t.Fatalf("expected uploaded resource to be readable, got %d: %s", resourceRec.Code, resourceRec.Body.String())
	}
	if got := resourceRec.Body.String(); got != string(fileBody) {
		t.Fatalf("unexpected uploaded resource body %q", got)
	}
}

func TestWebSocketResourcePushesLocalFileToGateway(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	status, body, err := fixture.server.ExecuteInternalUpload(context.Background(), "chat_ws_resource", "resource-req", "resource.txt", "text/plain", []byte("resource body"))
	if err != nil {
		t.Fatalf("create resource fixture: %v", err)
	}
	if status != http.StatusOK {
		t.Fatalf("create resource fixture status=%d body=%s", status, string(body))
	}

	var gotAuth, gotContentType, gotBody atomic.Value
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		gotContentType.Store(r.Header.Get("Content-Type"))
		data, _ := io.ReadAll(r.Body)
		gotBody.Store(string(data))
		w.WriteHeader(http.StatusOK)
	}))
	defer gateway.Close()
	fixture.server.deps.GatewayResolver = fixedGatewayResolver{
		baseURL: gateway.URL,
		token:   "gateway-resource-token",
	}

	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/resource",
		ID:    "req_resource_ws",
		Payload: ws.MarshalPayload(map[string]any{
			"file":    "chat_ws_resource/resource.txt",
			"pushURL": gateway.URL + "/api/push/ticket-1",
		}),
	}); err != nil {
		t.Fatalf("write websocket resource: %v", err)
	}
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("decode websocket resource frame: %v", err)
		}
		return meta.Frame == ws.FrameResponse && meta.ID == "req_resource_ws"
	})
	var frame ws.ResponseFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket resource response: %v", err)
	}
	if frame.Type != "/api/resource" || frame.Code != 0 {
		t.Fatalf("unexpected websocket resource response: %s", string(raw))
	}
	if got, _ := gotAuth.Load().(string); got != "Bearer gateway-resource-token" {
		t.Fatalf("expected gateway auth header, got %q", got)
	}
	if got, _ := gotContentType.Load().(string); !strings.HasPrefix(got, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", got)
	}
	if got, _ := gotBody.Load().(string); got != "resource body" {
		t.Fatalf("unexpected pushed resource body %q", got)
	}
}

func TestWebSocketResourceRejectsMissingLocalFile(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
		writeProviderSSE(t, w, `[DONE]`)
	}, testFixtureOptions{
		notifications: ws.NewHub(),
		configure: func(cfg *config.Config) {
			cfg.WebSocket.WriteQueueSize = 4
			cfg.WebSocket.PingInterval = 30000
		},
	})

	server := newLoopbackServer(t, fixture.server)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
	conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer conn.Close()
	waitForPushFrameType(t, conn, "connected")

	if err := conn.WriteJSON(ws.RequestFrame{
		Frame: ws.FrameRequest,
		Type:  "/api/resource",
		ID:    "req_missing_resource",
		Payload: ws.MarshalPayload(map[string]any{
			"file":    "chat_missing/nope.txt",
			"pushURL": "/api/push/ticket-1",
		}),
	}); err != nil {
		t.Fatalf("write websocket resource: %v", err)
	}
	raw := waitForWebSocketFrame(t, conn, func(data []byte) bool {
		var meta struct {
			Frame string `json:"frame"`
			ID    string `json:"id"`
		}
		if err := json.Unmarshal(data, &meta); err != nil {
			t.Fatalf("decode websocket resource error frame: %v", err)
		}
		return meta.Frame == ws.FrameError && meta.ID == "req_missing_resource"
	})
	var frame ws.ErrorFrame
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatalf("decode websocket resource error: %v", err)
	}
	if frame.Type != "resource_not_found" || frame.Code != http.StatusNotFound {
		t.Fatalf("unexpected websocket resource error: %s", string(raw))
	}
}

func TestWebSocketUploadRejectsInvalidUploadMetadata(t *testing.T) {
	tests := []struct {
		name        string
		payload     map[string]any
		errContains string
	}{
		{
			name: "size mismatch",
			payload: map[string]any{
				"chatId":    "chat_ws_upload",
				"requestId": "req_upload_size",
				"fileName":  "gateway.txt",
				"url":       "/download",
				"sizeBytes": 999,
			},
			errContains: "sizeBytes mismatch",
		},
		{
			name: "sha mismatch",
			payload: map[string]any{
				"chatId":    "chat_ws_upload",
				"requestId": "req_upload_sha",
				"fileName":  "gateway.txt",
				"url":       "/download",
				"sha256":    strings.Repeat("0", 64),
			},
			errContains: "sha256 mismatch",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newTestFixtureWithModelHandlerAndOptions(t, func(w http.ResponseWriter, r *http.Request) {
				writeProviderSSE(t, w, `[DONE]`)
			}, testFixtureOptions{
				notifications: ws.NewHub(),
				configure: func(cfg *config.Config) {
					cfg.WebSocket.WriteQueueSize = 4
					cfg.WebSocket.PingInterval = 30000
				},
			})

			gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("gateway upload body"))
			}))
			defer gateway.Close()

			server := newLoopbackServer(t, fixture.server)
			defer server.Close()

			wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"
			conn, _, err := gws.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial websocket: %v", err)
			}
			defer conn.Close()

			waitForPushFrameType(t, conn, "connected")

			payload := make(map[string]any, len(tc.payload))
			for key, value := range tc.payload {
				payload[key] = value
			}
			payload["url"] = gateway.URL + "/download"

			if err := conn.WriteJSON(ws.RequestFrame{
				Frame:   ws.FrameRequest,
				Type:    "/api/upload",
				ID:      "req_invalid_upload",
				Payload: ws.MarshalPayload(payload),
			}); err != nil {
				t.Fatalf("write websocket upload: %v", err)
			}

			_, raw, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read websocket upload error: %v", err)
			}
			var frame ws.ErrorFrame
			if err := json.Unmarshal(raw, &frame); err != nil {
				t.Fatalf("decode websocket upload error: %v", err)
			}
			if frame.Frame != ws.FrameError || frame.Type != "invalid_upload_metadata" || frame.Code != http.StatusBadRequest {
				t.Fatalf("unexpected websocket upload error: %s", string(raw))
			}
			if !strings.Contains(frame.Msg, tc.errContains) {
				t.Fatalf("expected error to contain %q, got %#v", tc.errContains, frame)
			}
		})
	}
}
