---
name: desktop-action
description: "Use this skill when the user wants to read or operate Desktop through the desktop_action function tool, or use Desktop WS action.call: transient display effects, chat-bound WorkPanel tabs, WebViews, local HTTP services, and workspace files, Desktop shell navigation, runtime info/diagnostics, assistant chat, Agent/Skill editing, device/theme/locale/Copilot settings, Desktop skin ZIP import/list/apply/remove, web surface and tab management including per-page refresh, website entries, website apps, control-center services and logs, market items and sandbox images, Help routes, Kanban issues, Desktop pet state/visibility/appearance, or Desktop WS public action names."
metadata:
  version: 0.3.3
---

# desktop-action

Use the `desktop_action` function tool for allowlisted `desktop.*` actions. It is the high-level control plane for Desktop shell state, services, and the current Chat's WorkPanel.

Prefer `desktop-cdp` for webpage content, DOM inspection, screenshots, arbitrary JavaScript/CDP protocol calls, browser debugging, and page-level automation beyond navigation/tab control.

## Read Strategy

- Start here for the overall map and safety rules.
- Read only the reference file for the relevant business area.
- Read `references/catalog.md` when choosing an action name, checking action kind/category, or handling `unknown_action`.
- Treat Desktop `src/shared/desktop-actions.ts` as the action-name source and Platform internal runtime allowlist as the callable subset. The tool schema describes domain scope only and intentionally omits the full action enum; always send an exact action name, never a wildcard. The catalog excludes WebApp-page-only actions. Do not switch transport to work around a stale Platform whitelist.
- Prefer `desktop-cdp` for page content. Desktop also exposes `desktop.web.interactElement` and `desktop.web.executeScript`; read `references/web-surfaces.md` for their exact current-page inputs. AWCP page actions still use `desktop_cdp`. Website CRUD uses `desktop.website.*`, WebApp lifecycle uses `desktop.webapp.*`, and the combined catalog uses `desktop.site.list`.
- Use `desktop.display` for the supported transient visual effects in either Desktop runtime or standalone WebClient. Read `references/display.md` before calling it.

## Default webpage routing

For ordinary Chat requests such as “open this website/URL”, use `desktop.workpanel.openWeb` by default; no mention of a sidebar is required. Continue in the owning Website/WebApp context when its Copilot already has one. An open WorkPanel webpage returns `surfaceId`, `containerId` and `status`.

A Container hosts webpages; one Surface is one independently addressable webpage. `desktop.web.listSurfaces` discovers authorized live pages, including the current Chat's WorkPanel and authorized background pages. Pass the exact `surfaceId` to `desktop.web.*` or `desktop_cdp`; WorkPanel item IDs and Container IDs are not substitutes. Read [web surfaces](references/web-surfaces.md) for the shared operations. A failed high-level web action does not imply a read-only Website.

For page-content tasks on any authorized network webpage, including ordinary Chat WorkPanel, follow desktop-cdp: read one AWCP.getManual directory first, then a matching section and invoke. Only a host-confirmed missing protocol entry or no matching section permits DOM work. This also applies before desktop.web.interactElement or desktop.web.executeScript; those actions do not bypass manual-first routing. Merely opening a URL, managing tabs or taking a screenshot does not require a manual probe.

## Feature References

| Business area | Read |
| --- | --- |
| Public action list, action kinds, categories | `references/catalog.md` |
| Current chat WorkPanel tabs and WebViews | `references/workpanel.md` |
| Agent and Skill open/update | `references/agent-skill.md` |
| Runtime info/diagnostics and assistant chat | `references/runtime-assistant.md` |
| Shell navigation | `references/navigation.md` |
| Device name, theme, locale, and Copilot settings | `references/settings.md` |
| Desktop skin state, list, ZIP import, apply and remove | `references/skin.md` |
| Transient fireworks, snowfall, and National Day effects | `references/display.md` |
| Web surface and tab management | `references/web-surfaces.md` |
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
- WorkPanel actions always target the current run's trusted Chat. Never send `chatId`, `workspaceId`, `surfaceId`, `agentKey`, `stableKey`, `preload`, or `webPreferences` as top-level keys in their `args`. A contract-valid WebClient `descriptor.context` may carry matching contextual identities but cannot select another Chat.
- Use `desktop.workpanel.openWeb` for a known HTTP(S) URL, including a Desktop-host-visible loopback service. Use `desktop.workpanel.openLocalFile` for a file inside the current Agent's authoritative Workspace. Use `desktop.workpanel.refreshWeb` only for an exact already-open URL and `openTab` only with a complete, contract-valid public descriptor.
- Open only a URL explicitly supplied by the user, returned by a trusted tool, already present in the current conversation/session, or derived as the loopback URL of a service started for the current task. Do not synthesize arbitrary public, LAN/private-network, tracking, authentication, or side-effecting URLs.
- Do not convert a local file into `file://` or start a temporary HTTP server as a fallback. Use `openLocalFile` for Workspace files; use `openWeb` only when an actual host-visible HTTP(S) service exists.
- Prefer read actions for diagnosis.
- Use validate and preview actions before apply actions when the selected business domain documents them.
- For execute/apply actions that mutate state, start/stop services, install/update/uninstall items, edit websites, navigate web surfaces or tabs, change settings, change Kanban issues, or control the Desktop pet, rely on the Desktop action confirmation/permission flow and ask the user only when the target is unclear.
- Treat `response.ok: true` with `result.ok: false` as a business failure. Report `result.message` or `result.issues`; never infer success from returned `item` or `items` snapshots.
- Prefer `desktop-cdp` for DOM inspection, screenshots, CDP and AWCP. The explicit `desktop.web.interactElement`, `desktop.web.executeScript`, and `desktop.web.exportArtifact` actions are supported under their own contracts.

## Failure Handling

- `unknown_action`: the action is not public or not allowlisted. Check `references/catalog.md` and the Platform runtime allowlist/version; the tool schema is not an action catalog. If an eligible Desktop action is missing from Platform, report the version mismatch and update/rebuild/restart Platform; do not retry removed names.
- `unsupported_action`: the action is reserved by Desktop but not implemented in the current version. Report the limitation and use the closest read or preview action if available.
- `invalid_args` (including `details.clientErrorType: invalid_args`): inspect `details.issues[]` field paths and the recovery reference. Re-read the relevant reference and retry once with corrected input. Stop guessing field names or nesting. When a batch shares an unverified shape, verify one call before issuing the rest.
- `desktop_action_target_unavailable`, `desktop_action_client_disconnected`, or `desktop_action_client_timeout`: the current run has no usable reverse control connection. Report the connection limitation instead of selecting another window.
- `desktop_action_client_rejected`: inspect `details.clientErrorType` and recovery metadata. If the provider reports `invalid_args`, follow the parameter-error recovery below; otherwise report it without changing targets.
- `kanban_unavailable`: Kanban runtime is not initialized.
- `pet_action_unavailable`, `pet_unsupported`, or `pet_appearance_not_found`: report the Desktop pet limitation and avoid retrying without a different target.
- `interactive_file_picker_required`: the action requires an interactive picker and cannot complete from `archivePath` or `targetPath` unless a non-interactive action explicitly supports that argument.
- `target_unavailable` from a WorkPanel action means its workspace, Tab, or live WebView guest is absent. Re-read state before one corrected retry; do not create a different target as a fallback.
- `invalid_path`, `workspace_unavailable`, `path_outside_workspace`, or `file_unavailable` from `openLocalFile` means the path cannot be safely resolved from the current Agent's authoritative Workspace. Correct the relative path or report the limitation; do not use `file://`, an absolute path, or a temporary server fallback.
- `forbidden` from `openLocalFile` means the call did not come from an eligible ordinary Agent Platform Run in Desktop runtime. Do not retry through Desktop WS, HTTP Action Bridge, WebApp, debug, Team, or Agent WebClient routes.
- `display_target_unavailable`: the Desktop Main Window is absent, hidden, or minimized. Report the limitation; do not redirect the effect to another window or client.
- `capability_denied` from WorkPanel means the requested descriptor crosses the trusted Chat boundary or the workspace contains protected entries. Report it without forcing closure.
- `invalid_url` or `unsupported_native_surface` from WorkPanel means the requested URL or descriptor is outside the public capability. Do not rewrite it into an internal surface.
- Bridge unavailable: report that the Desktop action bridge is not reachable.
