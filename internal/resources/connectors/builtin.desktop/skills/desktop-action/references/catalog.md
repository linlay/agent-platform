# Public `desktop_action` Catalog

For `desktop.*`, the runtime `/actions` endpoint and Desktop source `src/shared/desktop-actions.ts` are authoritative. Platform maintains a separate exact runtime allowlist; the model-facing tool schema delegates action discovery to the mounted skill. Always pass a concrete action name from this catalog, never a wildcard. The 11 WebApp-page-only actions (`desktop.assistant.image`, `desktop.assistant.image.cancel`, `desktop.capabilities.list`, and eight `desktop.native.*` actions) are excluded. A public Desktop name alone does not imply Agent eligibility.

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
| `desktop.web.exportArtifact` | execute | web |
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

## Webpages And WorkPanel

Opening webpages, file previews, the WorkPanel and all webpage content belong to the separate `builtin.web-control` connector (`workpanel_*`, `surface_*` and `awcp_*` tools). `desktop_action` does not open or operate pages. The `desktop.web.*` page actions and `desktop.workpanel.*` actions are not callable through `desktop_action`.

## Deprecated Or Non-Public Names

- Do not use `desktop.setting.getState`, `desktop.setting.validatePatch`, `desktop.setting.previewPatch`, or `desktop.setting.applyPatch`; Desktop exposes dedicated domain actions instead.
- Do not use `desktop.page.*`; page-control branches are internal, not public action enum values.
- Do not use old namespaces such as `desktop.settings.*`, `desktop.embeddedWeb.*`, `desktop.webs.*`, `desktop.websites.*`, `desktop.staticServer.*`, `desktop.tunnelHub.*`, `desktop.agents.*`, or `desktop.automations.*`.
- Website/WebApp names are flattened. Do not use `desktop.web.website.*`, `desktop.web.websites.*`, or `desktop.websites.*`; use `desktop.website.*`.
- Do not use `desktop.web.webapp.*`, `desktop.web.webapps.*`, or old lifecycle names `desktop.webapp.installAndOpen`, `desktop.webapp.checkPrerequisites`, `desktop.webapp.getPublishInfo`, and `desktop.webapp.selectDirectory`; use the listed `desktop.webapp.*` actions. No compatibility aliases exist.
- Do not call `desktop.market.buildSandboxImage` through `desktop_action` unless a future runtime `/actions` catalog lists it.
- Do not use legacy Desktop pet names such as `desktop.pet.getState`, `desktop.pet.getSettings`, `desktop.pet.setEnabled`, `desktop.pet.listAppearances`, or `desktop.pet.setAppearance`.

## Contract Updates

- Read `references/webapp.md` for `desktop.webapp.package.init/validate/build` and Workspace-relative installation. Old `desktop.webapp.manifest.init`, `desktop.webapp.manifest.validate`, and `desktop.webapp.init` are removed.
- Read `references/agent-skill.md` for Agent/Skill open and update and `references/runtime-assistant.md` for runtime information and assistant chat.
- If Desktop declares an eligible action but Platform returns `unknown_action`, update/rebuild/restart Platform with the matching embedded action schema. Do not retry obsolete aliases or use HTTP as a fallback.
