Use `desktop_settings` with `{action,args}`.

# Dedicated Desktop Settings

Use dedicated domain actions for the Desktop device name, theme, locale, and Copilot page preferences. Desktop does not expose an aggregated `setting.*` API.

For skin packages (皮肤), use `skin.*` and read [skin](settings.md). `theme.*` only selects light/dark/system.

## Actions

```text
general.deviceName is owned by desktop_shell
theme.get [read]
theme.set [execute]
locale.get [read]
locale.set [execute]
copilot.getPagePreferences [read]
copilot.setPagePreference [execute]
```

## Arguments And Behavior

- `general.deviceName`: pass no arguments. It returns `{ "deviceName", "configuredDeviceName" }`; no public action changes the device name.
- `theme.get`: pass no arguments. It returns `{ "themeMode", "resolvedTheme" }`.
- `theme.set`: pass `{ "themeMode": "light" | "dark" | "system" }`. It persists the preference and updates the interface immediately.
- `locale.get`: pass no arguments. It returns the current locale settings.
- `locale.set`: pass `{ "locale": "zh-CN" | "en-US" }`. It persists and broadcasts the locale change.
- `copilot.getPagePreferences`: pass no arguments. It returns all Desktop Copilot page preferences and available agent options.
- `copilot.setPagePreference`: pass `{ "pageKey", "enabled"?, "agentKey"? }`. Include at least one of `enabled` or `agentKey`. Valid page keys are `controlCenter`, `market`, `help`, `agents`, `schedules`, and `skills`.

When changing a page's agent selection, first call `copilot.getPagePreferences` and select an actual key from the returned available agent options based on the user's request. Do not infer a key from an agent display name or copy one from examples or another environment. If only enabling or disabling Copilot, omit `agentKey` to preserve the existing selection.

Theme, locale, and Copilot setters use Platform view review in default mode, and server-side automatic approval in auto_approve/full_access; trusted Platform calls do not repeat Desktop confirmation. Getters and `general.deviceName` do not require confirmation.

Do not call `setting.getState`, `setting.validatePatch`, `setting.previewPatch`, or `setting.applyPatch`. These actions have no compatibility layer. Website, market, pet, and other settings remain in their dedicated action domains.

## Examples

Switch to dark mode:

```json
{
  "action": "theme.set",
  "args": {
    "themeMode": "dark"
  }
}
```

Switch the locale:

```json
{
  "action": "locale.set",
  "args": {
    "locale": "en-US"
  }
}
```

Enable Copilot on one page without changing its agent selection:

```json
{
  "action": "copilot.setPagePreference",
  "args": {
    "pageKey": "market",
    "enabled": true
  }
}
```


# Desktop skins

Use `skin.*` for Desktop skins (皮肤). `theme.*` controls only light/dark/system mode. These actions require Desktop; ordinary Website/WebApp pages do not gain skin or local file access.

| Action | Kind | Args | Result |
| --- | --- | --- | --- |
| `skin.get` | read | none | `{ skinId, activeSkinId, available, customBackground: { configured, available } }` |
| `skin.list` | read | none | `{ skins: [{ skinId, name, source, version?, available }] }` |
| `skin.import` | execute | `{ filePath }` | `{ skinId, name, source, version, available }` |
| `skin.set` | execute | `{ skinId, keepBackground? }` | Same state shape as get |
| `skin.remove` | execute | `{ skinId }` | `{ skinId, activeSkinId }` |

- `filePath` must be an absolute local ZIP path on the Desktop host: `/Users/<user>/Downloads/<skin-package>.zip` on macOS or `C:\Users\<user>\Downloads\<skin-package>.zip` on Windows (replace placeholders and escape backslashes in JSON). Relative paths, aliases, URLs, file URIs, shell expansion and Windows UNC/device paths are not accepted. A container or remote Agent path is not automatically a Desktop-host path. The action does not download files.
- Import installs only; it does not apply the skin. For “导入并使用”, import, then set using the returned `skinId`. For “导入皮肤”, stop after successful import. Do not derive skin IDs from names, manifest IDs or file names.
- List includes built-in and installed summaries without image bytes, tokens or storage paths. Get distinguishes saved selection from effective fallback. `source` is `builtin` or `installed`; version is present for installed packages.
- Same manifest ID/version returns `packageExists`, never overwrites. Different versions coexist. Use list to inspect existing skins instead of repeatedly importing a duplicate.
- Applying an installed skin clears a custom background by default; pass `keepBackground: true` to retain it. Built-in skin selection preserves the custom background. Selecting `default` returns to default skin styling.
- Remove accepts installed skin IDs only. Removing the selected skin falls back to default; it does not delete the original ZIP.
- Mutations use the same Platform review policy, with no repeated Desktop confirmation for trusted Platform calls. Do not introduce an extra conversational confirmation when the target is already clear.
- Treat outer `ok: false` as failure. Errors include `invalid_args`, `skin_not_found`, `file_not_found`, `file_access_denied`, package validation/limit errors, `packageExists` and `storageFailed`. No write result contains an inner `ok` or full skin list.
- If the action is unknown, check the catalog and Platform runtime allowlist/version; update/rebuild/restart the stale component. Do not fall back to HTTP, arbitrary script execution, or direct profile edits.

```json
{
  "action": "skin.import",
  "args": { "filePath": "<absolute-desktop-host-zip-path>" }
}
```

After a successful import, use the actual returned `skinId` in `skin.set` only when applying was requested.


# Desktop Pet

Use these actions for the current public Desktop pet API.

## Actions

```text
pet.state [read]
pet.show [execute]
pet.hide [execute]
pet.list [read]
pet.import [execute]
pet.set [execute]
```

## Arguments And Behavior

- `pet.state`: returns support, visibility, settings, and appearance option state.
- `pet.list`: returns the current `appearanceId` and available `appearances`.
- `pet.show`: shows the Desktop pet.
- `pet.hide`: hides the Desktop pet.
- `pet.set`: pass `{ "appearanceId": "..." }` or `{ "id": "..." }`.

## Example

```json
{
  "action": "pet.set",
  "args": {
    "appearanceId": "default"
  }
}
```

### Import a local pet

- Call `pet.import` with `{ "filePath": "/absolute/path/panda.pet.zip" }` on the Desktop host. macOS absolute paths and Windows drive-qualified absolute paths are supported. Relative paths, aliases, URLs and Windows UNC/device paths are rejected. A remote or container path is not automatically a Desktop-host path.
- Import installs only: it does not change the selected pet, visibility or Agent binding. Success returns `{ appearanceId, displayName }`. Only call `pet.set` with the returned ID if the user also requested applying it.
- Default mode uses Platform appearance review; auto_approve/full_access permit automatic approval. Trusted Platform calls do not repeat Desktop confirmation.
- Duplicate manifest IDs return `packageExists` and are never overwritten. Read `pet.list` to inspect installed appearances. Invalid or oversized packages are rejected.
- For a requested directory, enumerate individual `.pet.zip` files and call once per package; report successes, existing packages and failures. Do not treat a collection ZIP as one pet or write into Desktop storage directly. If unavailable, update the Desktop/Platform components rather than using private IPC or UI automation.
