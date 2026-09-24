# Surface operation failures

- `target_required`: supply the exact `surfaceId` from the open result or `Surface.list`.
- `target_not_found`, `surface_not_ready`: the page closed, was replaced, or has not registered. Rediscover in the same scope. Do not automatically open a replacement.
- `target_not_owned_by_chat`, `target_not_in_current_surface`, `site_control_unavailable`: the selected page is outside the trusted Chat/Run grant or that grant ended. Selecting a background page is valid only within the grant. Do not borrow the foreground page or another application's ID.
- `current_target_unavailable`: no current page; use `Surface.list` for authorized WorkPanel pages.
- `invalid_args`: correct all reported fields using native JSON types. The preflight rejection did not execute this command; it says nothing about prior commands. Never reload a form to repair a parameter error.
- `method_not_allowed`: Desktop and Platform must be deployed together with the Surface contract. Do not retry removed methods or switch transport to bypass a stale tool schema.
- `web_action_unavailable`: a host action could not route; this does not mean Website content is read-only. Rediscover and use the returned Surface with supported CDP operations.
- `Runtime.evaluate.exceptionDetails`: inspect script failure details and current page state. Arbitrary JavaScript return values are not host execution status.
- Timeout/cancel after mutation: side effects may have happened. Verify state before a deliberate retry.

## AWCP Manual and invocation

Any authorized network webpage may provide AWCP, including ordinary Chat WorkPanel pages. `awcp_protocol_unavailable` means the host confirmed that the selected page has no AWCP entry; ordinary CDP/DOM may then serve the task. `awcp_unsupported_protocol`, `awcp_invalid_contract` and authorization failures are not permission to bypass AWCP. `source_chat_not_ready` requires restoring the source Run grant, never borrowing another Chat.

Use `AWCP.getManual` on the selected `surfaceId`: first the directory, then exactly `{section,revision}` in `params`. Invoke on the same Surface with exactly `{revision,action,args}` in `params`. Do not probe or invoke AWCP through `Runtime.evaluate`.

Host `awcp_preflight_rejected` carries `stage:desktop_preflight`, `executionStarted:false` and one of `manual_required`, `page_changed`, `stale_revision`, or `action_not_found`. Read an unread section; for stale/page-changed evidence, revalidate the intended Surface and read its fresh directory and selected section. Do not substitute another page or guess an action name.

Page `invalid_arguments.details.fieldErrors` may guide a deliberate low-impact argument correction against the section Schema. It is not Desktop preflight evidence. Timeout, disconnect, cancellation and unknown execution require read-only reconciliation before a new mutation; do not automatically retry or bypass through DOM. See [AWCP](awcp.md) for capability and fallback boundaries.
