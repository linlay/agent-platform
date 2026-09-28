# Desktop skins

Use `desktop.skin.*` for Desktop skins (皮肤). `desktop.theme.*` controls only light/dark/system mode. These actions require Desktop; ordinary Website/WebApp pages do not gain skin or local file access.

| Action | Kind | Args | Result |
| --- | --- | --- | --- |
| `desktop.skin.get` | read | none | `{ skinId, activeSkinId, available, customBackground: { configured, available } }` |
| `desktop.skin.list` | read | none | `{ skins: [{ skinId, name, source, version?, available }] }` |
| `desktop.skin.import` | execute | `{ filePath }` | `{ skinId, name, source, version, available }` |
| `desktop.skin.set` | execute | `{ skinId, keepBackground? }` | Same state shape as get |
| `desktop.skin.remove` | execute | `{ skinId }` | `{ skinId, activeSkinId }` |

- `filePath` must be an absolute local ZIP path on the Desktop host: `/Users/.../dunhuang.skin.zip` on macOS or `C:\Users\...\dunhuang.skin.zip` on Windows (escape backslashes in JSON). Relative paths, aliases, URLs, file URIs, shell expansion and Windows UNC/device paths are not accepted. A container or remote Agent path is not automatically a Desktop-host path. The action does not download files.
- Import installs only; it does not apply the skin. For “导入并使用”, import, then set using the returned `skinId`. For “导入皮肤”, stop after successful import. Do not derive skin IDs from names, manifest IDs or file names.
- List includes built-in and installed summaries without image bytes, tokens or storage paths. Get distinguishes saved selection from effective fallback. `source` is `builtin` or `installed`; version is present for installed packages.
- Same manifest ID/version returns `packageExists`, never overwrites. Different versions coexist. Use list to inspect existing skins instead of repeatedly importing a duplicate.
- Applying an installed skin clears a custom background by default; pass `keepBackground: true` to retain it. Built-in skin selection preserves the custom background. Selecting `default` returns to default skin styling.
- Remove accepts installed skin IDs only. Removing the selected skin falls back to default; it does not delete the original ZIP.
- Mutations use the existing Desktop Action permission/confirmation flow. Do not introduce an extra conversational confirmation when the target is already clear.
- Treat outer `ok: false` as failure. Errors include `invalid_args`, `skin_not_found`, `file_not_found`, `file_access_denied`, package validation/limit errors, `packageExists` and `storageFailed`. No write result contains an inner `ok` or full skin list.
- If the action is unknown, check the catalog and Platform runtime allowlist/version; update/rebuild/restart the stale component. Do not fall back to HTTP, arbitrary script execution, or direct profile edits.

```json
{
  "action": "desktop.skin.import",
  "args": { "filePath": "/Users/linlay/Project/zenmind/zenmind-desktop/output/skin-collection/dunhuang.skin.zip" }
}
```

After a successful import, use the actual returned `skinId` in `desktop.skin.set` only when applying was requested.
