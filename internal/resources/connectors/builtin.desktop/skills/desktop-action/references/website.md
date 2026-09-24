# Website Entries

Use these actions for registered website entries in Desktop.

## Actions

```text
desktop.site.list [read]
desktop.website.list [read]
desktop.website.add [execute]
desktop.website.update [execute]
desktop.website.remove [execute]
desktop.website.open [execute]
```

## Arguments

- `desktop.site.list` returns both registered website entries and installed WebApps.
- `desktop.website.add`: adds one website per call. Prefer `{ "input": { "label": "...", "url": "https://...", "copilotAgentKey": "..." } }`; top-level `label` and `url` are also accepted. `url` is required, and `label` is the display name field. Legacy `agentKey` input is accepted temporarily, but responses only return `copilotAgentKey`. Do not send `items`, `description`, or `icon`; batch add is not public.
- If the bridge response is `ok: true` but `result.ok: false`, the bridge worked and website validation failed. Read `result.message` and `result.issues` before retrying.
- `desktop.website.update`: pass `id` or `websiteId`, plus `input` or `patch` with `label`, `url`, and optional `copilotAgentKey`. Send an empty value to follow the Desktop default Copilot dynamically.
- `desktop.website.remove` and `desktop.website.open`: pass `id` or `websiteId`.
- Do not use `desktop.web.website.*`, plural `desktop.web.websites.*`, or `desktop.websites.*`; no old alias is provided.

When adding a website, omit `copilotAgentKey` unless the user requests a specific agent; the website will follow the Desktop default Copilot dynamically. When updating a website, omit this field to preserve its current selection. If a specific agent is requested, obtain the current available agent options with `desktop.copilot.getPagePreferences` and use the matching actual key. Do not invent keys or copy them from examples or another environment.

## Example

Add a website that follows the Desktop default Copilot:

```json
{
  "action": "desktop.website.add",
  "args": {
    "input": {
      "label": "Docs",
      "url": "https://example.com/docs"
    }
  }
}
```
