Use `desktop_webapp` with `{action,args}`.

# Website Apps

Use these flattened actions for local WebApp installation, runtime control, Tunnel publishing, and complete removal.

## Actions

```text
webapp.getStatus [read]
webapp.checkRuntime [validate]
webapp.start [execute]
webapp.stop [execute]
webapp.restart [execute]
webapp.open [execute]
webapp.updatePreferences [execute]
webapp.package.init [execute]
webapp.package.validate [validate]
webapp.package.build [execute]
webapp.install [execute]
webapp.uninstall [execute]
webapp.getPublishStatus [read]
webapp.publish [execute]
webapp.unpublish [execute]
```

## Arguments

- `getStatus`, `checkRuntime`, `start`, `stop`, `restart`, `open`, `getPublishStatus`, `publish`, `unpublish`, and `uninstall`: pass `webappId` or `id`.
- `webapp.getStatus` reports only local runtime state. `webapp.getPublishStatus` separately reports Tunnel readiness and route state as `{ ready, info, state, message }`; a signed-out, unconfigured, disabled, or disconnected Tunnel returns `ready=false` without making the action call fail.
- `webapp.checkRuntime` checks Java, native, container, or static launcher conditions without starting a process or gateway. Read `result.ready` and `result.issues`; a missing WebApp returns `webapp_not_found`.
- `webapp.open` starts the installed WebApp and navigates to `/webs/webapp:<id>`.
- `webapp.updatePreferences`: pass `{ "id": "...", "patch": { "label": "...", "openMode": "workspace|dialog" } }` with only the fields being changed.
- `webapp.install`: pass `{ "archivePath": "<ZIP path>" }`. Use only `archivePath`; `workspaceArchivePath` is removed. It installs or updates one ZIP transactionally, validates its package before Desktop confirmation, and returns `{ webappId, operation }` without starting, opening, navigating, or publishing. Continue using the returned `webappId`; there is no `itemId`, `item`, or `message` in this success result. Do not pass `itemId`; install a market item with `market.installItem`.
- Installation `archivePath` accepts an absolute local ZIP path on the Desktop host, a path relative to the current Run Workspace, `@workspace/<path>`, or `@chat/<path>`. Absolute host paths may be outside Workspace; copying into Workspace is unnecessary. `@chat` identifies the current Chat directory and can install without a project Workspace. Platform resolves aliases to host paths before dispatch. Relative paths and `@workspace` require a bound Workspace. A remote or container absolute path is not automatically a Desktop-host path. URLs, `file://`, shell expansion, parent traversal in relative/alias paths, Windows UNC/device and drive-relative paths are rejected.
- Omit `expectedId` unless the actual Manifest `id` is already known. It must match `webapp-` plus 16 lowercase hexadecimal digits and the ZIP Manifest; a display name or `key` such as `personal-workbench` is not an id. Installation reads the Manifest itself; do not read the ZIP with text tools or run shell extraction merely to guess the id. A ZIP changed after preflight is rejected; request installation again to validate and confirm the current bytes.
- `webapp.publish` requires an already running WebApp with a `webUrl`; `webapp_not_running` means call `start` explicitly before retrying. Publishing registers the gateway as a `.wa` Tunnel Hub route and never starts the runtime implicitly.
- `webapp.unpublish` idempotently disables the `.wa` route without stopping the local runtime or deleting data.
- `webapp.uninstall` unpublishes, stops the runtime, closes its windows, and removes the program, persistent data, state, logs, and install record. A managed WebApp may refuse removal; any failed step stops later deletion.
- `.m` paired-mobile access is automatic and is not the same as user-triggered `.wa` public publishing.
- Do not use `web.webapp.*`, plural `web.webapps.*`, `webapp.installAndOpen`, `webapp.checkPrerequisites`, `webapp.getPublishInfo`, or `webapp.selectDirectory`; they have no aliases. Schema v5 directory selection uses `native.dialog.selectDirectory`, which is not exposed by this `desktop_webapp` allowlist.

## Example

```json
{
  "action": "webapp.install",
  "args": {
    "archivePath": "@chat/personal-workbench.zip"
  }
}
```

## Workspace Package Tooling

These three actions require a trusted Agent Platform Run in Desktop runtime. All paths are relative to that Run's Workspace; never send `workspaceRoot` or `source` in args. Standalone returns `desktop_unsupported_runtime`; Desktop WS/HTTP and WebApp calls are forbidden.

| Action | Exact args |
| --- | --- |
| `webapp.package.init` | `{projectPath, key, label, target?}` |
| `webapp.package.validate` | Exactly one of `{projectPath}` or `{archivePath}` |
| `webapp.package.build` | `{projectPath, outputPath}` |

Initialize a Manifest v2 project, edit its files, validate the directory, then build a validated ZIP at a new output path. Build refuses to overwrite an existing output. Inspect the returned validation result before installing, then pass its `outputPath` as installation `archivePath`. Only installation accepts external absolute paths; package.init/validate/build remain Workspace-bound, including their @chat/@workspace aliases. Do not call the removed `manifest.init`, `manifest.validate`, or `webapp.init` aliases.

## Export

`web.exportArtifact` accepts `{surfaceId, format}` for an authorized WebApp export provider and saves the export to Downloads. It does not provide an export bridge for ordinary webpages. Obtain the `surfaceId` with the web-control `surface_list` tool.
