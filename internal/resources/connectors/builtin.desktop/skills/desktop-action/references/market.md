# Market

Use these actions for market settings and catalog items.

For sandbox image import/export/delete, read `sandbox-images.md`.

## Market Settings

```text
desktop.market.getSettings [read]
desktop.market.validateSettings [validate]
desktop.market.previewSettingsPatch [preview]
desktop.market.applySettingsPatch [apply]
```

Arguments:

- `desktop.market.validateSettings`: pass candidate settings fields directly.
- `desktop.market.previewSettingsPatch` and `desktop.market.applySettingsPatch`: pass `{ "patch": { ... } }`.
- Use validate and preview before apply.

## Market Items

```text
desktop.market.listItems [read]
desktop.market.refresh [execute]
desktop.market.getItemDetail [read]
desktop.market.installItem [execute]
desktop.market.updateItem [execute]
desktop.market.uninstallItem [execute]
desktop.market.importSkill [execute]
```

Arguments:

- Item actions require `{ "itemId": "..." }`.
- `desktop.market.listItems` and `desktop.market.refresh` optionally accept `sections` directly or inside `options`.
- Valid sections are `plugins`, `skills`, `agents`, `sandboxImages`, `pets`, `cli`, and `websiteApps`.
- `desktop.market.importSkill` returns `interactive_file_picker_required`; it cannot complete a local-file import non-interactively through `desktop_action`.

## Examples

```json
{
  "action": "desktop.market.listItems",
  "args": {
    "sections": ["skills", "websiteApps", "sandboxImages"]
  }
}
```

```json
{
  "action": "desktop.market.installItem",
  "args": {
    "itemId": "desktop-action"
  }
}
```

## Open a Market Item

`desktop.market.openItem` accepts `{itemId}` (`id` alias) and opens that item in Desktop Market. Opening is separate from installation.
