package documentpreview

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Opt-in only: uploads generated fixtures to the explicitly selected local hub.
// Keep state in a persistent test directory so copies can be inspected and recycled.
func TestLiveDocumentHub(t *testing.T) {
	origin := os.Getenv("DOCUMENT_PREVIEW_LIVE_HUB")
	if origin == "" {
		t.Skip("set DOCUMENT_PREVIEW_LIVE_HUB for local integration")
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Hostname() != "127.0.0.1" {
		t.Fatal("live fixture test only accepts an explicit loopback hub")
	}
	fixtures := os.Getenv("DOCUMENT_PREVIEW_FIXTURES")
	state := os.Getenv("DOCUMENT_PREVIEW_TEST_STATE")
	if fixtures == "" || state == "" {
		t.Fatal("fixture and state directories are required")
	}
	cfg := DefaultConfig()
	cfg.Enabled = true
	cfg.APIBaseURL = origin
	cfg.PublicBaseURL = origin
	s, err := New(cfg, state)
	if err != nil {
		t.Fatal(err)
	}
	results := map[string]Result{}
	for _, name := range []string{"preview-long.docx", "preview-slides.pptx", "preview-sheets.xlsx"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(fixtures, name)
			resolve := func() (Resolved, error) { return Resolved{Path: path, Identity: "integration:" + path}, nil }
			r, err := s.Prepare(context.Background(), "preview-integration", Request{RequestID: "live-" + name}, resolve)
			if err != nil {
				t.Fatal(err)
			}
			repeated, err := s.Prepare(context.Background(), "preview-integration", Request{RequestID: "live-" + name}, resolve)
			if err != nil || repeated.URL != r.URL {
				t.Fatal("repeat did not reuse preview")
			}
			results[name] = r
		})
	}
	if len(results) != 3 {
		t.Fatal("not all formats succeeded")
	}
	data, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(state, "live-results.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("DOCUMENT_PREVIEW_CLEAN_TEST_COPIES") == "1" {
		h, err := newHub(s.config)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range results {
			var saved record
			if err := s.read(r.PreviewID+".json", &saved); err != nil {
				t.Fatal(err)
			}
			if err := h.request(context.Background(), "DELETE", "/api/v1/documents/"+saved.DocumentID+"/share-links/"+saved.LinkID, "", nil, nil); err != nil {
				t.Fatal(err)
			}
			saved.LastAccess = time.Now().Add(-25 * time.Hour).UnixMilli()
			saved.ExpiresAt = time.Now().Add(-time.Hour).UnixMilli()
			if err := s.write(r.PreviewID+".json", saved); err != nil {
				t.Fatal(err)
			}
		}
		if err := s.Cleanup(context.Background()); err != nil {
			t.Fatal(err)
		}

		for _, r := range results {
			if _, err := os.Stat(filepath.Join(s.dir, r.PreviewID+".json")); !os.IsNotExist(err) {
				t.Fatalf("test copy was not recycled: %s (%v)", r.PreviewID, err)
			}
		}
	}
}
