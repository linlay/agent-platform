# Containers and webpage Surfaces

A Container hosts pages. A Surface identifies one webpage instance, regardless of Website, WebApp or Chat WorkPanel presentation. The same URL opened twice has two Surface identities; navigation and reload preserve each identity.

`desktop.web.listSurfaces` returns live pages in the trusted Chat/Run scope, including authorized background pages. Each entry has `surfaceId` and `containerId`. `desktop.web.getSurfaceState` accepts `{surfaceId}` and returns `{surface}` for that one page. It never means “all tabs in a container”.

All these actions require the exact webpage `surfaceId`:

| Action | Additional args | Effect |
| --- | --- | --- |
| `desktop.web.activateSurface` / `desktop.web.switchTab` | none | Show/select the page |
| `desktop.web.navigate` | `url` | Navigate the page |
| `desktop.web.reload` / `desktop.web.refreshSurface` | none | Reload only this page |
| `desktop.web.goBack` | none | Go back in this page's history |
| `desktop.web.openTab` | `url` | Open another page in the owning container; return its Surface identity |
| `desktop.web.closeTab` | none | Close this page through the host lifecycle |
| `desktop.web.executeScript` | `script` | Execute JavaScript in this page |
| `desktop.web.interactElement` | `selector`, `action`, optional `value` | Use click/fill/scroll/focus/select on a verified element |

WebApp remains single-page. WorkPanel opens another Web item rather than adding a nested tab. Closing a Website's final page closes its live container, preserving the saved Website entry.

```json
{"action":"desktop.web.reload","args":{"surfaceId":"page:returned-id"}}
```

Use `desktop-cdp` for screenshots, DOM, protocol calls and AWCP. Before page-content reads or mutations (including executeScript/interactElement), read the selected page's AWCP manual directory and prefer a matching section. Any authorized network webpage can provide AWCP; its container is not a support test. Neither a `containerId` nor a WorkPanel `tabId` may substitute for `surfaceId`. Background authorization comes from the trusted Run/Chat; foreground display is not authorization.

`desktop.web.exportArtifact` accepts `{surfaceId, format}` for an authorized WebApp export provider and saves to Downloads. It does not confer an export bridge on ordinary webpages.
