# Page operation failures

- `target_required`: supply the exact `surfaceId` from `workpanel_open` or `surface_list`.
- `target_not_found`, `surface_not_ready`: the page closed, was replaced or has not registered. Rediscover it with `surface_list` in the same Run. Do not automatically open a replacement.
- `target_not_owned_by_chat`, `target_not_in_current_surface`, `site_control_unavailable`: the page is outside the trusted Chat/Run grant, or that grant ended. A background page is valid only within the grant. Do not borrow the foreground page or another application's page.
- `current_target_unavailable`: there is no current page; pass a surfaceId from `surface_list`.
- `invalid_args`: correct every reported field using native JSON types. The rejected call did not execute and says nothing about earlier calls. Never reload a form to repair a parameter error.
- `method_not_allowed`: Desktop and Platform must be deployed together. Do not retry removed methods or switch transport.
- `web_action_unavailable`: a host action could not be routed. This does not mean the page is read-only; rediscover the page and use the other surface tools.
- `desktop_cdp_unsupported_runtime`: the surface and AWCP tools require the Desktop runtime. Only the WorkPanel tools work without it.
- `exceptionDetails` from `surface_evaluate`: a script failure. Inspect the details and the current page state. A script's return value is not a host execution status.
- Timeout or cancellation after a mutation: side effects may have happened. Verify state before a deliberate retry.

## AWCP

`awcp_protocol_unavailable` means the host confirmed that the selected page has no AWCP entry; the surface content tools may then serve the task. `awcp_unsupported_protocol`, `awcp_invalid_contract` and authorization failures are not permission to bypass AWCP. `source_chat_not_ready` requires restoring the Run's Chat binding, never borrowing another Chat.

Read the directory with `awcp_manual`, then the section with `section` and `revision`, then call `awcp_invoke` on the same page. Do not probe or invoke AWCP through `surface_evaluate`.

Host `awcp_preflight_rejected` carries `stage:desktop_preflight`, `executionStarted:false` and one of `manual_required`, `page_changed`, `stale_revision` or `action_not_found`. Read the unread section; for stale or page-changed evidence, revalidate the page and read its fresh directory and section. Do not substitute another page or guess an action name.

Page `invalid_arguments.details.fieldErrors` may guide a deliberate low-impact correction against the section schema. Timeout, disconnect, cancellation and unknown outcomes require read-only reconciliation before a new mutation. See [AWCP](awcp.md).
