package connector

// ControlAction describes a model-facing action. Desktop retains its confirmation policy.
type ControlAction struct {
	Tool, Action string
	ReadOnly     bool
}

var controlActions = []ControlAction{
	{"automation_query", "list", true},
	{"automation_query", "get", true},
	{"automation_query", "executions", true},
	{"automation_query", "execution", true},
	{"automation_query", "validate", true},
	{"automation_manage", "create", false},
	{"automation_manage", "update", false},
	{"automation_manage", "setEnabled", false},
	{"automation_manage", "delete", false},
	{"automation_manage", "trigger", false},

	{"catalog_query", "resourceTypes", true},
	{"catalog_query", "list", true},
	{"catalog_query", "get", true},
	{"catalog_query", "defaults", true},
	{"catalog_query", "validate", true},
	{"catalog_manage", "apply", false},
	{"catalog_manage", "delete", false},
	{"chat_query", "current", true},
	{"chat_query", "list", true},
	{"chat_query", "search", true},
	{"chat_query", "read", true},
	{"chat_query", "artifacts", true},
	{"chat_manage", "rename", false},
	{"chat_manage", "setPinned", false},
	{"chat_manage", "archive", false},
	{"chat_manage", "restore", false},
	{"chat_manage", "fork", false},
	{"chat_manage", "export", false},
	{"chat_manage", "delete", false},
	{"platform_inspect", "runtimeStatus", true},
	{"platform_inspect", "securityExplain", true},
	{"desktop_shell", "navigate.toRoute", false},
	{"desktop_shell", "help.openTopic", false},
	{"desktop_shell", "agent.open", false},
	{"desktop_shell", "skill.open", false},
	{"desktop_shell", "assistant.chat", false},
	{"desktop_shell", "display", false},
	{"desktop_shell", "runtime.info", true},
	{"desktop_shell", "runtime.diagnostics", true},
	{"desktop_shell", "general.deviceName", true},
	{"desktop_settings", "theme.get", true},
	{"desktop_settings", "theme.set", false},
	{"desktop_settings", "locale.get", true},
	{"desktop_settings", "locale.set", false},
	{"desktop_settings", "skin.get", true},
	{"desktop_settings", "skin.list", true},
	{"desktop_settings", "skin.import", false},
	{"desktop_settings", "skin.set", false},
	{"desktop_settings", "skin.remove", false},
	{"desktop_settings", "pet.state", true},
	{"desktop_settings", "pet.list", true},
	{"desktop_settings", "pet.show", false},
	{"desktop_settings", "pet.hide", false},
	{"desktop_settings", "pet.set", false},
	{"desktop_settings", "copilot.getPagePreferences", true},
	{"desktop_settings", "copilot.setPagePreference", false},
	{"desktop_site", "site.list", true},
	{"desktop_site", "website.list", true},
	{"desktop_site", "website.add", false},
	{"desktop_site", "website.update", false},
	{"desktop_site", "website.remove", false},
	{"desktop_site", "website.open", false},
	{"desktop_webapp", "webapp.getStatus", true},
	{"desktop_webapp", "webapp.checkRuntime", true},
	{"desktop_webapp", "webapp.getPublishStatus", true},
	{"desktop_webapp", "webapp.start", false},
	{"desktop_webapp", "webapp.stop", false},
	{"desktop_webapp", "webapp.restart", false},
	{"desktop_webapp", "webapp.open", false},
	{"desktop_webapp", "webapp.updatePreferences", false},
	{"desktop_webapp", "webapp.package.init", false},
	{"desktop_webapp", "webapp.package.validate", true},
	{"desktop_webapp", "webapp.package.build", false},
	{"desktop_webapp", "webapp.install", false},
	{"desktop_webapp", "webapp.uninstall", false},
	{"desktop_webapp", "webapp.publish", false},
	{"desktop_webapp", "webapp.unpublish", false},
	{"desktop_webapp", "web.exportArtifact", false},
	{"desktop_service", "controlCenter.listServices", true},
	{"desktop_service", "controlCenter.getServiceStatus", true},
	{"desktop_service", "controlCenter.getServiceDetail", true},
	{"desktop_service", "controlCenter.getServiceLogsMeta", true},
	{"desktop_service", "controlCenter.readServiceLog", true},
	{"desktop_service", "controlCenter.openService", false},
	{"desktop_service", "controlCenter.openLogViewer", false},
	{"desktop_service", "controlCenter.installService", false},
	{"desktop_service", "controlCenter.initializeService", false},
	{"desktop_service", "controlCenter.startService", false},
	{"desktop_service", "controlCenter.stopService", false},
	{"desktop_service", "controlCenter.restartService", false},
	{"desktop_market", "market.getSettings", true},
	{"desktop_market", "market.validateSettings", true},
	{"desktop_market", "market.previewSettingsPatch", true},
	{"desktop_market", "market.applySettingsPatch", false},
	{"desktop_market", "market.listItems", true},
	{"desktop_market", "market.getItemDetail", true},
	{"desktop_market", "market.refresh", false},
	{"desktop_market", "market.installItem", false},
	{"desktop_market", "market.updateItem", false},
	{"desktop_market", "market.uninstallItem", false},
	{"desktop_market", "market.openItem", false},
	{"desktop_market", "market.importSkill", false},
	{"desktop_market", "market.importSandboxImage", false},
	{"desktop_market", "market.exportSandboxImage", false},
	{"desktop_market", "market.deleteSandboxImage", false},
	{"desktop_kanban", "kanban.listIssues", true},
	{"desktop_kanban", "kanban.getIssue", true},
	{"desktop_kanban", "kanban.createIssue", false},
	{"desktop_kanban", "kanban.updateIssue", false},
	{"desktop_kanban", "kanban.deleteIssue", false},
	{"desktop_kanban", "kanban.moveIssue", false},
}

func ControlActions() []ControlAction { return append([]ControlAction(nil), controlActions...) }
func LookupControlAction(tool, action string) (ControlAction, bool) {
	for _, a := range controlActions {
		if a.Tool == tool && a.Action == action {
			return a, true
		}
	}
	return ControlAction{}, false
}
func ControlActionOwner(action string) string {
	for _, a := range controlActions {
		if a.Action == action {
			return a.Tool
		}
	}
	return ""
}
func IsPlatformRootTool(tool string) bool {
	switch tool {
	case "automation_query", "automation_manage", "catalog_query", "catalog_manage", "chat_query", "chat_manage":
		return true
	}
	return false
}
