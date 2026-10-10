package server

import (
	"agent-platform/internal/kbasescenter"
	"context"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKBasesCenterHTTP(t *testing.T) {
	service, err := kbasescenter.New(context.Background(), t.TempDir(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{deps: Dependencies{KBasesCenter: service}}
	request := func(method, path, body string, want int) json.RawMessage {
		t.Helper()
		r := httptest.NewRecorder()
		s.handleKBasesCenter(r, httptest.NewRequest(method, path, strings.NewReader(body)))
		if r.Code != want {
			t.Fatalf("%s %s: %d %s", method, path, r.Code, r.Body.String())
		}
		var e struct {
			Code int
			Data json.RawMessage
		}
		if err := json.Unmarshal(r.Body.Bytes(), &e); err != nil {
			t.Fatal(err)
		}
		if want == 200 && e.Code != 0 {
			t.Fatal("nonzero success code")
		}
		return e.Data
	}
	input, _ := json.Marshal(kbasescenter.Input{Name: "Docs", SourcePath: t.TempDir()})
	raw := request("POST", "/api/admin/kbases", string(input), 200)
	var d kbasescenter.Definition
	if err = json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	request("GET", "/api/admin/kbases", "", 200)
	request("PUT", "/api/admin/kbases/"+d.ID, `{"name":"Renamed","description":"Reference"}`, 200)
	request("GET", "/api/admin/kbases/"+d.ID, "", 200)
	request("GET", "/api/admin/kbases/"+d.ID+"/search", "", 405)
	request("POST", "/api/admin/kbases/"+d.ID+"/search", "broken", 400)
	request("GET", "/api/admin/kbases/missing", "", 404)
	request("DELETE", "/api/admin/kbases/"+d.ID, "", 200)
	request("GET", "/api/admin/kbases/"+d.ID, "", 404)
	input, _ = json.Marshal(kbasescenter.Input{Name: "Combined", Collections: []kbasescenter.Collection{{Name: "docs", SourcePath: t.TempDir(), Description: "Editable docs", Editable: true}, {Name: "reports", SourcePath: t.TempDir()}}})
	raw = request("POST", "/api/admin/kbases", string(input), 200)
	if err = json.Unmarshal(raw, &d); err != nil || len(d.Collections) != 2 {
		t.Fatalf("multiple collections: %s %v", raw, err)
	}
	if !d.Collections[0].Editable || d.Collections[0].Description != "Editable docs" {
		t.Fatalf("metadata lost on create: %s", raw)
	}
	d.Collections[0].Editable = false
	d.Collections[0].Description = "Read-only docs"
	input, _ = json.Marshal(kbasescenter.Input{Name: "Edited", Collections: []kbasescenter.Collection{d.Collections[0], {Name: "notes", SourcePath: t.TempDir()}}})
	raw = request("PUT", "/api/admin/kbases/"+d.ID, string(input), 200)
	if err = json.Unmarshal(raw, &d); err != nil || len(d.Collections) != 2 || d.Collections[1].Name != "notes" || d.State != "unindexed" {
		t.Fatalf("edited collections: %s %v", raw, err)
	}
	if d.Collections[0].Editable || d.Collections[0].Description != "Read-only docs" {
		t.Fatalf("metadata lost on edit: %s", raw)
	}
	request("PUT", "/api/admin/kbases/"+d.ID, `{"name":"Invalid","collections":[]}`, 400)
	request("POST", "/api/admin/kbases/"+d.ID+"/search", `{"query":"fixture","method":"get"}`, 400)
}

func TestKBasesCenterHTTPDiagnosticsAndDeletion(t *testing.T) {
	root, runtimeRoot := t.TempDir(), t.TempDir()
	service, err := kbasescenter.New(context.Background(), root, runtimeRoot, nil)
	if err != nil {
		t.Fatal(err)
	}
	good, err := service.Create(kbasescenter.Input{Name: "Good", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	bad, err := service.Create(kbasescenter.Input{Name: "Bad", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, bad.ID, "library.yml"), []byte("state: ready\n"), 0600); err != nil {
		t.Fatal(err)
	}
	orphan, err := service.Create(kbasescenter.Input{Name: "Orphan", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, orphan.ID)); err != nil {
		t.Fatal(err)
	}
	server := &Server{deps: Dependencies{KBasesCenter: service}}
	recorder := httptest.NewRecorder()
	server.handleKBasesCenter(recorder, httptest.NewRequest("GET", "/api/admin/kbases", nil))
	if recorder.Code != 200 {
		t.Fatal(recorder.Body.String())
	}
	var response struct {
		Code int
		Data []kbasescenter.Definition
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Data) != 3 {
		t.Fatal(recorder.Body.String())
	}
	for _, d := range response.Data {
		if d.ID == good.ID && d.State != "unindexed" {
			t.Fatal(d)
		}
		if d.ID == bad.ID && (d.State != "error" || d.Error == "") {
			t.Fatal(d)
		}
		if d.ID == orphan.ID && (!d.Orphaned || d.State != "error") {
			t.Fatal(d)
		}
	}
	for _, id := range []string{bad.ID, orphan.ID} {
		recorder = httptest.NewRecorder()
		server.handleKBasesCenter(recorder, httptest.NewRequest("DELETE", "/api/admin/kbases/"+id, nil))
		if recorder.Code != 200 {
			t.Fatal(recorder.Body.String())
		}
		if _, err := os.Stat(filepath.Join(runtimeRoot, "libraries", id)); !os.IsNotExist(err) {
			t.Fatal("runtime retained", err)
		}
	}
}

func TestKBasesTemplateEndpointsReturnNotFound(t *testing.T) {
	root := t.TempDir()
	service, err := kbasescenter.New(context.Background(), root, t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	templateDir := filepath.Join(root, "example")
	if err := os.Mkdir(templateDir, 0700); err != nil {
		t.Fatal(err)
	}
	template := filepath.Join(templateDir, "library.example.yml")
	if err := os.WriteFile(template, []byte("name: \"Example\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	server := &Server{deps: Dependencies{KBasesCenter: service}}
	input, _ := json.Marshal(kbasescenter.Input{Name: "Attempt", SourcePath: t.TempDir()})
	for _, request := range []struct{ method, path string }{
		{"GET", "/api/admin/kbases/example"},
		{"PUT", "/api/admin/kbases/example"},
		{"POST", "/api/admin/kbases/example/refresh"},
		{"DELETE", "/api/admin/kbases/example"},
	} {
		recorder := httptest.NewRecorder()
		server.handleKBasesCenter(recorder, httptest.NewRequest(request.method, request.path, strings.NewReader(string(input))))
		if recorder.Code != 404 {
			t.Fatalf("%s template: %d %s", request.method, recorder.Code, recorder.Body.String())
		}
	}
	if _, err := os.Stat(template); err != nil {
		t.Fatal("template removed", err)
	}
	if _, err := os.Stat(filepath.Join(templateDir, "library.yml")); !os.IsNotExist(err) {
		t.Fatal("template became live")
	}
}
