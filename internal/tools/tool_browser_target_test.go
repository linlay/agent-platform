package tools

import (
	"context"
	"reflect"
	"strings"
	"testing"

	. "agent-platform/internal/contracts"
	"agent-platform/internal/resources"
)

func TestSurfaceBrowserUsesCanonicalRunAndOnlyTaskTarget(t *testing.T) {
	for _, browser := range []string{"chrome", "workpanel"} {
		t.Run(browser, func(t *testing.T) {
			executor, execCtx, invoker := webControlTestRuntime(t, nil)
			result, err := executor.Invoke(context.Background(), "surface_browser", map[string]any{"browser": browser}, execCtx)
			if err != nil || result.ExitCode != 0 || len(invoker.requests) != 1 {
				t.Fatalf("result=%#v error=%v requests=%#v", result, err, invoker.requests)
			}
			request := invoker.requests[0]
			if request.Type != desktopCDPRequestType || request.Payload["method"] != "Browser.select" || request.Payload["surfaceId"] != nil {
				t.Fatalf("unexpected selection wire: %#v", request)
			}
			if !reflect.DeepEqual(request.Payload["params"], map[string]any{"browser": browser}) || !reflect.DeepEqual(request.Payload["source"], map[string]any{
				"runId": execCtx.Session.RunID, "chatId": execCtx.Session.ChatID, "agentKey": execCtx.Session.AgentKey,
			}) {
				t.Fatalf("selection changed trusted source or exposed connection settings: %#v", request)
			}
		})
	}
}

func TestSurfaceBrowserRejectsForgedAuthorityAndUnknownTargetBeforeDispatch(t *testing.T) {
	executor, execCtx, invoker := webControlTestRuntime(t, nil)
	for _, args := range []map[string]any{
		{}, {"browser": "auto"}, {"browser": true}, {"browser": " chrome "},
		{"browser": "chrome", "extensionToken": "private"}, {"browser": "chrome", "surfaceId": "copied"},
		{"browser": "workpanel", "source": map[string]any{"chatId": "other"}}, {"browser": "workpanel", "scope": "main"},
	} {
		result, err := executor.Invoke(context.Background(), "surface_browser", args, execCtx)
		if err != nil || result.Error != "invalid_args" {
			t.Fatalf("args=%#v result=%#v error=%v", args, result, err)
		}
	}
	if len(invoker.requests) != 0 {
		t.Fatalf("invalid selection was dispatched: %#v", invoker.requests)
	}
}

func TestSurfaceBrowserPreservesHostScopeAndStartedTaskRejectionsWithoutFallback(t *testing.T) {
	for _, code := range []string{"browser_scope_locked", "browser_target_locked", "browser_run_unavailable", "browser_selection_required"} {
		t.Run(code, func(t *testing.T) {
			message := "Continue in this task's authorized environment; do not replay earlier writes."
			executor, execCtx, invoker := webControlTestRuntime(t, func(request ClientRequest) map[string]any {
				return map[string]any{"ok": false, "error": map[string]any{"code": code, "message": message}}
			})
			result, err := executor.Invoke(context.Background(), "surface_browser", map[string]any{"browser": "chrome"}, execCtx)
			if err != nil || result.ExitCode == 0 || webControlHostErrorCode(result) != code || !strings.Contains(result.Output, message) || len(invoker.requests) != 1 {
				t.Fatalf("result=%#v error=%v requests=%#v", result, err, invoker.requests)
			}
			if code != "browser_run_unavailable" && result.Error != code {
				t.Fatalf("browser recovery code was hidden: %#v", result)
			}
		})
	}
}

func TestBrowserIntentSkillRequiresClarificationAndRetainsApplicationScope(t *testing.T) {
	data, err := resources.ConnectorFS.ReadFile("connectors/builtin.web-control/skills/web-control/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	manual := string(data)
	for _, instruction := range []string{
		"before calling any webpage or AWCP tool", "use `ask_user_question` once", "Do not ask the user to toggle a browser control",
		"WorkPanel needs no Chrome extension or connection approval", "Website/WebApp Copilot already has a captured application grant",
		"Do not silently use the saved default", "browser_target_locked", "never replay it in the other environment",
		"browser_selection_required", "A URL alone does not choose a browser",
	} {
		if !strings.Contains(manual, instruction) {
			t.Errorf("missing browser intent contract: %q", instruction)
		}
	}
}

func TestBrowserSelectionFailureManualsDoNotAllowOtherBrowserFallback(t *testing.T) {
	for _, file := range []string{"SKILL.md", "references/surface.md", "references/troubleshooting.md"} {
		data, err := resources.ConnectorFS.ReadFile("connectors/builtin.web-control/skills/web-control/" + file)
		if err != nil {
			t.Fatal(err)
		}
		manual := string(data)
		for _, required := range []string{"stop and report the failure", "Never ignore a failed selection and continue in another browser", "browser_target_locked", "new Run"} {
			if !strings.Contains(strings.ToLower(manual), strings.ToLower(required)) {
				t.Errorf("%s is missing failed-selection guidance: %q", file, required)
			}
		}
		for _, misleading := range []string{"Continue in that browser", "Continue in its browser", "keep the existing browser", "keep the current provider"} {
			if strings.Contains(manual, misleading) {
				t.Errorf("%s still permits ignoring a failed target selection: %q", file, misleading)
			}
		}
	}
}
