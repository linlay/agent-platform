# Public `desktop_action` Catalog

For `desktop.*`, the runtime `/actions` endpoint and Desktop source `src/shared/desktop-actions.ts` are authoritative. Platform maintains a separate exact runtime allowlist; the model-facing tool schema describes domains without enumerating actions. Always pass a concrete action name from this catalog, never a wildcard. The 11 WebApp-page-only actions (`desktop.assistant.image`, `desktop.assistant.image.cancel`, `desktop.capabilities.list`, and eight `desktop.native.*` actions) are excluded. A public Desktop name alone does not imply Agent eligibility.

Do not call implementation-only bridge branches or WebClient actions that are not listed here.

| Action | Kind | Category |
| --- | --- | --- |
| `desktop.navigate.toRoute` | execute | navigation |
| `desktop.assistant.chat` | execute | assistant |
| `desktop.general.deviceName` | read | general |
| `desktop.runtime.info` | read | runtime |
| `desktop.runtime.diagnostics` | read | runtime |
| `desktop.theme.get` | read | theme |
| `desktop.theme.set` | execute | theme |
| `desktop.skin.get` | read | skin |
| `desktop.skin.list` | read | skin |
| `desktop.skin.import` | execute | skin |
| `desktop.skin.set` | execute | skin |
| `desktop.skin.remove` | execute | skin |
| `desktop.locale.get` | read | locale |
| `desktop.locale.set` | execute | locale |
| `desktop.display` | execute | display |
| `desktop.copilot.getPagePreferences` | read | copilot |
| `desktop.copilot.setPagePreference` | execute | copilot |
| `desktop.web.listSurfaces` | read | web |
| `desktop.web.getSurfaceState` | read | web |
| `desktop.web.interactElement` | execute | web |
| `desktop.web.executeScript` | execute | web |
| `desktop.web.exportArtifact` | execute | web |
| `desktop.web.activateSurface` | execute | web |
| `desktop.web.navigate` | execute | web |
| `desktop.web.reload` | execute | web |
| `desktop.web.refreshSurface` | execute | web |
| `desktop.web.goBack` | execute | web |
| `desktop.web.openTab` | execute | web |
| `desktop.web.closeTab` | execute | web |
| `desktop.web.switchTab` | execute | web |
| `desktop.workpanel.getState` | read | workpanel |
| `desktop.workpanel.openTab` | execute | workpanel |
| `desktop.workpanel.openWeb` | execute | workpanel |
| `desktop.workpanel.openLocalFile` | execute | workpanel |
| `desktop.workpanel.refreshWeb` | execute | workpanel |
| `desktop.workpanel.activateTab` | execute | workpanel |
| `desktop.workpanel.closeTab` | execute | workpanel |
| `desktop.workpanel.closeWorkpanel` | execute | workpanel |
| `desktop.site.list` | read | web |
| `desktop.website.list` | read | web |
| `desktop.website.add` | execute | web |
| `desktop.website.update` | execute | web |
| `desktop.website.remove` | execute | web |
| `desktop.website.open` | execute | web |
| `desktop.webapp.getStatus` | read | web |
| `desktop.webapp.checkRuntime` | validate | web |
| `desktop.webapp.start` | execute | web |
| `desktop.webapp.stop` | execute | web |
| `desktop.webapp.restart` | execute | web |
| `desktop.webapp.open` | execute | web |
| `desktop.webapp.updatePreferences` | execute | web |
| `desktop.webapp.package.init` | execute | web |
| `desktop.webapp.package.validate` | validate | web |
| `desktop.webapp.package.build` | execute | web |
| `desktop.webapp.install` | execute | web |
| `desktop.webapp.uninstall` | execute | web |
| `desktop.webapp.getPublishStatus` | read | web |
| `desktop.webapp.publish` | execute | web |
| `desktop.webapp.unpublish` | execute | web |
| `desktop.controlCenter.listServices` | read | controlCenter |
| `desktop.controlCenter.openService` | execute | controlCenter |
| `desktop.controlCenter.getServiceStatus` | read | controlCenter |
| `desktop.controlCenter.getServiceDetail` | read | controlCenter |
| `desktop.controlCenter.getServiceLogsMeta` | read | controlCenter |
| `desktop.controlCenter.readServiceLog` | read | controlCenter |
| `desktop.controlCenter.openLogViewer` | execute | controlCenter |
| `desktop.controlCenter.installService` | execute | controlCenter |
| `desktop.controlCenter.initializeService` | execute | controlCenter |
| `desktop.controlCenter.startService` | execute | controlCenter |
| `desktop.controlCenter.stopService` | execute | controlCenter |
| `desktop.controlCenter.restartService` | execute | controlCenter |
| `desktop.market.getSettings` | read | market |
| `desktop.market.validateSettings` | validate | market |
| `desktop.market.previewSettingsPatch` | preview | market |
| `desktop.market.applySettingsPatch` | apply | market |
| `desktop.market.listItems` | read | market |
| `desktop.market.refresh` | execute | market |
| `desktop.market.getItemDetail` | read | market |
| `desktop.market.installItem` | execute | market |
| `desktop.market.updateItem` | execute | market |
| `desktop.market.uninstallItem` | execute | market |
| `desktop.market.openItem` | execute | market |
| `desktop.market.importSkill` | execute | market |
| `desktop.market.importSandboxImage` | execute | market |
| `desktop.market.exportSandboxImage` | execute | market |
| `desktop.market.deleteSandboxImage` | execute | market |
| `desktop.help.openTopic` | execute | help |
| `desktop.agent.open` | execute | agent |
| `desktop.agent.update` | execute | agent |
| `desktop.skill.open` | execute | skill |
| `desktop.skill.update` | execute | skill |
| `desktop.kanban.listIssues` | read | kanban |
| `desktop.kanban.getIssue` | read | kanban |
| `desktop.kanban.createIssue` | execute | kanban |
| `desktop.kanban.updateIssue` | execute | kanban |
| `desktop.kanban.deleteIssue` | execute | kanban |
| `desktop.kanban.moveIssue` | execute | kanban |
| `desktop.pet.state` | read | pet |
| `desktop.pet.show` | execute | pet |
| `desktop.pet.hide` | execute | pet |
| `desktop.pet.list` | read | pet |
| `desktop.pet.set` | execute | pet |

## Display Contract

- `desktop.display` shows one transient effect and requires `{ kind: "effect", effect }`, with optional `durationMs`.
- Supported effects are `fireworks`, `snowfall`, and `nationalDay`. The duration defaults to 8000 ms and must be an integer from 1000 through 30000.
- The action is available in Desktop runtime and standalone WebClient. Read `references/display.md` for its exact contract and failure behavior.

## Web Tab Lifecycle Contracts


## WorkPanel Lifecycle Contracts

- WorkPanel actions bind to the current run's trusted Chat and never accept caller-selected ownership fields.
- `desktop.workpanel.openWeb` opens or activates a deterministic HTTP(S) WebView item, including a Desktop-host-visible loopback service. It still rejects `file://`; Desktop does not synthesize a temporary HTTP server for a file.
- `desktop.workpanel.openLocalFile` takes `{path,title?}` and is available only to an ordinary Agent Platform Run in Desktop runtime. The path must be relative to that Agent's authoritative Workspace; the result contains only `{workspace}`.
- `desktop.workpanel.refreshWeb` only reloads an exact already-open normalized URL.
- `desktop.workpanel.activateTab` and `desktop.workpanel.closeTab` take `{tabId}`, where `tabId` is `state.items[].itemId` from `desktop.workpanel.getState`.
- `desktop.workpanel.closeWorkpanel` takes no arguments and refuses to discard protected non-overview entries.
- Read `references/workpanel.md` before calling any of these actions.

## Deprecated Or Non-Public Names

- Do not use `desktop.setting.getState`, `desktop.setting.validatePatch`, `desktop.setting.previewPatch`, or `desktop.setting.applyPatch`; Desktop exposes dedicated domain actions instead.
- Do not use `desktop.web.getActiveSurface`; use `desktop.web.getSurfaceState` with an exact `surfaceId`. The old action has no compatibility alias.
- Do not use `desktop.page.*`; page-control branches are internal, not public action enum values.
- Do not use old namespaces such as `desktop.settings.*`, `desktop.embeddedWeb.*`, `desktop.webs.*`, `desktop.websites.*`, `desktop.staticServer.*`, `desktop.tunnelHub.*`, `desktop.agents.*`, or `desktop.automations.*`.
- Do not use removed names `desktop.web.getPageContext`, `desktop.web.readPageData`, or `desktop.web.extractStructured`; prefer `desktop-cdp` for webpage content, DOM inspection, screenshots, arbitrary scripts, CDP protocol calls, and page-level automation beyond navigation/tab control.
- Website/WebApp names are flattened. Do not use `desktop.web.website.*`, `desktop.web.websites.*`, or `desktop.websites.*`; use `desktop.website.*`.
- Do not use `desktop.web.webapp.*`, `desktop.web.webapps.*`, or old lifecycle names `desktop.webapp.installAndOpen`, `desktop.webapp.checkPrerequisites`, `desktop.webapp.getPublishInfo`, and `desktop.webapp.selectDirectory`; use the listed `desktop.webapp.*` actions. No compatibility aliases exist.
- Do not call `desktop.market.buildSandboxImage` through `desktop_action` unless a future runtime `/actions` catalog lists it.
- Do not use legacy Desktop pet names such as `desktop.pet.getState`, `desktop.pet.getSettings`, `desktop.pet.setEnabled`, `desktop.pet.listAppearances`, or `desktop.pet.setAppearance`.

## Contract Updates

- Read `references/webapp.md` for `desktop.webapp.package.init/validate/build` and Workspace-relative installation. Old `desktop.webapp.manifest.init`, `desktop.webapp.manifest.validate`, and `desktop.webapp.init` are removed.
- Read `references/agent-skill.md` for Agent/Skill open and update, `references/runtime-assistant.md` for runtime information and assistant chat, and `references/web-surfaces.md` for the supported current-page actions and export.
- If Desktop declares an eligible action but Platform returns `unknown_action`, update/rebuild/restart Platform with the matching embedded action schema. Do not retry obsolete aliases or use HTTP as a fallback.

All webpage actions select one exact `surfaceId`, including WorkPanel network pages. `refreshSurface` reloads that one page; `closeTab` no longer accepts a separate tab selector. See [web surfaces](web-surfaces.md).
