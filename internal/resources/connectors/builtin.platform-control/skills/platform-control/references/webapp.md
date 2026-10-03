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
- `webapp.install`: from Agent Platform pass `{ "workspaceArchivePath": "dist/package.zip", "expectedId": "optional-id" }`. It accepts only a local archive, installs or updates transactionally, and returns `{ itemId, operation, item, message }` without starting, opening, navigating, or publishing. Do not pass `itemId`; install a market item with `market.installItem`.
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
    "workspaceArchivePath": "dist/demo-website-app.zip",
    "expectedId": "demo-website-app"
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

Initialize a Manifest v2 project, edit its files, validate the directory, then build a validated ZIP at a new output path. Build refuses to overwrite an existing output. Inspect the returned validation result before installing. Installation from Platform requires `workspaceArchivePath`; the absolute `archivePath` form belongs only to Desktop UI/local file selection. Do not call the removed `manifest.init`, `manifest.validate`, or `webapp.init` aliases.

## Export

`web.exportArtifact` accepts `{surfaceId, format}` for an authorized WebApp export provider and saves the export to Downloads. It does not provide an export bridge for ordinary webpages. Obtain the `surfaceId` with the web-control `surface_list` tool.
