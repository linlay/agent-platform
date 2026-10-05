package viewport

import (
	"context"
	"strings"
	"testing"
)

func TestServiceProvidesBuiltinApprovalTemplates(t *testing.T) {
	service := NewService()
	for _, key := range []string{"confirm_dialog", "platform_control_review", "resource_delete_review", "chat_delete_review", "desktop_appearance_review", "desktop_website_review", "desktop_kanban_review", "desktop_webapp_review", "desktop_export_review", "desktop_diagnostics_review", "installation_review"} {
		t.Run(key, func(t *testing.T) {
			payload, err := service.Get(context.Background(), key)
			if err != nil || payload["viewportKey"] != key {
				t.Fatalf("builtin %s: %#v %v", key, payload, err)
			}
			html, _ := payload["html"].(string)
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
	service := NewService()
	for _, key := range []string{"", "..", "../platform_control_review", "nested/template", `nested\template`} {
		if _, err := service.Get(context.Background(), key); err == nil {
			t.Fatalf("accepted invalid key %q", key)
		}
	}
	for _, key := range []string{"leave_form", "expense_form", "procurement_form", "missing"} {
		payload, err := service.Get(context.Background(), key)
		if err != nil || payload["status"] != "not_implemented" {
			t.Fatalf("external template %s: %#v %v", key, payload, err)
		}
	}
}
