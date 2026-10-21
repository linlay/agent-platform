# WorkPanel

The WorkPanel belongs to the current Chat. Platform supplies the Chat and Workspace from the trusted Run; no tool takes a Chat, workspace or item identifier.

## Arguments

- `workpanel_state`: `{}`.
- `workpanel_open`: `{url, title?, reload?}`. `url` is a required string: a webpage starting with `http://` or `https://`, or a file starting with `@workspace/` (bound project) or `@chat/` (current Chat) followed by a path relative to that root, for example `@workspace/docs/report.html` or the `@chat/artifacts/...` url returned by `artifact_publish`. Bare paths, host names without a scheme, absolute paths, `@runtime/` and `file://` are rejected. `title` is an optional string for a file preview and is not accepted for webpages. `reload` is an optional boolean.
- `workpanel_close`: `{all: true}`. `all` is a required boolean and must be true; it closes the whole panel. Close one tab with `surface_close` and its surfaceId.

Booleans are native JSON values, never strings. Unknown fields are rejected.

## State

`workpanel_state` lists the open items. Each item reports:

- `kind`: `web` or `file` (other kinds are items opened by the product itself).
- `url`: the address it was opened with, when known.
- `surfaceId`: the tab's identity for the surface tools. It is `null` while a page is still registering; read the state again.
- `capabilities`: what the tab accepts. Webpages and HTML file previews are live pages; every other tab can only be activated or closed.
- `title`, `active`, and whether it is `pinned` or `closable`.

In the Desktop runtime, the action result also includes `activeFile`: null, or exactly `{fileName, kind, path, mimeType, sizeBytes}`. `kind` is `html` or `markdown`. This describes only the currently active local original in the Run's own Chat, not every open file or the last file opened globally. `path` is the exact absolute path of the original on the Desktop host, including native Windows drive or UNC paths. Treat it as path data and preserve it exactly when passing it to file tools; it is not a preview URL or a new `workpanel_open` argument.

For example, an eligible original can report:

```json
{"fileName":"report.html","kind":"html","path":"/Users/alice/Desktop/report.html","mimeType":"text/html","sizeBytes":2048}
```

The same shape on Windows can contain `"path":"C:\\Users\\alice\\Desktop\\report.html"`. No document, item, workspace or container identifier is exposed. Overview, webpages and other preview kinds return null. A successful Desktop reply must include `activeFile`, even when it is null; missing fields produce `desktop_workpanel_contract_mismatch` instead of a legacy success. Standalone has no original-file extension. A closed, switched or unavailable original cannot be replaced by another file as a fallback.

`surface_list` lists the live pages, including pages of a Website or WebApp Copilot that are not WorkPanel tabs.

## Accessing the selected file

When the user's task refers to the current WorkPanel selection:

1. Call `workpanel_state` with `{}` and identify the active item. Do not supply a Chat ID, file name or path to this lookup.
2. If `activeFile` is non-null, use its exact `path` for the local original. The preview's isolated URL cannot be used with AWCP, CDP or file tools.
3. Otherwise, if the active file item has a recorded `@workspace/...` or `@chat/...` URL, use that reference with the file tools. `activeFile: null` does not make an existing Workspace or Chat preview unavailable.
4. If neither source identifies the intended file, ask the user to open or activate it. Do not search directories, infer a path from a file name, or substitute an inactive file or a file from another Chat.

Read the selected file with `file_read` before a requested edit. Use `file_edit` on that same file, preserve unrelated content and read back the expected change. A request to edit an original must not be implemented by editing a replacement copy. Copying, uploading or executing a file is a separate action and must follow the user's task; do not add it merely to read or edit a preview. Desktop refreshes a local original's preview when the file changes.

For a later task that depends on the current selection, read the state again rather than reusing an earlier path. Follow the reported recovery guidance for a stale or unavailable selection. A contract mismatch requires updating Desktop and Platform together, not another file selection or directory search.

Workspace and Chat files retain their recorded `@workspace/` and `@chat/` URLs and existing file-tool permissions. Reading original-file metadata does not grant additional file roots, suppress approvals, or turn a preview into a webpage Surface. This lookup never reads file contents, starts a Run, copies/uploads a file, or adds file references to a user message.

## Webpages

`workpanel_open` with an `http://` or `https://` url opens the page or activates the matching open page, and returns its `surfaceId`, `capabilities` and `status`. The address must not contain a username or password. Desktop-host-visible loopback services such as `http://127.0.0.1:3000`, `http://localhost:3000` and `http://[::1]:3000` are valid.

`reload: true` reloads the page after opening or activating it. When you already have the surfaceId, `surface_navigate` with `action: reload` does the same for that page.

## File previews

`workpanel_open` with `@workspace/<path>` or `@chat/<path>` previews a file and returns the tab's `surfaceId` and `capabilities`. The file must be a regular file inside the root it names; parent traversal is rejected. The two roots are independent: an `@chat/` file needs no bound project, and an `@workspace/` file needs one. `title` sets the tab title. File previews require the Desktop runtime.

An `@workspace/` file uses the built-in preview: HTML, PDF, images, text, audio and video are shown, and other formats open an unsupported-preview page. Reopening the same file activates its tab and reloads it. This preview may load bounded sibling resources and `data:` or `blob:` content. It cannot reach external HTTP(S), WebSocket or FTP resources, navigate elsewhere, open popups or request permissions.

An `@chat/` file opens the way it does when the user clicks it in the conversation: HTML and images in the isolated native preview, and other formats, including rendered Markdown, in the document viewer. Reopening the same file activates its tab. Tool-internal Chat files cannot be previewed.

An HTML preview of either root is a live page: read, operate, capture and resize it with the surface tools, as for a webpage. It cannot navigate elsewhere, use network CDP methods, AWCP or saved logins. Other previews have an item surfaceId that can only be activated or closed. When a real host-visible HTTP(S) service exists, open that address instead.

## Closing

To close one tab, pass its surfaceId from `workpanel_open`, `workpanel_state` or `surface_list` to `surface_close`. A URL or file name never identifies a tab. `workpanel_close` with `all: true` closes the whole panel. Closing a pinned or non-closable item fails. Closing the whole panel fails with `capability_denied` while protected items remain.

## Failure codes

- `desktop_workpanel_contract_mismatch`: the Desktop state reply lacks the required `activeFile` field. Update Desktop and Platform together; do not retry unchanged input or guess a file path.
- `source_chat_not_ready`: the Run's Chat binding is not ready, failed or ended. Follow the reported recovery condition; do not use another Chat or retry unchanged input.
- `source_chat_required`: the Run has no trusted Chat binding. Report the limitation.
- `invalid_url`: the address is not credential-free HTTP(S). Do not rewrite it to another scheme or guess a host.
- `invalid_path`, `path_outside_workspace`: the file path is malformed or resolves outside the Workspace, including through a symlink. Do not retry with another spelling.
- `workspace_unavailable`: the Workspace is missing or not visible to the Desktop host. Do not guess another root; a Chat file is addressed with `@chat/`.
- `file_unavailable`: the file is missing or is not a regular file.
- `target_unavailable`: the panel, item or live page is absent. Read `workpanel_state` once before a corrected retry.
- `forbidden`: file preview was requested outside an eligible ordinary Run in the Desktop runtime.
- `capability_denied`: the tab does not accept this tool (see its `capabilities`), or the request crosses the trusted Chat boundary or tries to remove protected state. Do not force it.
