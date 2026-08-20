package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"agent-platform/internal/config"
	"agent-platform/internal/conversationexport"
	"agent-platform/internal/stream"
)

const conversationExportTestTemplate = `<!doctype html><html><head><meta name="conversation-export-profile" content="conversation-snapshot-json-v1"><meta name="conversation-export-asset-set" content="0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"><meta http-equiv="Content-Security-Policy" content="font-src __CONVERSATION_EXPORT_ASSET_ORIGIN__; style-src-elem __CONVERSATION_EXPORT_ASSET_ORIGIN__; script-src __CONVERSATION_EXPORT_ASSET_ORIGIN__"><link rel="stylesheet" href="__CONVERSATION_EXPORT_ASSET_ORIGIN__/assets/conversation-export/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/runtime.css" integrity="sha384-test" crossorigin="anonymous"></head><body><script id="conversation-snapshot" type="application/json">__CONVERSATION_EXPORT_SNAPSHOT_JSON_V1__</script><script src="__CONVERSATION_EXPORT_ASSET_ORIGIN__/assets/conversation-export/0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef/runtime.js" integrity="sha384-test" crossorigin="anonymous"></script></body></html>`

func TestHandleChatExportHTMLReturnsRenderedDocument(t *testing.T) {
	fixture := newTestFixture(t)
	seedCompletedConversationExport(t, fixture, "chat-html-export")
	fixture.server.conversationHTML = mustConversationHTMLRenderer(t)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/chat/export?chatId=chat-html-export&format=html", nil)
	req.Header.Set(tunnelOriginHeader, "http://127.0.0.1:11961")
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Content-Disposition") != `attachment; filename="rollback plan.html"` {
		t.Fatalf("unexpected headers: %#v", rec.Header())
	}
	if rec.Header().Get("Content-Length") != strconv.Itoa(rec.Body.Len()) {
		t.Fatalf("content-length=%q body=%d", rec.Header().Get("Content-Length"), rec.Body.Len())
	}
	body := rec.Body.String()
	if strings.Contains(body, conversationexport.TemplateMarker) || strings.Contains(body, conversationexport.AssetOriginMarker) || !strings.Contains(body, `"version":1`) || !strings.Contains(body, "rollback completed") || !strings.Contains(body, "http://127.0.0.1:11961/assets/conversation-export/") {
		t.Fatalf("unexpected rendered HTML: %s", body)
	}
}

func TestHandleChatExportHTMLRequiresSafeAssetOrigin(t *testing.T) {
	fixture := newTestFixture(t)
	seedCompletedConversationExport(t, fixture, "chat-html-origin")
	fixture.server.conversationHTML = mustConversationHTMLRenderer(t)
	for _, origin := range []string{"", "http://public.example.test", "https://example.test/path"} {
		req := httptest.NewRequest(http.MethodGet, "/api/chat/export?chatId=chat-html-origin&format=html", nil)
		req.Header.Set(tunnelOriginHeader, origin)
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("origin=%q status=%d body=%s", origin, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleChatExportHTMLUnavailableAndValidation(t *testing.T) {
	fixture := newTestFixture(t)
	for _, tc := range []struct {
		path string
		code int
	}{
		{path: "/api/chat/export?chatId=chat-1&format=html", code: http.StatusServiceUnavailable},
		{path: "/api/chat/export?chatId=../chat", code: http.StatusBadRequest},
		{path: "/api/chat/export?chatId=missing-chat", code: http.StatusNotFound},
	} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if rec.Code != tc.code {
			t.Fatalf("path=%s status=%d want=%d body=%s", tc.path, rec.Code, tc.code, rec.Body.String())
		}
	}
}

func TestHandleChatShareUploadsTheRenderedHTMLAndKeepsPrivateAuthorizationSeparate(t *testing.T) {
	fixture := newTestFixture(t)
	seedCompletedConversationExport(t, fixture, "chat-share-create")
	fixture.server.conversationHTML = mustConversationHTMLRenderer(t)
	var uploaded []byte
	fixture.server.conversationShares = newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get("Authorization") != "Bearer tunnel-site-token" {
			t.Fatalf("tunnel authorization=%q", request.Header.Get("Authorization"))
		}
		if request.Header.Get(tunnelExpirationHeader) != defaultShareExpiration {
			t.Fatalf("share expiration=%q", request.Header.Get(tunnelExpirationHeader))
		}
		if request.Header.Get(tunnelConversationIDHeader) != "chat-share-create" {
			t.Fatalf("conversation id=%q", request.Header.Get(tunnelConversationIDHeader))
		}
		var err error
		uploaded, err = io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read upload: %v", err)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"2026-08-17T10:00:00Z","expiresAt":"2026-09-16T10:00:00Z","lastAccessedAt":null}`,
			)),
		}, nil
	})})

	req := httptest.NewRequest(http.MethodPost, "/api/chat/share", strings.NewReader(`{"chatId":"chat-share-create"}`))
	req.Header.Set(tunnelOriginHeader, "https://tunnel.example.test")
	req.Header.Set(tunnelAuthorizationHeader, "Bearer tunnel-site-token")
	req.Header.Set("Authorization", "Bearer platform-token")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !bytes.Contains(uploaded, []byte(`"version":1`)) || bytes.Contains(uploaded, []byte(conversationexport.TemplateMarker)) || bytes.Contains(uploaded, []byte(conversationexport.AssetOriginMarker)) || !bytes.Contains(uploaded, []byte("https://tunnel.example.test/assets/conversation-export/")) {
		t.Fatalf("unexpected uploaded HTML: %s", uploaded)
	}
	var response struct {
		Code int               `json:"code"`
		Data tunnelShareResult `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Code != 0 || response.Data.ID != "share_abc" {
		t.Fatalf("unexpected response=%#v err=%v", response, err)
	}
}

func TestHandleChatShareValidatesAndForwardsExpiration(t *testing.T) {
	for _, expiration := range []string{"5m", "30m", "1h", "3h", "1d", "5d", "15d", "30d", "permanent"} {
		t.Run(expiration, func(t *testing.T) {
			fixture := newTestFixture(t)
			seedCompletedConversationExport(t, fixture, "chat-share-"+expiration)
			fixture.server.conversationHTML = mustConversationHTMLRenderer(t)
			fixture.server.conversationShares = newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.Header.Get(tunnelExpirationHeader) != expiration {
					t.Fatalf("expiration header=%q", request.Header.Get(tunnelExpirationHeader))
				}
				expiresAt := `"2026-09-16T10:00:00Z"`
				if expiration == "permanent" {
					expiresAt = "null"
				}
				return &http.Response{
					StatusCode: http.StatusCreated,
					Header:     make(http.Header),
					Body: io.NopCloser(strings.NewReader(
						`{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"2026-08-17T10:00:00Z","expiresAt":` + expiresAt + `,"lastAccessedAt":null}`,
					)),
				}, nil
			})})
			body := `{"chatId":"chat-share-` + expiration + `","expiration":"` + expiration + `"}`
			req := httptest.NewRequest(http.MethodPost, "/api/chat/share", strings.NewReader(body))
			req.Header.Set(tunnelOriginHeader, "https://tunnel.example.test")
			req.Header.Set(tunnelAuthorizationHeader, "Bearer tunnel-site-token")
			rec := httptest.NewRecorder()
			fixture.server.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}

	for _, body := range []string{
		`{"chatId":"chat-share-create","expiration":"90d"}`,
		`{"chatId":"chat-share-create","expiration":null}`,
		`{"chatId":"chat-share-create","expiration":300}`,
		`{"chatId":"chat-share-create","extra":true}`,
	} {
		fixture := newTestFixture(t)
		fixture.server.conversationHTML = mustConversationHTMLRenderer(t)
		req := httptest.NewRequest(http.MethodPost, "/api/chat/share", strings.NewReader(body))
		req.Header.Set(tunnelOriginHeader, "https://tunnel.example.test")
		req.Header.Set(tunnelAuthorizationHeader, "Bearer tunnel-site-token")
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s", body, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleChatSharesListProxiesMetadataAsEpochMilliseconds(t *testing.T) {
	fixture := newTestFixture(t)
	fixture.server.conversationShares = newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != "https://tunnel.example.test/api/desktop/shares?conversationId=chat-share-list" {
			t.Fatalf("unexpected Tunnel request %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer tunnel-site-token" {
			t.Fatalf("tunnel authorization=%q", request.Header.Get("Authorization"))
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"items":[{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"2026-08-17T10:00:00Z","expiresAt":null,"lastAccessedAt":"2026-08-17T10:05:00Z"}]}`,
			)),
		}, nil
	})})
	req := httptest.NewRequest(http.MethodGet, "/api/chat/shares?chatId=chat-share-list", nil)
	req.Header.Set(tunnelOriginHeader, "https://tunnel.example.test")
	req.Header.Set(tunnelAuthorizationHeader, "Bearer tunnel-site-token")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response struct {
		Code int `json:"code"`
		Data struct {
			Items []tunnelShareResult `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Code != 0 || len(response.Data.Items) != 1 {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	item := response.Data.Items[0]
	if item.CreatedAt != 1_786_960_800_000 || item.LastAccessedAt == nil || *item.LastAccessedAt != 1_786_961_100_000 {
		t.Fatalf("item=%#v", item)
	}
}

func TestHandleChatSharesListRejectsInvalidChatID(t *testing.T) {
	fixture := newTestFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/api/chat/shares?chatId=../bad", nil)
	req.Header.Set(tunnelOriginHeader, "https://tunnel.example.test")
	req.Header.Set(tunnelAuthorizationHeader, "Bearer tunnel-site-token")
	rec := httptest.NewRecorder()
	fixture.server.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleChatShareCORSRejectsBrowserRequestsForPrivateTunnelHeaders(t *testing.T) {
	fixture := newTestFixtureWithModelHandlerAndOptions(t, func(http.ResponseWriter, *http.Request) {}, testFixtureOptions{
		configure: func(cfg *config.Config) {
			cfg.CORS = config.CORSConfig{
				Enabled:               true,
				AllowedOriginPatterns: []string{"https://web.example.test"},
				AllowedMethods:        []string{http.MethodPost},
				AllowedHeaders:        []string{"Content-Type", tunnelOriginHeader, tunnelAuthorizationHeader},
			}
		},
	})
	req := httptest.NewRequest(http.MethodOptions, "/api/chat/share", nil)
	req.Header.Set("Origin", "https://web.example.test")
	req.Header.Set("Access-Control-Request-Headers", "content-type, "+tunnelAuthorizationHeader)
	rec := httptest.NewRecorder()

	fixture.server.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
	if rec.Header().Get("Access-Control-Allow-Headers") != "" {
		t.Fatalf("private headers were exposed to browser CORS: %#v", rec.Header())
	}
}

func seedCompletedConversationExport(t *testing.T, fixture testFixture, chatID string) {
	t.Helper()
	seedSearchableChat(t, fixture.chats, chatID)
	for _, event := range []stream.EventData{
		{Type: "content.snapshot", Timestamp: testEpochMillis + 1_999, Payload: map[string]any{"runId": "run-" + chatID, "text": "rollback completed"}},
		{Type: "run.complete", Timestamp: testEpochMillis + 2_000, Payload: map[string]any{"runId": "run-" + chatID}},
	} {
		if err := fixture.chats.AppendEvent(chatID, event); err != nil {
			t.Fatalf("append %s event: %v", event.Type, err)
		}
	}
}

func mustConversationHTMLRenderer(t *testing.T) *conversationexport.HTMLRenderer {
	t.Helper()
	renderer, err := conversationexport.NewHTMLRenderer([]byte(conversationExportTestTemplate))
	if err != nil {
		t.Fatalf("new HTML renderer: %v", err)
	}
	return renderer
}
