---
name: desktop-action
description: "Use this skill when the user wants to read or operate Desktop through the desktop_action function tool, or use Desktop WS action.call: transient display effects, Desktop shell navigation, runtime info/diagnostics, assistant chat, Agent/Skill editing, device/theme/locale/Copilot settings, Desktop skin ZIP import/list/apply/remove, website entries, website apps, control-center services and logs, market items and sandbox images, Help routes, Kanban issues, Desktop pet state/visibility/appearance, or Desktop WS public action names."
metadata:
  version: 0.4.0
---

# desktop-action

Use the `desktop_action` function tool for allowlisted `desktop.*` actions. It is the high-level control plane for Desktop shell state, applications and services.

Opening webpages, file previews, the WorkPanel and all webpage content belong to the separate `builtin.web-control` connector (`workpanel_*`, `surface_*` and `awcp_*` tools). `desktop_action` does not open or operate pages.

## Read Strategy

- Start here for the overall map and safety rules.
- Read only the reference file for the relevant business area.
- Read `references/catalog.md` when choosing an action name, checking action kind/category, or handling `unknown_action`.
- Treat Desktop `src/shared/desktop-actions.ts` as the action-name source and Platform internal runtime allowlist as the callable subset. The tool schema intentionally omits the action catalog; always send an exact action name, never a wildcard. The catalog excludes WebApp-page-only actions. Do not switch transport to work around a stale Platform whitelist.
- Website CRUD uses `desktop.website.*`, WebApp lifecycle uses `desktop.webapp.*`, and the combined catalog uses `desktop.site.list`. A page opened by `desktop.website.open` or `desktop.webapp.open` is operated with the web-control tools when that connector is mounted.
- Use `desktop.display` for the supported transient visual effects in either Desktop runtime or standalone WebClient. Read `references/display.md` before calling it.

## Feature References

| Business area | Read |
| --- | --- |
| Public action list, action kinds, categories | `references/catalog.md` |
| Agent and Skill open/update | `references/agent-skill.md` |
| Runtime info/diagnostics and assistant chat | `references/runtime-assistant.md` |
| Shell navigation | `references/navigation.md` |
| Device name, theme, locale, and Copilot settings | `references/settings.md` |
| Desktop skin state, list, ZIP import, apply and remove | `references/skin.md` |
| Transient fireworks, snowfall, and National Day effects | `references/display.md` |
| Website entries | `references/website.md` |
| Website apps | `references/webapp.md` |
| Control-center services and logs | `references/control-center.md` |
| Market settings and catalog items | `references/market.md` |
| Sandbox image import/export/delete | `references/sandbox-images.md` |
| Help route jumps | `references/help.md` |
| Kanban issues | `references/kanban.md` |
| Desktop pet state/show/hide/list/set | `references/pet.md` |
| Desktop WS `action.call` public names and blocked old aliases | `references/ws-aliases.md` |

## Core Rules

- Use only public action names from `references/catalog.md`; use Desktop `/actions` only for `desktop.*`.
- Put action-specific inputs in `args`, using the exact structure in the relevant reference. Read that reference before the first call; returned objects are not input contracts and different actions may use different nesting.
- Never put transport-owned `source` in `args`; Platform rejects it. If a stable correlation id is needed, use the optional top-level `requestId` tool parameter. Do not construct the Platform-to-client reverse WebSocket frame yourself.
- Prefer read actions for diagnosis.
- Use validate and preview actions before apply actions when the selected business domain documents them.
- For execute/apply actions that mutate state, start/stop services, install/update/uninstall items, edit websites, change settings, change Kanban issues, or control the Desktop pet, rely on the Desktop action confirmation/permission flow and ask the user only when the target is unclear.
- Treat `response.ok: true` with `result.ok: false` as a business failure. Report `result.message` or `result.issues`; never infer success from returned `item` or `items` snapshots.
- `desktop.web.exportArtifact` exports from an authorized WebApp export provider; read `references/webapp.md`. All other webpage work uses the web-control tools.

## Failure Handling

- `unknown_action`: the action is not public or not allowlisted. Check `references/catalog.md` and the Platform runtime allowlist/version; the tool schema is not an action catalog. If an eligible Desktop action is missing from Platform, report the version mismatch and update/rebuild/restart Platform; do not retry removed names.
- `unsupported_action`: the action is reserved by Desktop but not implemented in the current version. Report the limitation and use the closest read or preview action if available.
- `invalid_args` (including `details.clientErrorType: invalid_args`): inspect `details.issues[]` field paths and the recovery reference. Re-read the relevant reference and retry once with corrected input. Stop guessing field names or nesting. When a batch shares an unverified shape, verify one call before issuing the rest.
- `desktop_action_target_unavailable`, `desktop_action_client_disconnected`, or `desktop_action_client_timeout`: the current run has no usable reverse control connection. Report the connection limitation instead of selecting another window.
- `desktop_action_client_rejected`: inspect `details.clientErrorType` and recovery metadata. If the provider reports `invalid_args`, follow the parameter-error recovery below; otherwise report it without changing targets.
- `kanban_unavailable`: Kanban runtime is not initialized.
- `pet_action_unavailable`, `pet_unsupported`, or `pet_appearance_not_found`: report the Desktop pet limitation and avoid retrying without a different target.
- `interactive_file_picker_required`: the action requires an interactive picker and cannot complete from `archivePath` or `targetPath` unless a non-interactive action explicitly supports that argument.
- `display_target_unavailable`: the Desktop Main Window is absent, hidden, or minimized. Report the limitation; do not redirect the effect to another window or client.
- Bridge unavailable: report that the Desktop action bridge is not reachable.
