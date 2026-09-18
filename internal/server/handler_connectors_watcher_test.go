package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"agent-platform/internal/connector"
	"agent-platform/internal/reload"
)

func TestConnectorMutationsWithActiveWatcher(t *testing.T) {
	f := setupAdminRegistriesFixture(t)
	root := f.server.connectorSources().ExternalRoot
	writeMCPConnectorForTest(t, root, "watched-mcp")
	nested := filepath.Join(root, "watched-mcp", "assets", "nested", "old.txt")
	if err := os.MkdirAll(filepath.Dir(nested), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nested, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reloader := f.catalogReloader.(*reload.RuntimeCatalogReloader)
	reload.StartBackgroundReloaders(ctx, f.cfg, reloader)
	upload := func(id string, overwrite bool) {
		t.Helper()
		archive := serverSkillImportZIP(t, map[string]string{
			"connector.json":        fmt.Sprintf(`{"id":%q,"name":"Watched MCP","version":"2.0.0","type":"mcp","auth_mode":"none"}`, id),
			"mcp.json":              `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://example.test/mcp"}}}`,
			"assets/nested/new.txt": "new",
		})
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		part, err := form.CreateFormFile("file", "connector.zip")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(archive); err != nil {
			t.Fatal(err)
		}
		if overwrite {
			if err := form.WriteField("overwrite", "true"); err != nil {
				t.Fatal(err)
			}
		}
		if err := form.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/api/admin/connectors/import", &body)
		request.Header.Set("Content-Type", form.FormDataContentType())
		result := httptest.NewRecorder()
		f.server.ServeHTTP(result, request)
		if result.Code != http.StatusOK {
			t.Fatalf("import: %d %s", result.Code, result.Body.String())
		}
	}
	upload("watched-mcp", true)
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatalf("old payload remains: %v", err)
	}
	upload("fresh-mcp", false)
	previous, err := connector.ReadFile(root, "watched-mcp", "mcp.json")
	if err != nil {
		t.Fatal(err)
	}
	content := `{"mcpServers":{"main":{"type":"streamableHttp","url":"https://updated.example.test/mcp"}}}`
	body, err := json.Marshal(map[string]any{"id": "watched-mcp", "file": "mcp.json", "content": content, "baseSha256": previous.SHA256})
	if err != nil {
		t.Fatal(err)
	}
	result := httptest.NewRecorder()
	f.server.ServeHTTP(result, httptest.NewRequest(http.MethodPut, "/api/admin/connectors/detail", bytes.NewReader(body)))
	if result.Code != http.StatusOK {
		t.Fatalf("edit: %d %s", result.Code, result.Body.String())
	}
	for _, id := range []string{"watched-mcp", "fresh-mcp"} {
		result := deleteConnectorRequest(f.server, id)
		if result.Code != http.StatusOK {
			t.Fatalf("delete: %d %s", result.Code, result.Body.String())
		}
		if _, err := os.Stat(filepath.Join(root, id)); !os.IsNotExist(err) {
			t.Fatalf("deleted connector remains: %v", err)
		}
	}
	// Confirm suspension did not permanently disable resource monitoring.
	probe := filepath.Join(f.cfg.Paths.SkillsCenterDir, "mock-skill")
	if err := os.MkdirAll(probe, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(probe, "SKILL.md"), []byte("---\nname: mock-skill\ndescription: Watcher resumed\n---\n\nProbe.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	awaitWatchedSkillDescription(t, f.registry, "mock-skill", "Watcher resumed")
}
