package server

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestParseTunnelShareTarget(t *testing.T) {
	for _, origin := range []string{
		"https://tunnel.example.test",
		"http://localhost:8080",
		"http://127.0.0.1:8080",
		"http://[::1]:8080",
	} {
		if _, err := parseTunnelShareTarget(origin, "Bearer site-token"); err != nil {
			t.Fatalf("valid origin %q: %v", origin, err)
		}
	}
	for _, origin := range []string{
		"http://tunnel.example.test",
		"https://127.0.0.2:8080",
		"https://demo.localhost:8080",
		"https://0.0.0.0:8080",
		"https://user@tunnel.example.test",
		"https://tunnel.example.test/path",
		"https://tunnel.example.test?token=bad",
	} {
		if _, err := parseTunnelShareTarget(origin, "Bearer site-token"); err == nil {
			t.Fatalf("invalid origin accepted: %q", origin)
		}
	}
	if _, err := parseTunnelShareTarget("https://tunnel.example.test", "Bearer token with-space"); err == nil {
		t.Fatal("invalid authorization accepted")
	}
}

func TestValidTunnelShareMetadataRejectsReservedLocalHosts(t *testing.T) {
	for _, shareURL := range []string{
		"https://127.0.0.2/share/share_abc",
		"https://demo.localhost/share/share_abc",
		"https://0.0.0.0/share/share_abc",
	} {
		if validTunnelShareMetadata(tunnelShareResult{
			ID:        "share_abc",
			URL:       shareURL,
			CreatedAt: 1_786_960_800_000,
		}) {
			t.Fatalf("reserved local share URL accepted: %q", shareURL)
		}
	}
}

func TestTunnelShareClientCreateForwardsExactHTMLAndValidatesResponse(t *testing.T) {
	html := []byte("<!doctype html><title>Shared</title>")
	client := newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if request.Method != http.MethodPost || request.URL.String() != "https://tunnel.example.test/api/desktop/shares" || !bytes.Equal(body, html) {
			t.Fatalf("unexpected request %s %s body=%q", request.Method, request.URL, body)
		}
		if request.Header.Get("Authorization") != "Bearer site-token" || request.Header.Get("Content-Type") != "text/html; charset=utf-8" || request.Header.Get(tunnelDocumentVersionHeader) != "1" || request.Header.Get(tunnelExpirationHeader) != "30d" || request.Header.Get(tunnelConversationIDHeader) != "chat-1" {
			t.Fatalf("unexpected request headers: %#v", request.Header)
		}
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"2026-08-17T10:00:00Z","expiresAt":"2026-09-16T10:00:00Z","lastAccessedAt":null}`,
			)),
		}, nil
	})})
	target, err := parseTunnelShareTarget("https://tunnel.example.test", "Bearer site-token")
	if err != nil {
		t.Fatal(err)
	}
	result, err := client.Create(context.Background(), target, "chat-1", html, "30d")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if result.ID != "share_abc" || result.CreatedAt != 1_786_960_800_000 || result.ExpiresAt == nil || *result.ExpiresAt <= result.CreatedAt || result.LastAccessedAt != nil {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestTunnelShareClientRejectsRedirectWithoutForwardingAuthorization(t *testing.T) {
	requests := 0
	client := newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		return &http.Response{
			StatusCode: http.StatusTemporaryRedirect,
			Header:     http.Header{"Location": []string{"https://attacker.example.test/steal"}},
			Body:       io.NopCloser(strings.NewReader("redirect")),
		}, nil
	})})
	target, _ := parseTunnelShareTarget("https://tunnel.example.test", "Bearer secret-token")
	_, err := client.Create(context.Background(), target, "chat-1", []byte("html"), "30d")
	if err == nil || requests != 1 {
		t.Fatalf("redirect should fail after one request: requests=%d err=%v", requests, err)
	}
}

func TestTunnelShareClientAcceptsPermanentAndRequiresExplicitNull(t *testing.T) {
	responses := []string{
		`{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"2026-08-17T10:00:00Z","expiresAt":null,"lastAccessedAt":null}`,
		`{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"2026-08-17T10:00:00Z","expiresAt":null}`,
	}
	client := newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Header.Get(tunnelExpirationHeader) != "permanent" {
			t.Fatalf("expiration header=%q", request.Header.Get(tunnelExpirationHeader))
		}
		response := responses[0]
		responses = responses[1:]
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(response)),
		}, nil
	})})
	target, _ := parseTunnelShareTarget("https://tunnel.example.test", "Bearer site-token")
	result, err := client.Create(context.Background(), target, "chat-1", []byte("html"), "permanent")
	if err != nil || result.ExpiresAt != nil {
		t.Fatalf("permanent result=%#v err=%v", result, err)
	}
	if _, err := client.Create(context.Background(), target, "chat-1", []byte("html"), "permanent"); err == nil {
		t.Fatal("missing nullable metadata should fail")
	}
}

func TestTunnelShareClientRejectsInvalidRFC3339Metadata(t *testing.T) {
	client := newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"not-a-time","expiresAt":null,"lastAccessedAt":null}`,
			)),
		}, nil
	})})
	target, _ := parseTunnelShareTarget("https://tunnel.example.test", "Bearer site-token")
	if _, err := client.Create(context.Background(), target, "chat-1", []byte("html"), "permanent"); err == nil {
		t.Fatal("invalid RFC3339 timestamp should fail")
	}
}

func TestTunnelShareClientListsMetadataAndConvertsTimes(t *testing.T) {
	client := newTunnelShareClient(&http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet || request.URL.String() != "https://tunnel.example.test/api/desktop/shares?conversationId=chat-1" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"items":[{"id":"share_abc","url":"https://share.example.test/share/share_abc","createdAt":"2026-08-17T10:00:00Z","expiresAt":null,"lastAccessedAt":"2026-08-17T10:05:00Z"}]}`,
			)),
		}, nil
	})})
	target, _ := parseTunnelShareTarget("https://tunnel.example.test", "Bearer site-token")
	items, err := client.List(context.Background(), target, "chat-1")
	if err != nil || len(items) != 1 || items[0].LastAccessedAt == nil || *items[0].LastAccessedAt != 1_786_961_100_000 {
		t.Fatalf("items=%#v err=%v", items, err)
	}
}

func TestConversationShareExpirationValidation(t *testing.T) {
	for _, value := range []string{"5m", "30m", "1h", "3h", "1d", "5d", "15d", "30d", "permanent"} {
		if !validConversationShareExpiration(value) {
			t.Fatalf("valid expiration rejected: %q", value)
		}
	}
	for _, value := range []string{"", " 30d ", "90d", "0", "forever"} {
		if validConversationShareExpiration(value) {
			t.Fatalf("invalid expiration accepted: %q", value)
		}
	}
}
