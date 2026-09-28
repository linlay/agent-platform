---
name: desktop-action
description: "Operate the current Chat's WorkPanel, preview workspace files, and manage authorized webpages and tabs through desktop_action. Use desktop-cdp for page content, screenshots, DOM, CDP and AWCP."
metadata:
  version: 1.0.0
---

# desktop-action

Use `desktop_action` for WorkPanel and webpage lifecycle operations. Read [the action catalog](references/catalog.md) to select an exact action, then read the matching contract before the first call:

- [WorkPanel](references/workpanel.md): state, opening webpages and workspace files, refreshing, activating and closing tabs, general tabs and closing the panel.
- [Web surfaces](references/web-surfaces.md): discovering authorized pages, navigation, tab management, page interaction and artifact export.
- Use the installed `desktop-cdp` skill for screenshots, DOM, input, JavaScript, CDP and AWCP.

## Page selection and routing

Ordinary Chat requests to open a website/URL default to `desktop.workpanel.openWeb`. In Website/WebApp Copilot, continue in the owning application's context. A Container hosts pages; a Surface is one live webpage. Use the exact returned `surfaceId` or discover it with `desktop.web.listSurfaces` / `Surface.list`. WorkPanel item IDs and container IDs are not page identities. Background pages may be used within the same authorization scope.

Before page-content reads or interaction on any authorized HTTP(S) webpage, follow desktop-cdp's manual-first routing: read one `AWCP.getManual` directory, read a matching section and invoke it. Only a host-confirmed missing protocol entry or no matching section permits DOM work. This applies to `desktop.web.interactElement` and `desktop.web.executeScript` too. Opening URLs, managing tabs and screenshots do not require a manual probe. Local-file previews do not gain generic CDP access.

## Call contracts

- Send an exact action name from the installed catalog, with action-specific inputs in `args`. Returned objects are not input contracts; read the reference before constructing arguments.
- Never put transport-owned `source` in `args`. Platform provides the trusted Chat/Run context. Use optional top-level `requestId` for correlation.
- WorkPanel always belongs to the current Chat. Do not supply top-level `chatId`, `workspaceId`, `surfaceId`, `agentKey`, `stableKey`, `preload` or `webPreferences` in WorkPanel args. Read the WorkPanel reference for valid descriptor context.
- Open URLs supplied by the user, returned by a trusted tool, present in this session, or derived from a host-visible service started for this task. Do not invent arbitrary public, LAN, tracking, authentication or side-effecting URLs.
- Use `openLocalFile` with a Workspace-relative path for local previews. Do not use `file://` or start a temporary HTTP server as a fallback. Use `openWeb` for actual host-visible HTTP(S) services.
- Rely on the existing Desktop confirmation flow for mutations. Ask the user when the target is unclear.
- Treat `response.ok: true` with `result.ok: false` as failure. Report the business message or issues. Inspect state before retrying mutations or unknown outcomes.

## Recovery

For `unknown_action`, check the installed catalog and report a Platform/Desktop version mismatch when appropriate. Do not try removed aliases or switch transport. For `invalid_args`, inspect structured field diagnostics, reread the contract and retry once with corrected input. Verify one call before issuing a batch with the same shape.

For a disconnected/unavailable target or timeout, report the connection limitation without selecting another Chat or client window. On a closed or replaced page, rediscover within the same authorization scope. For WorkPanel path, ownership and lifecycle failures, use the exact recovery in the WorkPanel reference; do not redirect through another transport or fabricate a new target.
