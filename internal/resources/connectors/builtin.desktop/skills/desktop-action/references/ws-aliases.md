# Desktop WS Public Action Names

When using Desktop WS `action.call`, these public short names map or normalize to canonical Desktop actions. Canonical `desktop.*` action names are still the safest choice when available.

```text
navigation.toRoute -> desktop.navigate.toRoute
general.deviceName -> desktop.general.deviceName
theme.get -> desktop.theme.get
theme.set -> desktop.theme.set
locale.get -> desktop.locale.get
locale.set -> desktop.locale.set
display -> desktop.display
copilot.getPagePreferences -> desktop.copilot.getPagePreferences
copilot.setPagePreference -> desktop.copilot.setPagePreference
service.list -> desktop.controlCenter.listServices
service.get -> desktop.controlCenter.getServiceDetail
service.status -> desktop.controlCenter.getServiceStatus
service.logs.meta -> desktop.controlCenter.getServiceLogsMeta
service.logs.read -> desktop.controlCenter.readServiceLog
service.start -> desktop.controlCenter.startService
service.stop -> desktop.controlCenter.stopService
service.restart -> desktop.controlCenter.restartService
market.settings -> desktop.market.getSettings
market.list -> desktop.market.listItems
market.refresh -> desktop.market.refresh
market.get -> desktop.market.getItemDetail
market.install -> desktop.market.installItem
market.update -> desktop.market.updateItem
market.uninstall -> desktop.market.uninstallItem
help.open -> desktop.help.openTopic
kanban.issue.list -> desktop.kanban.listIssues
kanban.issue.get -> desktop.kanban.getIssue
kanban.issue.create -> desktop.kanban.createIssue
kanban.issue.update -> desktop.kanban.updateIssue
kanban.issue.delete -> desktop.kanban.deleteIssue
kanban.issue.move -> desktop.kanban.moveIssue
site.list -> desktop.site.list
web.listSurfaces -> desktop.web.listSurfaces
web.getSurfaceState -> desktop.web.getSurfaceState
web.activateSurface -> desktop.web.activateSurface
web.navigate -> desktop.web.navigate
web.reload -> desktop.web.reload
web.refreshSurface -> desktop.web.refreshSurface
web.goBack -> desktop.web.goBack
web.openTab -> desktop.web.openTab
web.closeTab -> desktop.web.closeTab
web.switchTab -> desktop.web.switchTab
website.list -> desktop.website.list
website.add -> desktop.website.add
website.update -> desktop.website.update
website.remove -> desktop.website.remove
webapp.getStatus -> desktop.webapp.getStatus
webapp.checkRuntime -> desktop.webapp.checkRuntime
webapp.start -> desktop.webapp.start
webapp.stop -> desktop.webapp.stop
webapp.restart -> desktop.webapp.restart
webapp.open -> desktop.webapp.open
webapp.install -> desktop.webapp.install
webapp.uninstall -> desktop.webapp.uninstall
webapp.getPublishStatus -> desktop.webapp.getPublishStatus
webapp.publish -> desktop.webapp.publish
webapp.unpublish -> desktop.webapp.unpublish
```

WorkPanel has no public short aliases. Use these canonical names through `action.call`:

```text
desktop.workpanel.getState
desktop.workpanel.openTab
desktop.workpanel.openWeb
desktop.workpanel.refreshWeb
desktop.workpanel.activateTab
desktop.workpanel.closeTab
desktop.workpanel.closeWorkpanel
```

`desktop.workpanel.openLocalFile` is intentionally absent. It is available only through `desktop_action` from an eligible ordinary Agent Platform Run in Desktop runtime; Desktop WS `action.call` returns `forbidden` and must not be used as a fallback.

## Do Not Use

- Aggregated setting names such as `setting.get`, `setting.validatePatch`, `setting.previewPatch`, and `setting.applyPatch`; use the dedicated general, theme, locale, or Copilot action.
- Removed surface-state names `web.getActiveSurface` and `desktop.web.getActiveSurface`; use `web.getSurfaceState` or canonical `desktop.web.getSurfaceState` with an exact `surfaceId`.
- Old web aliases such as `web.entries.list`, `web.website.*`, `web.webapp.*`, `web.list`, `web.surfaces`, `web.active`, `web.activate`, `web.context`, `web.read`, `web.back`, `web.tab.open`, `web.tab.close`, `web.tab.switch`, `web.websites.*`, `web.webapps.*`, and `web.webapps.status`.
- Removed WebApp short names `webapp.installAndOpen`, `webapp.checkPrerequisites`, and `webapp.getPublishInfo`; they return unknown request/action and have no compatibility alias.
- Page-content aliases such as `web.getPageContext`, `web.readPageData`, `web.extractStructured`, `web.interactElement`, and `web.executeScript`; use `desktop-cdp` instead.
- Old Help and Kanban aliases such as `help.openTopic`, `kanban.listIssues`, `kanban.getIssue`, `kanban.createIssue`, `kanban.updateIssue`, `kanban.deleteIssue`, and `kanban.moveIssue`.
- Old pet aliases such as `pet.settings` and `pet.appearances`. No public short pet aliases are exposed in the current WS alias map; use canonical `desktop.pet.*` action names.
- Internal page aliases such as `page.context`, `page.read`, `page.interact`, `page.fillForm`, and `page.submitForm`; they resolve to non-public `desktop.page.*` actions.

## Agent-only Tooling

`desktop.webapp.package.init`, `desktop.webapp.package.validate`, and `desktop.webapp.package.build` require a trusted Platform Run. Desktop WS `action.call` is not a fallback for these actions. The canonical `desktop.web.interactElement` and `desktop.web.executeScript` are public actions; this does not imply that a corresponding short WS alias is supported.
