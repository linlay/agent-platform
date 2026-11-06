package server

import (
	"context"
	"net/http/httptest"
	"testing"
)

func TestWebClientTargetFromHTTPRequestUsesSurfaceAndAuthenticatedBoundary(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/query", nil)
	request.Header.Set(webClientSurfaceIDHeader, " surface-1 ")
	request = request.WithContext(WithPrincipal(context.Background(), &Principal{
		Subject: "user-1",
		Claims:  map[string]any{"deviceId": "device-1"},
	}))

	target := webClientTargetFromHTTPRequest(request)
	if target.Subject != "user-1" || target.SurfaceID != "surface-1" {
		t.Fatalf("unexpected webclient target: %#v", target)
	}
	if target.SessionID != "" || target.BoundaryKey != "subject:user-1\x00device:device-1" {
		t.Fatalf("HTTP target must remain logical: %#v", target)
	}
}

func TestWebClientTargetFromHTTPRequestRequiresSurface(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/query", nil)
	request = request.WithContext(WithPrincipal(context.Background(), &Principal{
		Subject: "user-1",
		Claims:  map[string]any{"deviceId": "device-1"},
	}))
	if target := webClientTargetFromHTTPRequest(request); !target.IsZero() {
		t.Fatalf("expected no target without surface header: %#v", target)
	}
}

func TestWebClientTargetFromHTTPRequestRequiresDeviceBoundary(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/query", nil)
	request.Header.Set(webClientSurfaceIDHeader, "surface-1")
	request = request.WithContext(WithPrincipal(context.Background(), &Principal{Subject: "user-1"}))
	if target := webClientTargetFromHTTPRequest(request); !target.IsZero() {
		t.Fatalf("expected no target without authenticated device boundary: %#v", target)
	}
}

func TestWebClientTargetFromHTTPRequestUsesDeviceHeaderWithoutClaim(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/query", nil)
	request.Header.Set(webClientDeviceIDHeader, " device-browser ")
	request.Header.Set(webClientSurfaceIDHeader, "surface-browser")

	target := webClientTargetFromHTTPRequest(request)
	if target.BoundaryKey != "device:device-browser" || target.SurfaceID != "surface-browser" {
		t.Fatalf("unexpected anonymous webclient target: %#v", target)
	}
}

func TestWebClientTargetFromHTTPRequestPrefersAuthenticatedDeviceClaim(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/query", nil)
	request.Header.Set(webClientDeviceIDHeader, "device-header")
	request.Header.Set(webClientSurfaceIDHeader, "surface-1")
	request = request.WithContext(WithPrincipal(context.Background(), &Principal{
		Subject: "user-1",
		Claims:  map[string]any{"deviceId": "device-claim"},
	}))

	target := webClientTargetFromHTTPRequest(request)
	if target.BoundaryKey != "subject:user-1\x00device:device-claim" {
		t.Fatalf("authenticated device claim must win: %#v", target)
	}
}
