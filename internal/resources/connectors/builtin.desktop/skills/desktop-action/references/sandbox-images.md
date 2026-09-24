# Sandbox Images

Use these actions for public sandbox image import/export/delete flows exposed through `desktop_action`.

## Actions

```text
desktop.market.importSandboxImage [execute]
desktop.market.exportSandboxImage [execute]
desktop.market.deleteSandboxImage [execute]
```

## Arguments And Constraints

- `desktop.market.importSandboxImage` returns `interactive_file_picker_required`; it cannot complete a local-file import non-interactively through `desktop_action`.
- `desktop.market.exportSandboxImage` requires `itemId` and `targetPath`.
- `desktop.market.deleteSandboxImage` requires `itemId`.
- A sandbox-image build branch exists in bridge implementation, but it is not in the current public action catalog. Do not call `desktop.market.buildSandboxImage` through `desktop_action` unless the runtime `/actions` endpoint lists it in a future build.

## Example

```json
{
  "action": "desktop.market.exportSandboxImage",
  "args": {
    "itemId": "local-image-id",
    "targetPath": "/absolute/path/image.tar"
  }
}
```
