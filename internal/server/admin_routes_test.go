package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeAdmin 实现 GatewayAdmin，仅在内存里跟踪注册状态，便于单测。
type fakeAdmin struct {
	entries []GatewayAdminEntry
}

func (f *fakeAdmin) AdminRegister(e GatewayAdminEntry) error {
	for _, existing := range f.entries {
		if existing.ID == e.ID {
			return fmt.Errorf("duplicate id: %s", e.ID)
		}
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAdmin) AdminUnregister(id string) error {
	for i, existing := range f.entries {
		if existing.ID == id {
			f.entries = append(f.entries[:i], f.entries[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("not found: %s", id)
}

func (f *fakeAdmin) AdminList() []GatewayAdminEntry { return f.entries }

func newTestServerWithAdmin(admin GatewayAdmin) *Server {
	s := &Server{router: http.NewServeMux()}
	s.deps.GatewayAdmin = admin
	s.router.HandleFunc("/api/admin/gateways", s.adminOnly(s.handleAdminGateways))
	s.router.HandleFunc("/api/admin/gateways/", s.adminOnly(s.handleAdminGatewayByID))
	return s
}

func TestAdminGatewayReturns404WhenNoAdminConfigured(t *testing.T) {
	s := newTestServerWithAdmin(nil)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/gateways", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 when admin disabled, got %d", rec.Code)
	}
}

func TestAdminGatewayRejectsNonLoopback(t *testing.T) {
	s := newTestServerWithAdmin(&fakeAdmin{})
	req := httptest.NewRequest(http.MethodGet, "/api/admin/gateways", nil)
	req.RemoteAddr = "8.8.8.8:5555"
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403 from non-loopback, got %d", rec.Code)
	}
}

func TestAdminGatewayRegisterListUnregister(t *testing.T) {
	admin := &fakeAdmin{}
	s := newTestServerWithAdmin(admin)

	// POST register
	body, _ := json.Marshal(GatewayAdminEntry{ID: "wecom-1", Channel: "wecom", URL: "ws://localhost:1/ws", Token: "tok"})
	req := httptest.NewRequest(http.MethodPost, "/api/admin/gateways", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1000"
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("register failed: code=%d body=%s", rec.Code, rec.Body.String())
	}

	// GET list
	req = httptest.NewRequest(http.MethodGet, "/api/admin/gateways", nil)
	req.RemoteAddr = "127.0.0.1:1001"
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list failed: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "\"token\":\"tok\"") {
		t.Fatalf("token leaked in list response: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "wecom-1") {
		t.Fatalf("entry missing in list: %s", rec.Body.String())
	}

	// POST duplicate → 409
	req = httptest.NewRequest(http.MethodPost, "/api/admin/gateways", bytes.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1002"
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("want 409 on duplicate, got %d", rec.Code)
	}

	// DELETE
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/gateways/wecom-1", nil)
	req.RemoteAddr = "127.0.0.1:1003"
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unregister failed: %d %s", rec.Code, rec.Body.String())
	}

	// DELETE again → 404
	req = httptest.NewRequest(http.MethodDelete, "/api/admin/gateways/wecom-1", nil)
	req.RemoteAddr = "127.0.0.1:1004"
	rec = httptest.NewRecorder()
	s.router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("want 404 on re-delete, got %d", rec.Code)
	}
}
