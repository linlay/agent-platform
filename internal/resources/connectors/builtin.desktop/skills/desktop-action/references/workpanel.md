# WorkPanel Tabs And WebViews

Use these actions for the WorkPanel owned by the current run's trusted Chat. The Platform injects ownership from the run context; never choose another Chat or workspace through action arguments.

Ordinary Chat requests to open a website or URL default to openWeb. WorkPanel owns item creation, presentation and non-web tabs. Its HTTP(S) webpages also appear in desktop.web.listSurfaces and desktop_cdp Surface.list for the owning Chat/Run, and use the same surfaceId operations as Website/WebApp pages.

Use WorkPanel actions to manage the workspace/items; use desktop.web.* or desktop_cdp with the exact surfaceId to operate a webpage. Neither workspaceId nor itemId is a webpage identity.

## Actions

```text
desktop.workpanel.getState [read]
desktop.workpanel.openTab [execute]
desktop.workpanel.openWeb [execute]
desktop.workpanel.openLocalFile [execute, Desktop Platform only]
desktop.workpanel.refreshWeb [execute]
desktop.workpanel.activateTab [execute]
desktop.workpanel.closeTab [execute]
desktop.workpanel.closeWorkpanel [execute]
```

## Ownership And Forbidden Arguments

Every action is scoped by trusted `source.chatId`. Never include any of these fields as top-level keys in `args`:

```text
chatId
workspaceId
surfaceId
agentKey
stableKey
preload
webPreferences
```

A contract-valid WebClient `descriptor.context` may include contextual `chatId` or `agentKey` fields required by its module. Any `chatId` must match the trusted owner Chat. Do not attempt to target another Chat through descriptor context; Desktop rejects cross-Chat descriptors.

## Contracts

### Read State

`desktop.workpanel.getState` takes no arguments. Its result includes:

- `workspaceId`
- optional `state`
- `state.items[]`, where each item has an `itemId`, descriptor, title, closable/pinned flags, and creation time
- `state.activeItemId`

Use an item's `itemId` as the `tabId` for activation or closure.

### Open A General Tab

`desktop.workpanel.openTab` requires exact `{descriptor}`. Prefer the narrower `openWeb` action for ordinary URLs.

A web descriptor may contain only:

```json
{
  "kind": "web",
  "url": "https://example.com/",
  "title": "Example",
  "pinned": false,
  "closable": true
}
```

`title`, `pinned`, and `closable` are optional. The URL must use HTTP(S) and must not contain credentials.

A WebClient descriptor uses `kind: "webclient"` plus an exact supported `module`, absolute app-relative `route`, and contract-valid `context`. Use it only when the complete descriptor is already supplied by a trusted product flow or the current conversation. Do not invent module routes, identities, or context fields. Native descriptors are not a usable public capability.

Opening is deterministic: if a matching stable item already exists, Desktop activates it instead of duplicating it.

### Open A WebView

`desktop.workpanel.openWeb` requires exact `{url}`. It accepts only explicit HTTP(S) URLs without a username or password, normalizes the URL, and opens or activates the matching WorkPanel WebView. Desktop-host-visible loopback services such as `http://127.0.0.1:3000`, `http://localhost:3000`, and `http://[::1]:3000` are valid.

```json
{
  "action": "desktop.workpanel.openWeb",
  "args": {
    "url": "https://example.com/docs"
  }
}
```

Open only a URL explicitly provided by the user, returned by a trusted tool, already present in the current conversation/session, or derived as the loopback URL of a service started for the current task. A service running only inside a container is not host-visible unless its port is exposed. Do not synthesize arbitrary LAN/private-network, tracking, authentication, or side-effecting URLs.

The openWeb result includes `{workspace, surfaceId, containerId, status}`. Use the returned surfaceId for subsequent webpage operations; creation does not by itself prove the page has finished loading.

### Open A Workspace File

`desktop.workpanel.openLocalFile` requires `{path}` and accepts optional `{title}`. Use it only from an ordinary Agent Platform Run in Desktop runtime. `path` is relative to the current Agent's authoritative Workspace:

```json
{
  "action": "desktop.workpanel.openLocalFile",
  "args": {
    "path": "artifacts/report.html",
    "title": "Report"
  }
}
```

Never pass an absolute path, drive-qualified path, UNC path, `..`, control characters, `file://`, `chatId`, `agentKey`, or a Main-generated handle. Desktop resolves both the Workspace and target with realpath, requires a regular file inside that Workspace, and returns only the committed `{workspace}` state.

Use this action for HTML, PDF, images, text, audio, video, and other ordinary Workspace files. Known formats use the existing preview; other formats open the existing unsupported-preview page. Reopening the same canonical file for the same Chat activates the existing Tab and reloads a previewable guest instead of creating a duplicate.

Local-file previews may load bounded sibling resources and necessary `data:` or `blob:` content, but cannot access external HTTP(S), WebSocket, or FTP resources, navigate externally, open popups, request permissions, or use Desktop/WebApp bridges. Run completion prevents new calls but does not close an existing Tab.

Do not pass a `local-file` descriptor to `openTab`, use `file://`, or start a temporary HTTP server as a file-preview fallback. When a real Desktop-host-visible HTTP(S) service already exists, use `openWeb` instead; ordinary Web items retain their normal network behavior.

### Refresh An Existing WebView

`desktop.workpanel.refreshWeb` requires exact `{url}`. Desktop normalizes the URL, finds the matching already-open WebView, reloads its live guest in place, and activates its Tab.

```json
{
  "action": "desktop.workpanel.refreshWeb",
  "args": {
    "url": "https://example.com/docs"
  }
}
```

This action does not create a missing WebView. On `target_unavailable`, call `getState` once to distinguish a closed item from a temporarily unavailable guest. Call `openWeb` only when opening the known URL is independently required by the user.

### Activate Or Close A Tab

`desktop.workpanel.activateTab` and `desktop.workpanel.closeTab` require exact `{tabId}`:

```json
{
  "action": "desktop.workpanel.activateTab",
  "args": {
    "tabId": "item:..."
  }
}
```

```json
{
  "action": "desktop.workpanel.closeTab",
  "args": {
    "tabId": "item:..."
  }
}
```

Use only a current `state.items[].itemId`. Closing a pinned or non-closable Tab fails.

### Close The WorkPanel

`desktop.workpanel.closeWorkpanel` takes no arguments and closes the whole current-Chat workspace:

```json
{
  "action": "desktop.workpanel.closeWorkpanel"
}
```

Use it only when the user asked to close the full WorkPanel. It fails with `capability_denied` when protected non-overview entries remain.

## Webpage control and AWCP

Use the open result or Surface.list to obtain the exact surfaceId; never substitute a WorkPanel itemId/tabId. Any authorized HTTP(S) WorkPanel webpage may provide AWCP. For page-content tasks, read its AWCP.getManual directory first, then a matching section and invoke. If the host confirms the protocol is absent or no section matches, continue through CDP/DOM. Keep the source Chat/Run and exact page throughout; local-file previews do not acquire this capability.

## Failure Handling

- `source_chat_not_ready`: the canonical source Run grant is not ready, failed or ended. Follow the reported recovery condition; do not use another Chat or repeatedly retry unchanged input.
- `source_chat_required`: the run lacks a trusted Chat binding. Report the limitation.
- `invalid_request`: the descriptor or arguments are malformed or contain forbidden fields. Re-read this contract and retry once with the exact shape.
- `invalid_url`: the URL is not credential-free HTTP(S). Do not silently rewrite another scheme or add a guessed host.
- `invalid_path`: the local path is empty, malformed, absolute, drive-qualified, UNC, contains `..` or control characters, or uses a URL scheme such as `file://`.
- `workspace_unavailable`: the authoritative Agent Workspace is missing or not visible to the Desktop host. Do not map `/workspace` or guess another root.
- `path_outside_workspace`: realpath resolution escaped the authoritative Workspace, including through a symlink or junction. Do not retry with an alternate representation.
- `file_unavailable`: the target is missing or is not a regular file.
- `target_unavailable`: the workspace, Tab, or live WebView guest is absent. Re-read state before one corrected retry.
- `forbidden`: `openLocalFile` was called outside an eligible ordinary Agent Platform Run in Desktop runtime. Do not retry through another bridge.
- `capability_denied`: the request crosses the trusted Chat boundary or tries to remove protected state. Do not force the operation.
- `unsupported_native_surface`: the native surface is not available through this public capability. Report it instead of inventing an internal route.
Ordinary Chat “open a website/URL” requests default to `desktop.workpanel.openWeb`. The result contains the opened `surfaceId`, `containerId`, `status` and workspace. Use that Surface directly for CDP or shared `desktop.web.*` page operations. `Surface.list` and `desktop.web.listSurfaces` rediscover owned live network pages. Local-file previews do not gain generic CDP access. WorkPanel lifecycle `tabId` remains the outer item ID.
