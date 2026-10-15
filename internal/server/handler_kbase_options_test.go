package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"agent-platform/internal/api"
	"agent-platform/internal/catalog"
	"agent-platform/internal/connector"
	"agent-platform/internal/contracts/queryinput"
	"agent-platform/internal/kbases"
	"agent-platform/internal/ws"
)

type kbaseOptionsRegistry struct {
	catalog.Registry
	definition catalog.AgentDefinition
}

func (r kbaseOptionsRegistry) AgentDefinition(key string) (catalog.AgentDefinition, bool) {
	if key == r.definition.Key {
		return r.definition, true
	}
	return r.Registry.AgentDefinition(key)
}

type kbaseDirectoryEngine struct{}

func (kbaseDirectoryEngine) Update(_ context.Context, db string, _ []kbases.Collection) error {
	return os.WriteFile(db, nil, 0600)
}
func (kbaseDirectoryEngine) Read(context.Context, string, string, string, int, ...string) (json.RawMessage, error) {
	return nil, nil
}

func readyDirectoryLibrary(t *testing.T, s *kbases.Service) kbases.Definition {
	t.Helper()
	d, err := s.Create(kbases.Input{Name: "产品说明", Description: "发布规则", Collections: []kbases.Collection{{Name: "docs", Description: "版本文档", SourcePath: t.TempDir()}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Refresh(d.ID); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		d, err = s.Get(d.ID)
		if err == nil && d.State == "ready" {
			return d
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("library did not become ready: %+v %v", d, err)
	return d
}

func TestKBaseOptionsHTTPWSAndQueryAdmission(t *testing.T) {
	f := newTestFixtureWithModelHandlerAndOptions(t, nil, testFixtureOptions{notifications: ws.NewHub()})
	service, err := kbases.New(context.Background(), t.TempDir(), t.TempDir(), kbaseDirectoryEngine{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close(context.Background()) })
	library := readyDirectoryLibrary(t, service)
	unreadable, err := service.Create(kbases.Input{Name: "未索引", SourcePath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	def, ok := f.registry.AgentDefinition("mock-agent")
	if !ok {
		t.Fatal("fixture agent missing")
	}
	def.Connectors = append(def.Connectors, connector.KBaseControlConnectorID)
	def.ConnectorNativeTools = append(def.ConnectorNativeTools, "kbase_list")
	def.Tools = append(def.Tools, "kbase_list") // Selection must not invent a minimum-search-tool policy.
	f.server.deps.Registry = kbaseOptionsRegistry{Registry: f.registry, definition: def}
	f.server.deps.KBases = service
	bindTestRuntime(f.server)
	rec := httptest.NewRecorder()
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/kbases?agentKey=mock-agent", nil))
	var body api.ApiResponse[api.KBaseOptionsResponse]
	if err = json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 200 || body.Code != 0 || !body.Data.Capability.Available || body.Data.BoundLibraryID != "" || len(body.Data.Libraries) != 2 {
		t.Fatalf("directory: %s", rec.Body.String())
	}
	for _, option := range body.Data.Libraries {
		if option.ID == library.ID && (!option.Readable || option.Collections[0].Description != "版本文档") {
			t.Fatalf("ready summary: %+v", option)
		}
		if option.ID == unreadable.ID && (option.Readable || option.UnavailableReason == "") {
			t.Fatalf("unreadable summary: %+v", option)
		}
	}
	// Match whole fields/values: a numeric TempDir basename may occur inside
	// an unrelated generated library ID.
	for _, forbidden := range []string{`"sourcePath":`, `"editable":`, `"baseRevision":`, `"` + filepath.Base(library.Collections[0].SourcePath) + `"`, library.Collections[0].SourcePath} {
		if strings.Contains(rec.Body.String(), forbidden) {
			t.Fatalf("management field leaked: %s", rec.Body.String())
		}
	}
	httpServer := httptest.NewServer(f.server)
	defer httpServer.Close()
	conn := dialTestWebSocket(t, httpServer.URL)
	defer conn.Close()
	readConnectedPush(t, conn)
	sendConnectorUsageRequest(t, conn, "/api/kbases", "kb-directory", map[string]any{"agentKey": "mock-agent"})
	fromWS := waitForWebSocketResponseData[api.KBaseOptionsResponse](t, conn, "kb-directory")
	if !reflect.DeepEqual(fromWS, body.Data) {
		t.Fatalf("HTTP/WS mismatch: %+v / %+v", fromWS, body.Data)
	}
	request := api.QueryRequest{AgentKey: "mock-agent", Message: "使用知识库", References: queryinput.MakeReferences("kbase", []string{" " + library.ID + " ", library.ID, " "})}
	admission, err := f.server.prepareQueryAdmissionRequest(context.Background(), request, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	admission.Release()
	if !reflect.DeepEqual(queryinput.ResourceIDs(admission.Req.References, "kbase"), []string{library.ID}) {
		t.Fatalf("normalization: %+v", queryinput.ResourceIDs(admission.Req.References, "kbase"))
	}
	request.References = queryinput.MakeReferences("kbase", []string{library.ID, unreadable.ID})
	if _, err = f.server.prepareQueryAdmissionRequest(context.Background(), request, false, "", ""); err == nil || !strings.Contains(err.Error(), unreadable.ID) {
		t.Fatalf("partial unreadable selection accepted: %v", err)
	}
	request.References = queryinput.MakeReferences("kbase", []string{library.ID})
	request.Lane = "btw"
	rec = httptest.NewRecorder()
	payload, _ := json.Marshal(request)
	f.server.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/query", strings.NewReader(string(payload))))
	if rec.Code != 400 || !strings.Contains(rec.Body.String(), "BTW does not accept skill, kbase or chat references") {
		t.Fatal("BTW selection accepted")
	}
	policy := def.Interaction()
	policy.Attachment.Kbases = false
	def.InteractionConfig = &policy
	f.server.deps.Registry = kbaseOptionsRegistry{Registry: f.registry, definition: def}
	bindTestRuntime(f.server)
	request.Lane = ""
	request.References = queryinput.MakeReferences("kbase", []string{})
	admission, err = f.server.prepareQueryAdmissionRequest(context.Background(), request, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	admission.Release()
	request.References = queryinput.MakeReferences("kbase", []string{library.ID})
	if _, err = f.server.prepareQueryAdmissionRequest(context.Background(), request, false, "", ""); err == nil {
		t.Fatal("disabled nonempty selection accepted")
	}
	// chat_start is not gated by the entry switch, through admission and the
	// session build alike, while an unreadable library is still refused.
	command := trustedQueryCommand(context.Background(), request)
	command.SkipInteractionPolicy = true
	toolAdmission, err := testQueryService(f.server).PrepareQueryAdmissionRequest(context.Background(), command, false, "", "")
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := testQueryService(f.server).CompleteQueryPreparation(context.Background(), toolAdmission, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := queryinput.ResourceIDs(prepared.Session.ReferencesScope.References(), "kbase"); !reflect.DeepEqual(got, []string{library.ID}) || prepared.Session.InteractionConfig.Attachment.Kbases {
		t.Fatalf("refs=%v frozen=%+v", got, prepared.Session.InteractionConfig)
	}
	command.References = queryinput.MakeReferences("kbase", []string{unreadable.ID})
	if _, err = testQueryService(f.server).PrepareQueryAdmissionRequest(context.Background(), command, false, "", ""); err == nil || !strings.Contains(err.Error(), unreadable.ID) {
		t.Fatalf("unreadable library accepted: %v", err)
	}
}
