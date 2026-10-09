package view

import (
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"agent-platform/internal/resources"
)

func TestServiceProvidesBuiltinApprovalTemplates(t *testing.T) {
	for _, key := range []string{"platform_control_review", "resource_delete_review", "chat_delete_review", "desktop_appearance_review", "desktop_website_review", "desktop_kanban_review", "desktop_webapp_review", "desktop_export_review", "desktop_diagnostics_review", "installation_review", "automation_review", "automation_delete_review", "automation_trigger_review"} {
		t.Run(key, func(t *testing.T) {
			payload, err := BuiltinDocument(key)
			if err != nil || payload.View.Key != key {
				t.Fatalf("builtin %s: %#v %v", key, payload, err)
			}
			html := payload.HTML
			if strings.Contains(html, "/* REVIEW_") {
				t.Fatal("unresolved shared review assets")
			}
			if strings.TrimSpace(html) == "" {
				t.Fatal("builtin template is empty")
			}
			if key != "confirm_dialog" && !strings.Contains(html, "awaiting_collect") {
				t.Fatal("review submission protocol missing")
			}
		})
	}
}

func TestServiceRejectsInvalidKeysAndExternalTemplates(t *testing.T) {
	for _, key := range []string{"", "..", "../platform_control_review", "nested/template", `nested\template`} {
		if _, err := BuiltinDocument(key); err == nil {
			t.Fatalf("accepted invalid key %q", key)
		}
	}
	for _, key := range []string{"leave_form", "expense_form", "procurement_form", "missing"} {
		payload, err := BuiltinDocument(key)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("external template %s: %#v %v", key, payload, err)
		}
	}
}

// Enumerate the embedded directory so new templates cannot miss the common bridge.
func TestEveryBuiltinViewHasSizingBridge(t *testing.T) {
	files, err := fs.Glob(resources.ViewFS, "views/*.html")
	if err != nil || len(files) == 0 {
		t.Fatalf("enumerate views: %v", err)
	}
	for _, file := range files {
		key := strings.TrimSuffix(filepath.Base(file), ".html")
		t.Run(key, func(t *testing.T) {
			payload, err := BuiltinDocument(key)
			if err != nil {
				t.Fatal(err)
			}
			html := payload.HTML
			if strings.Count(html, "<script data-view-resize>") != 1 || strings.Count(html, "type: 'awaiting_resize'") != 1 {
				t.Fatal("each builtin must include exactly one shared sizing bridge")
			}
		})
	}
}
