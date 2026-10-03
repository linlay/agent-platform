Use `desktop_market` with `{action,args}`.

# Market

Use these actions for market settings and catalog items.

For sandbox image import/export/delete, read `sandbox-images.md`.

## Market Settings

```text
market.getSettings [read]
market.validateSettings [validate]
market.previewSettingsPatch [preview]
market.applySettingsPatch [apply]
```

Arguments:

- `market.validateSettings`: pass candidate settings fields directly.
- `market.previewSettingsPatch` and `market.applySettingsPatch`: pass `{ "patch": { ... } }`.
- Use validate and preview before apply.

## Market Items

```text
market.listItems [read]
market.refresh [execute]
market.getItemDetail [read]
market.installItem [execute]
market.updateItem [execute]
market.uninstallItem [execute]
market.importSkill [execute]
```

Arguments:

- Item actions require `{ "itemId": "..." }`.
- `market.listItems` and `market.refresh` optionally accept `sections` directly or inside `options`.
- Valid sections are `plugins`, `skills`, `agents`, `sandboxImages`, `pets`, `cli`, and `websiteApps`.
- `market.importSkill` returns `interactive_file_picker_required`; it cannot complete a local-file import non-interactively through `desktop_market`.

## Examples

```json
{
  "action": "market.listItems",
  "args": {
    "sections": ["skills", "websiteApps", "sandboxImages"]
  }
}
```

```json
{
  "action": "market.installItem",
  "args": {
    "itemId": "platform-control"
  }
}
```

## Open a Market Item

`market.openItem` accepts `{itemId}` (`id` alias) and opens that item in Desktop Market. Opening is separate from installation.


# Sandbox Images

Use these actions for public sandbox image import/export/delete flows exposed through `desktop_market`.

## Actions

```text
market.importSandboxImage [execute]
market.exportSandboxImage [execute]
market.deleteSandboxImage [execute]
```

## Arguments And Constraints

- `market.importSandboxImage` returns `interactive_file_picker_required`; it cannot complete a local-file import non-interactively through `desktop_market`.
- `market.exportSandboxImage` requires `itemId` and `targetPath`.
- `market.deleteSandboxImage` requires `itemId`.
- A sandbox-image build branch exists in bridge implementation, but it is not in the current public action catalog. Do not call `market.buildSandboxImage` through `desktop_market` unless the runtime `/actions` endpoint lists it in a future build.

## Example

```json
{
  "action": "market.exportSandboxImage",
  "args": {
    "itemId": "local-image-id",
    "targetPath": "/absolute/path/image.tar"
  }
}
```
