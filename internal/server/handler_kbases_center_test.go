package server

import (
	"agent-platform/internal/kbasescenter"
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestKBasesCenterHTTP(t *testing.T) {
	service, err := kbasescenter.New(context.Background(), t.TempDir(), nil)
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
}
