# Website Apps

Use these flattened actions for local WebApp installation, runtime control, Tunnel publishing, and complete removal.

## Actions

```text
desktop.webapp.getStatus [read]
desktop.webapp.checkRuntime [validate]
desktop.webapp.start [execute]
desktop.webapp.stop [execute]
desktop.webapp.restart [execute]
desktop.webapp.open [execute]
desktop.webapp.updatePreferences [execute]
desktop.webapp.package.init [execute]
desktop.webapp.package.validate [validate]
desktop.webapp.package.build [execute]
desktop.webapp.install [execute]
desktop.webapp.uninstall [execute]
desktop.webapp.getPublishStatus [read]
desktop.webapp.publish [execute]
desktop.webapp.unpublish [execute]
```

## Arguments

- `getStatus`, `checkRuntime`, `start`, `stop`, `restart`, `open`, `getPublishStatus`, `publish`, `unpublish`, and `uninstall`: pass `webappId` or `id`.
- `desktop.webapp.getStatus` reports only local runtime state. `desktop.webapp.getPublishStatus` separately reports Tunnel readiness and route state as `{ ready, info, state, message }`; a signed-out, unconfigured, disabled, or disconnected Tunnel returns `ready=false` without making the action call fail.
- `desktop.webapp.checkRuntime` checks Java, native, container, or static launcher conditions without starting a process or gateway. Read `result.ready` and `result.issues`; a missing WebApp returns `webapp_not_found`.
- `desktop.webapp.open` starts the installed WebApp and navigates to `/webs/webapp:<id>`.
- `desktop.webapp.updatePreferences`: pass `{ "id": "...", "patch": { "label": "...", "openMode": "workspace|dialog" } }` with only the fields being changed.
- `desktop.webapp.install`: from Agent Platform pass `{ "workspaceArchivePath": "dist/package.zip", "expectedId": "optional-id" }`. It accepts only a local archive, installs or updates transactionally, and returns `{ itemId, operation, item, message }` without starting, opening, navigating, or publishing. Do not pass `itemId`; install a market item with `desktop.market.installItem`.
- `desktop.webapp.publish` requires an already running WebApp with a `webUrl`; `webapp_not_running` means call `start` explicitly before retrying. Publishing registers the gateway as a `.wa` Tunnel Hub route and never starts the runtime implicitly.
- `desktop.webapp.unpublish` idempotently disables the `.wa` route without stopping the local runtime or deleting data.
- `desktop.webapp.uninstall` unpublishes, stops the runtime, closes its windows, and removes the program, persistent data, state, logs, and install record. A managed WebApp may refuse removal; any failed step stops later deletion.
- `.m` paired-mobile access is automatic and is not the same as user-triggered `.wa` public publishing.
- Do not use `desktop.web.webapp.*`, plural `desktop.web.webapps.*`, `desktop.webapp.installAndOpen`, `desktop.webapp.checkPrerequisites`, `desktop.webapp.getPublishInfo`, or `desktop.webapp.selectDirectory`; they have no aliases. Schema v5 directory selection uses `desktop.native.dialog.selectDirectory`, which is not exposed by this `desktop_action` allowlist.

## Example

```json
{
  "action": "desktop.webapp.install",
  "args": {
    "workspaceArchivePath": "dist/demo-website-app.zip",
    "expectedId": "demo-website-app"
  }
}
```

## Workspace Package Tooling

These three actions require a trusted Agent Platform Run in Desktop runtime. All paths are relative to that Run's Workspace; never send `workspaceRoot` or `source` in args. Standalone returns `desktop_action_unsupported_runtime`; Desktop WS/HTTP and WebApp calls are forbidden.

| Action | Exact args |
| --- | --- |
| `desktop.webapp.package.init` | `{projectPath, key, label, target?}` |
| `desktop.webapp.package.validate` | Exactly one of `{projectPath}` or `{archivePath}` |
| `desktop.webapp.package.build` | `{projectPath, outputPath}` |

Initialize a Manifest v2 project, edit its files, validate the directory, then build a validated ZIP at a new output path. Build refuses to overwrite an existing output. Inspect the returned validation result before installing. Installation from Platform requires `workspaceArchivePath`; the absolute `archivePath` form belongs only to Desktop UI/local file selection. Do not call the removed `manifest.init`, `manifest.validate`, or `desktop.webapp.init` aliases.
