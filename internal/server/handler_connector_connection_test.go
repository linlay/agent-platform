package server

import (
	"agent-platform/internal/connectorauth"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestConnectorConnectionIsSharedAcrossPrincipals(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	writeMCPConnectorForTest(t, fixture.server.deps.Config.Paths.EffectiveConnectorsCenterDir(), "demo")
	call := func(owner, method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req = req.WithContext(WithPrincipal(req.Context(), &Principal{Subject: owner}))
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, req)
		return rec
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true}`); r.Code != 409 {
		t.Fatal(r.Code, r.Body.String())
	}
	if r := call("alice", "POST", "/api/connectors/connect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	read := func(owner string) connectorauth.Connection {
		r := call(owner, "GET", "/api/connectors/connection?id=demo&userKey=alice", "")
		if r.Code != 200 {
			t.Fatal(r.Code, r.Body.String())
		}
		var v struct {
			Data connectorauth.Connection `json:"data"`
		}
		if err := json.Unmarshal(r.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
		return v.Data
	}
	a, b := read("alice"), read("bob")
	if !a.Bound || a.Enabled || !b.Bound || b.Enabled {
		t.Fatal(a, b)
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true,"userKey":"bob"}`); r.Code != 400 {
		t.Fatal("accepted unsupported user field", r.Code, r.Body.String())
	}
	if r := call("alice", "PUT", "/api/connectors/connection", `{"connectorId":"demo","enabled":true}`); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if shared := read("bob"); !shared.Bound || !shared.Enabled {
		t.Fatal("instance connection changed with request identity", shared)
	}
	if r := call("alice", "POST", "/api/connectors/disconnect?id=demo", ""); r.Code != 200 {
		t.Fatal(r.Code, r.Body.String())
	}
	if a := read("bob"); a.Bound || a.Enabled {
		t.Fatal(a)
	}
	if r := call("alice", "GET", "/api/connectors/connection", ""); r.Code != 200 || !strings.Contains(r.Body.String(), `"connections"`) {
		t.Fatal(r.Code, r.Body.String())
	}
}

func TestConnectorConnectionRejectsMalformedMutations(t *testing.T) {
	fixture := setupAdminRegistriesFixture(t)
	for _, body := range []string{`{}`, `{"connectorId":"demo"}`, `{"connectorId":"../demo","enabled":true}`, `{"connectorId":"demo","enabled":true,"enabled":false}`, `{"connectorId":"demo","enabled":true} {}`} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("PUT", "/api/connectors/connection", strings.NewReader(body)))
		if rec.Code != 400 {
			t.Fatalf("invalid mutation accepted: %d %s", rec.Code, rec.Body.String())
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("connection response is cacheable")
		}
	}
	for _, path := range []string{"/api/connectors/connect?id=demo", "/api/connectors/disconnect?id=demo"} {
		rec := httptest.NewRecorder()
		fixture.server.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != 405 {
			t.Fatalf("mutation accepted GET: %d %s", rec.Code, rec.Body.String())
		}
	}
}
