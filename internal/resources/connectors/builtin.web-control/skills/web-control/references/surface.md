# Surface tools

Each surface tool acts on one live webpage and takes top-level arguments. Unknown fields are rejected. Use native JSON types: booleans and numbers are never strings.

`surfaceId` is the exact webpage identity returned by `workpanel_open` or `surface_list`. A url is not a page identity. It is required everywhere except `surface_state`.

## Discovering pages

- `surface_list`: `{}`. Lists the live webpages this Run is authorized to control, including background pages. Each entry has surfaceId, title, URL and page state.
- `surface_state`: `{surfaceId?}`. Reads the state of one webpage. Omit surfaceId only to read the current page of the application this Run belongs to; an ordinary Chat may have no current page.

## Navigation and visibility

- `surface_navigate`: `{surfaceId, action, url?, ignoreCache?}`. Navigates inside an existing page; the surfaceId stays the same.
  - action `"goto"` loads `url`, which is required, must start with `http://` or `https://`, and is rejected for other actions.
  - action `"reload"` reloads the page; `ignoreCache: true` bypasses the cache and is valid only here.
  - action `"back"` goes back in the page history.
  - Example: `{"surfaceId": "page:returned-id", "action": "reload", "ignoreCache": true}`.
- `surface_activate`: `{surfaceId}`. Brings the page to the foreground so the user can see it. Not needed before reading or operating a page.
- `surface_close`: `{surfaceId}`. Closes the page; its surfaceId becomes invalid. Do not also call `workpanel_close` for the same page.

## Reading

- `surface_screenshot`: `{surfaceId, fullPage?}`. Captures a PNG. `fullPage: true` captures the whole scrollable page instead of the visible viewport. The image is saved to the current Chat and the result exposes a `referenceName`; inspect it with `vision_recognize`.
- `surface_evaluate`: `{surfaceId, expression | expressionFile, awaitPromise?}`. Evaluates JavaScript and returns its value.
  - Provide exactly one of `expression` and `expressionFile`.
  - `expression` is a JavaScript expression; use a synchronous IIFE for synchronous reads.
  - `expressionFile` is the path of a UTF-8 JavaScript file, for scripts too large to pass inline. Relative paths resolve from the execution workspace; explicit roots such as `@chat` and authorized absolute paths follow the standard file read AccessPolicy and approval rules.
  - `awaitPromise` is a boolean, default true: wait for a returned Promise to settle.
  - An exception reported in `exceptionDetails` is a script failure.
  - Example: `{"surfaceId": "page:returned-id", "expression": "(() => document.title)()"}`.

## Operating

- `surface_click`: `{surfaceId, selector | x+y, waitFor?}`. Clicks with real input events.
  - Provide either a CSS `selector` (non-empty string, at most 4096 characters), or both `x` and `y` as finite non-negative numbers in Chromium CSS pixels. Never combine selector with x/y.
  - `waitFor` is an optional object `{state, selector?, value?, checked?}` describing a condition to observe after the click. Unknown fields are rejected. `state` is required and selects the shape:

    | state | selector | value | checked | Met when |
    | --- | --- | --- | --- | --- |
    | `"visible"` | required | not allowed | not allowed | the element is visible |
    | `"hidden"` | required | not allowed | not allowed | the element is hidden or absent |
    | `"value"` | required | required string | not allowed | the element's value equals `value` exactly |
    | `"checked"` | required | not allowed | required boolean | the element's checked state (or `aria-checked`) equals `checked` |
    | `"url"` | not allowed | required non-empty string | not allowed | the page URL equals `value` exactly |

    `waitFor.selector` and the `"url"` state's `value` are non-empty strings of at most 4096 characters. The default total click deadline is 3 seconds, shared by locating, clicking and observing the condition; this tool has no timeout argument. Examples: `{"selector": ".saved", "state": "visible"}`, `{"selector": "#agree", "state": "checked", "checked": true}`, `{"state": "url", "value": "https://example.com/done"}`.
  - With `waitFor`, the result reports whether the condition matched; an unmet condition is a timeout, not proof that the click failed.
  - Without `waitFor`, success proves input delivery, not business completion. Do not replay a click whose outcome is reported as unknown; read the page state first.
  - Example: `{"surfaceId": "page:returned-id", "selector": "button.save", "waitFor": {"selector": ".saved", "state": "visible"}}`.
- `surface_element`: `{surfaceId, selector, action, value?}`. Acts on one element identified by a verified CSS selector.
  - action `"fill"` sets an input value and `"select"` chooses an option; both take `value`, a string.
  - action `"focus"` focuses the element and `"scroll"` scrolls it into view.
  - Use `surface_click` to click. Verify the expected state afterwards.
- `surface_cdp`: `{surfaceId, method, params | paramsFile}`. Sends one Chrome DevTools Protocol method that has no dedicated tool.
  - `method` is one of `Page.enable`, `DOM.getDocument`, `DOM.querySelector`, `DOM.querySelectorAll`, `DOM.getOuterHTML`, `DOM.getBoxModel`, `Input.dispatchMouseEvent`, `Input.dispatchKeyEvent`, `Input.insertText`, `Network.enable`, `Network.disable`.
  - `params` is the method's JSON parameter object. `paramsFile` is the path of a UTF-8 JSON file containing only that object, resolved like `expressionFile`. They are mutually exclusive; omit both for a method without parameters.
  - Navigation, screenshots, script evaluation and clicks use their dedicated tools; website business actions use `awcp_manual` and `awcp_invoke`.

## Before changing page content

Call `awcp_manual` for the page first and prefer a matching AWCP section. Use `surface_evaluate`, `surface_click`, `surface_element` or `surface_cdp` on page content only when the page has no AWCP manual or no matching section; see [AWCP](awcp.md).

After any mutation, re-read the state before retrying; dispatching an event does not prove success. On `invalid_args`, fix every reported field. Never reload or close the page to fix parameter types.
