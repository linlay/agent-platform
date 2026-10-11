# WorkPanel

The WorkPanel belongs to the current Chat. Platform supplies the Chat and Workspace from the trusted Run; no tool takes a Chat, workspace or item identifier.

## Arguments

- `workpanel_state`: `{}`.
- `workpanel_open`: `{url, title?, reload?}`. `url` is a required string: a webpage starting with `http://` or `https://`, or a file starting with `@workspace/` (bound project) or `@chat/` (current Chat) followed by a path relative to that root, for example `@workspace/docs/report.html` or the `@chat/artifacts/...` url returned by `artifact_publish`. Bare paths, host names without a scheme, absolute paths, `@runtime/` and `file://` are rejected. `title` is an optional string for a file preview and is not accepted for webpages. `reload` is an optional boolean.
- `workpanel_close`: exactly one of `{url}` or `{all: true}`. `url` is the `@workspace/` or `@chat/` file that was passed to `workpanel_open`; webpage URLs are not accepted. `all` is a boolean.

Booleans are native JSON values, never strings. Unknown fields are rejected.

## State

`workpanel_state` lists the open items. Each item reports:

- `kind`: `web` or `file` (other kinds are items opened by the product itself).
- `url`: the address it was opened with, when known.
- `title`, `active`, and whether it is `pinned` or `closable`.

Use `surface_list` for live webpages and their surfaceId.

## Webpages

`workpanel_open` with an `http://` or `https://` url opens the page or activates the matching open page, and returns its `surfaceId` and `status`. The address must not contain a username or password. Desktop-host-visible loopback services such as `http://127.0.0.1:3000`, `http://localhost:3000` and `http://[::1]:3000` are valid.

`reload: true` reloads the page after opening or activating it. When you already have the surfaceId, `surface_navigate` with `action: reload` does the same for that page.

## File previews

`workpanel_open` with `@workspace/<path>` or `@chat/<path>` previews a file. The file must be a regular file inside the root it names; parent traversal is rejected. The two roots are independent: an `@chat/` file needs no bound project, and an `@workspace/` file needs one. `title` sets the tab title. File previews require the Desktop runtime.

An `@workspace/` file uses the built-in preview: HTML, PDF, images, text, audio and video are shown, and other formats open an unsupported-preview page. Reopening the same file activates its tab and reloads it. This preview may load bounded sibling resources and `data:` or `blob:` content. It cannot reach external HTTP(S), WebSocket or FTP resources, navigate elsewhere, open popups or request permissions.

An `@chat/` file opens the way it does when the user clicks it in the conversation: HTML and images in the isolated native preview, and other formats, including rendered Markdown, in the document viewer. Reopening the same file activates its tab. Tool-internal Chat files cannot be previewed.

File previews have no surfaceId. When a real host-visible HTTP(S) service exists, open that address instead.

## Closing

To close a webpage, call `surface_list` and pass the selected surfaceId to `surface_close`. `workpanel_close` accepts only a file preview URL (`@workspace/` or `@chat/`), or `all: true` for the whole panel. Closing a pinned or non-closable item fails. Closing the whole panel fails with `capability_denied` while protected items remain.

`workpanel_item_not_found` means no unique file preview has a recorded path matching the url; call `workpanel_state` to see what is open. Missing paths and duplicate path matches are rejected. A file name alone never identifies a preview, even if only one item has that name.

## Failure codes

- `source_chat_not_ready`: the Run's Chat binding is not ready, failed or ended. Follow the reported recovery condition; do not use another Chat or retry unchanged input.
- `source_chat_required`: the Run has no trusted Chat binding. Report the limitation.
- `invalid_url`: the address is not credential-free HTTP(S). Do not rewrite it to another scheme or guess a host.
- `invalid_path`, `path_outside_workspace`: the file path is malformed or resolves outside the Workspace, including through a symlink. Do not retry with another spelling.
- `workspace_unavailable`: the Workspace is missing or not visible to the Desktop host. Do not guess another root; a Chat file is addressed with `@chat/`.
- `file_unavailable`: the file is missing or is not a regular file.
- `target_unavailable`: the panel, item or live page is absent. Read `workpanel_state` once before a corrected retry.
- `forbidden`: file preview was requested outside an eligible ordinary Run in the Desktop runtime.
- `capability_denied`: the request crosses the trusted Chat boundary or tries to remove protected state. Do not force it.
