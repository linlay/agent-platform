---
name: desktop-cdp
description: "Inspect and operate authorized Website, WebApp and Chat WorkPanel webpages using desktop_cdp and exact surfaceId values, including background pages, screenshots, DOM, input and AWCP."
version: 0.7.1
---

# desktop-cdp

Use `desktop_cdp` for webpage operations. A **Container** hosts pages; a **Surface** is one independently addressable live webpage. Every tab has its own `surfaceId`. URLs, container IDs and WorkPanel item IDs are not page identities.

## Choose the page

- Ordinary Chat requests to open a website/URL default to `desktop.workpanel.openWeb`; no “sidebar” wording is required. Its result includes `surfaceId`, `containerId` and `status`.
- In Website/WebApp Copilot, continue within the owning application. Use `Surface.list` to discover the Run's authorized pages. Website tabs are separate Surfaces; WebApp remains single-page.
- `Surface.list` lists only the current Run's authorized application or owned WorkPanel webpages. `Surface.getCurrent` reads the selected page of an authorized application; ordinary Chat may have no current page even when its WorkPanel contains pages. Use the opened ID or `Surface.list` in that case.
- Supply the exact `surfaceId` for every page operation. A background page can be operated directly if authorized; `Page.bringToFront` is only needed when the user needs to see it.
- Navigation and reload preserve Surface identity. Closing/reopening invalidates the old identity. On a closed/replaced page, rediscover within the same authorization scope; never select another Chat or application as a fallback.

## Page-content routing: manual first

Any authorized HTTP(S) webpage may provide AWCP, whether it is in Website, WebApp or ordinary Chat WorkPanel. The container does not determine protocol support. For ordinary Chat, always supply the exact WorkPanel webpage surfaceId; do not borrow the foreground page. Local-file previews are outside this network-page capability.

Before page-content reads, DOM inspection or interaction:

1. Read one directory with `{"method":"AWCP.getManual","surfaceId":"page:returned-id"}`.
2. If a section matches the task, read it with `{"method":"AWCP.getManual","surfaceId":"page:returned-id","params":{"section":"orders.read","revision":"returned-revision"}}`. Read only needed sections.
3. Invoke with `{"method":"AWCP.invoke","surfaceId":"page:returned-id","params":{"revision":"returned-revision","action":"orders.read","args":{}}}`. Build native JSON arguments from that section's inputSchema.
4. Only a host-confirmed missing AWCP entry (`awcp_protocol_unavailable`) or a valid directory without a matching section permits ordinary CDP/DOM for that task. Permission failures, unsupported versions, malformed contracts and unknown execution outcomes are not absence of AWCP. Read [AWCP](references/awcp.md) for recovery.

Keep the same surfaceId throughout. Reuse a valid manual binding for the same page and Run; do not probe before every click. Navigation, guest replacement or stale revision requires fresh discovery. Never reuse a manual across Runs. Platform does not inject dynamic schemas, bind revision implicitly or replay calls. Screenshot-only, navigation and tab-management tasks do not require a manual probe. Successful AWCP results need no automatic DOM or screenshot verification.

## Calls (after page-content routing when applicable)

```json
{"method":"Surface.list"}
```

The result contains `surfaces`, each with `surfaceId`, `containerId`, title, URL and page state.

```json
{"method":"Runtime.evaluate","surfaceId":"page:returned-id","params":{"expression":"({title:document.title,url:location.href})","returnByValue":true}}
```

```json
{"method":"Input.click","surfaceId":"page:returned-id","params":{"selector":"#save","waitFor":{"selector":".saved","state":"visible"}}}
```

`Input.click` is a Desktop-composed operation. Use exactly one selector or x/y pair; coordinates use Chromium CSS pixels on both macOS and Windows. `waitFor` is optional. Without it, success proves input delivery, not business completion. It may observe visible/hidden, value, checked or URL state. Do not replay a click when the result reports an unknown outcome.

```json
{"method":"Page.reload","surfaceId":"page:returned-id","params":{"ignoreCache":true}}
```

```json
{"method":"Page.captureScreenshot","surfaceId":"page:returned-id","params":{"format":"png","captureBeyondViewport":true}}
```

Screenshot results expose `response.result.data.referenceName` and `visionRecognizeImage`. Inspect with `vision_recognize` using that reference. Do not repeat screenshot base64 in conversation or files.

Use `Surface.getState` to inspect one page, `Surface.goBack` for history, `Surface.open` with `params.url` to open another page in the selected page's container, and `Surface.close` to close it. WebApp does not support adding tabs. Do not also issue a second close action.

## Parameters and results

Use direct JSON `params` for ordinary reads, inputs and short scripts. Use `paramsFile` only for a large script/batch that needs file loading; it contains only the params object. Native booleans and numbers are required. A type error does not justify changing the whole task to file-based calls.

Use synchronous IIFEs for synchronous reads and `awaitPromise:true` for invoked asynchronous expressions. `Runtime.evaluate.exceptionDetails` is a script failure even when CDP transport succeeds. Re-read state before retrying a mutation; event delivery alone does not establish success. Read selectors/coordinates from the page, and verify the expected state after input.

## References

- [AWCP](references/awcp.md): progressive page manual discovery and business actions.
- [Ant Design forms](references/ant-design-form-fill.md): framework-backed form inputs and verification.
- [Troubleshooting](references/troubleshooting.md): authorization, lifecycle and command errors.
- [Raw gateway](references/commands.md): only for explicitly debugging standard CDP compatibility. Normal tool calls never use HTTP paths as methods.
