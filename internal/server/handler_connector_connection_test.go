package server

import (
	"agent-platform/internal/connectorauth"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectorConfigurationIsSharedAcrossPrincipals(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	writeMCPConnectorForTest(t, f.server.deps.Config.Paths.EffectiveConnectorsCenterDir(), "demo")
	call := func(owner, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: owner}))
		r := httptest.NewRecorder()
		f.server.ServeHTTP(r, req)
		return r
	}
	read := func(owner string) connectorauth.Connection {
		r := call(owner, "GET", "/api/connectors/connection?id=demo", "")
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var v struct {
			Data connectorauth.Connection `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r.Body.String(), `"enabled"`) || strings.Contains(r.Body.String(), `"bound"`) {
			t.Fatal("legacy switch in response", r.Body.String())
		}
		return v.Data
	}
	if read("alice").Configured {
		t.Fatal("missing configuration defaults ready")
	}
	if r := call("alice", "POST", "/api/connectors/connect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if s := read("bob"); !s.Configured || s.Readiness != "ready" {
		t.Fatal(s)
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true}`); r.Code != 405 {
		t.Fatal("enable API retained", r.Code)
	}
	if r := call("alice", "POST", "/api/connectors/check?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := call("alice", "POST", "/api/connectors/disconnect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if read("bob").Configured {
		t.Fatal("disconnect retained configuration")
	}
	for _, path := range []string{"/api/connectors/connect?id=demo", "/api/connectors/disconnect?id=demo", "/api/connectors/check?id=demo"} {
		if r := call("alice", "GET", path, ""); r.Code != 405 {
			t.Fatal(r.Code)
		}
	}
	if r := call("alice", "GET", "/api/connectors/connection", ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"connections"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}
