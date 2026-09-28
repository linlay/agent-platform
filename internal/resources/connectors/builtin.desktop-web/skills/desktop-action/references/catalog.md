# WorkPanel and webpage action catalog

Use these exact action names with `desktop_action`. Read the linked contract before constructing `args`; Platform also enforces its runtime allowlist and caller scope.

| Action | Kind | Category |
| --- | --- | --- |
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

- Read [WorkPanel](workpanel.md) for every `desktop.workpanel.*` action. WorkPanel binds to the current Chat; tab lifecycle uses `state.items[].itemId` as `tabId`.
- Read [web surfaces](web-surfaces.md) for every `desktop.web.*` action. Page operations use an exact `surfaceId`; a WorkPanel item ID or Container ID cannot replace it.
- Use `desktop-cdp` for page content, screenshots, DOM, input, CDP and AWCP. Follow its manual-first routing before webpage content reads or interaction.
- Use `desktop.web.getSurfaceState`, not the removed `desktop.web.getActiveSurface`. Use the documented page-content operations rather than removed `getPageContext`, `readPageData` or `extractStructured` names.
- If a documented action returns `unknown_action`, report the version mismatch. Do not guess aliases or retry through a different transport.
