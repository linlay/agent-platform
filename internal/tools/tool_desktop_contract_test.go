package tools

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"

	"agent-platform/internal/config"
)

// Desktop rejects these actions outside an authorized local WebApp page.
var desktopWebappPageOnlyActions = []string{
	"desktop.assistant.image",
	"desktop.assistant.image.cancel",
	"desktop.capabilities.list",
	"desktop.native.browser.openExternal",
	"desktop.native.clipboard.writeText",
	"desktop.native.dialog.selectDirectory",
	"desktop.native.dialog.selectFiles",
	"desktop.native.dialog.selectSavePath",
	"desktop.native.microphone.getPermission",
	"desktop.native.microphone.requestAccess",
	"desktop.native.notification.show",
}

// Desktop's WorkPanel and webpage actions belong to builtin.web-control. The
// typed web-control tools either send them (webControlActionNames) or reach
// the same executor through a CDP method, so desktop_action never exposes them.
var desktopActionsOwnedByWebControl = []string{
	"desktop.web.activateSurface",
	"desktop.web.closeTab",
	"desktop.web.executeScript",
	"desktop.web.getSurfaceState",
	"desktop.web.goBack",
	"desktop.web.interactElement",
	"desktop.web.listSurfaces",
	"desktop.web.navigate",
	"desktop.web.openTab",
	"desktop.web.refreshSurface",
	"desktop.web.reload",
	"desktop.web.switchTab",
	"desktop.workpanel.activateTab",
	"desktop.workpanel.closeTab",
	"desktop.workpanel.closeWorkpanel",
	"desktop.workpanel.getState",
	"desktop.workpanel.openLocalFile",
	"desktop.workpanel.openTab",
	"desktop.workpanel.openWeb",
	"desktop.workpanel.refreshWeb",
}

func TestDesktopActionRejectsWebappPageOnlyActions(t *testing.T) {
	invoker := &routingClientRequestInvoker{}
	executor := &RuntimeToolExecutor{
		cfg:           config.Config{RuntimeMode: config.RuntimeModeDesktop},
		clientRequest: invoker,
		clientTargets: emptyRunClientTargetStore{},
	}
	for _, action := range desktopWebappPageOnlyActions {
		t.Run(action, func(t *testing.T) {
			result, err := executor.invokeDesktopAction(context.Background(), map[string]any{"action": action}, desktopActionTestExecutionContext())
			if err != nil || result.Error != "unknown_action" {
				t.Fatalf("page-only action result=%#v err=%v", result, err)
			}
		})
	}
	_, requests := invoker.snapshots()
	if len(requests) != 0 {
		t.Fatalf("page-only actions reached Desktop: %#v", requests)
	}
}

// Local multi-repo development checks Desktop directly. CI can opt in with an
// explicit checkout; a missing explicitly configured checkout must fail.
func TestDesktopActionContractMatchesDesktopSource(t *testing.T) {
	root := os.Getenv("DESKTOP_SOURCE")
	explicit := root != ""
	if !explicit {
		root = filepath.Join("..", "..", "..", "desktop")
	}
	path := filepath.Join(root, "src", "shared", "desktop-actions.ts")
	source, err := os.ReadFile(path)
	if !explicit && os.IsNotExist(err) {
		t.Skip("Desktop checkout absent; set DESKTOP_SOURCE to require the cross-repository contract check")
	}
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)export const DESKTOP_ACTION_DEFINITIONS = \[(.*?)\] as const`).FindSubmatch(source)
	if len(block) != 2 {
		t.Fatal("cannot locate Desktop action definitions; update the contract reader")
	}
	matches := regexp.MustCompile(`\{\s*name:\s*"(desktop\.[\w.]+)"`).FindAllSubmatch(block[1], -1)
	if len(matches) == 0 {
		t.Fatal("Desktop action definitions are empty")
	}
	excluded := map[string]bool{}
	for _, action := range desktopWebappPageOnlyActions {
		excluded[action] = true
	}
	owned := map[string]bool{}
	for _, action := range desktopActionsOwnedByWebControl {
		owned[action] = true
		excluded[action] = true
	}
	for _, action := range webControlActionNames {
		if !owned[action] {
			t.Fatalf("web-control sends %s without owning it", action)
		}
	}
	seen := map[string]bool{}
	var want []string
	for _, match := range matches {
		action := string(match[1])
		if seen[action] {
			t.Fatalf("duplicate Desktop action: %s", action)
		}
		seen[action] = true
		if !excluded[action] {
			want = append(want, action)
		}
	}
	for action := range excluded {
		if !seen[action] {
			t.Fatalf("obsolete exclusion, Desktop no longer defines %s", action)
		}
	}
	policy, err := os.ReadFile(filepath.Join(root, "src", "main", "modules", "desktop-actions", "webapp-native-actions.ts"))
	if err != nil {
		t.Fatal(err)
	}
	pageOnlyBlock := regexp.MustCompile(`(?s)export const WEBAPP_PAGE_ONLY_ACTIONS = new Set\(\[(.*?)\]\)`).FindSubmatch(policy)
	if len(pageOnlyBlock) != 2 {
		t.Fatal("cannot locate Desktop page-only policy; update the contract reader")
	}
	// The image adapter enforces its page-only guard separately in executeAction.
	pageOnly := []string{"desktop.assistant.image", "desktop.assistant.image.cancel"}
	for _, match := range regexp.MustCompile(`"(desktop\.[\w.]+)"`).FindAllSubmatch(pageOnlyBlock[1], -1) {
		pageOnly = append(pageOnly, string(match[1]))
	}
	sort.Strings(pageOnly)
	if !reflect.DeepEqual(pageOnly, desktopWebappPageOnlyActions) {
		t.Fatalf("Desktop page-only policy changed; review Platform exclusions\nDesktop: %v\nPlatform: %v", pageOnly, desktopWebappPageOnlyActions)
	}
	sort.Strings(want)
	got := sortedDesktopActionAllowlist(t)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Platform must follow Desktop actions (excluding WebApp-page-only and web-control actions)\nDesktop: %v\nPlatform: %v", want, got)
	}
}
