package tools

import (
	"fmt"
	"regexp"
)

// Exact runtime admission policy, deliberately separate from the model-facing
// tool schema. Follow Desktop's public registry excluding WebApp-page-only
// actions and the WorkPanel/webpage actions owned by builtin.web-control;
// TestDesktopActionContractMatchesDesktopSource verifies that boundary.
var desktopActionNames = [...]string{
	"desktop.navigate.toRoute",
	"desktop.assistant.chat",
	"desktop.general.deviceName",
	"desktop.runtime.info",
	"desktop.runtime.diagnostics",
	"desktop.theme.get",
	"desktop.theme.set",
	"desktop.skin.get",
	"desktop.skin.list",
	"desktop.skin.import",
	"desktop.skin.set",
	"desktop.skin.remove",
	"desktop.locale.get",
	"desktop.locale.set",
	"desktop.display",
	"desktop.copilot.getPagePreferences",
	"desktop.copilot.setPagePreference",
	"desktop.web.exportArtifact",
	"desktop.site.list",
	"desktop.website.list",
	"desktop.website.add",
	"desktop.website.update",
	"desktop.website.remove",
	"desktop.website.open",
	"desktop.webapp.getStatus",
	"desktop.webapp.checkRuntime",
	"desktop.webapp.start",
	"desktop.webapp.stop",
	"desktop.webapp.restart",
	"desktop.webapp.open",
	"desktop.webapp.updatePreferences",
	"desktop.webapp.package.init",
	"desktop.webapp.package.validate",
	"desktop.webapp.package.build",
	"desktop.webapp.install",
	"desktop.webapp.uninstall",
	"desktop.webapp.getPublishStatus",
	"desktop.webapp.publish",
	"desktop.webapp.unpublish",
	"desktop.controlCenter.listServices",
	"desktop.controlCenter.openService",
	"desktop.controlCenter.getServiceStatus",
	"desktop.controlCenter.getServiceDetail",
	"desktop.controlCenter.getServiceLogsMeta",
	"desktop.controlCenter.readServiceLog",
	"desktop.controlCenter.openLogViewer",
	"desktop.controlCenter.installService",
	"desktop.controlCenter.initializeService",
	"desktop.controlCenter.startService",
	"desktop.controlCenter.stopService",
	"desktop.controlCenter.restartService",
	"desktop.market.getSettings",
	"desktop.market.validateSettings",
	"desktop.market.previewSettingsPatch",
	"desktop.market.applySettingsPatch",
	"desktop.market.listItems",
	"desktop.market.refresh",
	"desktop.market.getItemDetail",
	"desktop.market.installItem",
	"desktop.market.updateItem",
	"desktop.market.uninstallItem",
	"desktop.market.openItem",
	"desktop.market.importSkill",
	"desktop.market.importSandboxImage",
	"desktop.market.exportSandboxImage",
	"desktop.market.deleteSandboxImage",
	"desktop.help.openTopic",
	"desktop.agent.open",
	"desktop.agent.update",
	"desktop.skill.open",
	"desktop.skill.update",
	"desktop.kanban.listIssues",
	"desktop.kanban.getIssue",
	"desktop.kanban.createIssue",
	"desktop.kanban.updateIssue",
	"desktop.kanban.deleteIssue",
	"desktop.kanban.moveIssue",
	"desktop.pet.state",
	"desktop.pet.show",
	"desktop.pet.hide",
	"desktop.pet.list",
	"desktop.pet.set",
}

// Reverse actions the web-control tools may send. They are never accepted from
// the model through desktop_action.
var webControlActionNames = [...]string{
	"desktop.workpanel.getState",
	"desktop.workpanel.openWeb",
	"desktop.workpanel.openLocalFile",
	"desktop.workpanel.refreshWeb",
	"desktop.workpanel.closeTab",
	"desktop.workpanel.closeWorkpanel",
	"desktop.web.interactElement",
}

var desktopActionNamePattern = regexp.MustCompile(`^desktop(?:\.[A-Za-z][A-Za-z0-9]*)+$`)

func loadDesktopActionAllowlist() (map[string]bool, error) {
	return buildDesktopActionAllowlist(desktopActionNames[:])
}

func buildDesktopActionAllowlist(names []string) (map[string]bool, error) {
	if len(names) == 0 {
		return nil, fmt.Errorf("desktop_action runtime allowlist is empty")
	}
	allowed := make(map[string]bool, len(names))
	for _, name := range names {
		if !desktopActionNamePattern.MatchString(name) {
			return nil, fmt.Errorf("desktop_action runtime allowlist contains invalid exact name %q", name)
		}
		if allowed[name] {
			return nil, fmt.Errorf("desktop_action runtime allowlist contains duplicate name %q", name)
		}
		allowed[name] = true
	}
	return allowed, nil
}
