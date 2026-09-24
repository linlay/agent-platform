# Dedicated Desktop Settings

Use dedicated domain actions for the Desktop device name, theme, locale, and Copilot page preferences. Desktop does not expose an aggregated `desktop.setting.*` API.

For skin packages (皮肤), use `desktop.skin.*` and read [skin](skin.md). `desktop.theme.*` only selects light/dark/system.

## Actions

```text
desktop.general.deviceName [read]
desktop.theme.get [read]
desktop.theme.set [execute]
desktop.locale.get [read]
desktop.locale.set [execute]
desktop.copilot.getPagePreferences [read]
desktop.copilot.setPagePreference [execute]
```

## Arguments And Behavior

- `desktop.general.deviceName`: pass no arguments. It returns `{ "deviceName", "configuredDeviceName" }`; no public action changes the device name.
- `desktop.theme.get`: pass no arguments. It returns `{ "themeMode", "resolvedTheme" }`.
- `desktop.theme.set`: pass `{ "themeMode": "light" | "dark" | "system" }`. It persists the preference and updates the interface immediately.
- `desktop.locale.get`: pass no arguments. It returns the current locale settings.
- `desktop.locale.set`: pass `{ "locale": "zh-CN" | "en-US" }`. It persists and broadcasts the locale change.
- `desktop.copilot.getPagePreferences`: pass no arguments. It returns all Desktop Copilot page preferences and available agent options.
- `desktop.copilot.setPagePreference`: pass `{ "pageKey", "enabled"?, "agentKey"? }`. Include at least one of `enabled` or `agentKey`. Valid page keys are `controlCenter`, `market`, `help`, `agents`, `schedules`, and `skills`.

When changing a page's agent selection, first call `desktop.copilot.getPagePreferences` and select an actual key from the returned available agent options based on the user's request. Do not infer a key from an agent display name or copy one from examples or another environment. If only enabling or disabling Copilot, omit `agentKey` to preserve the existing selection.

Theme, locale, and Copilot setters use the Desktop Action confirmation flow. Getters and `desktop.general.deviceName` do not require confirmation.

Do not call `desktop.setting.getState`, `desktop.setting.validatePatch`, `desktop.setting.previewPatch`, or `desktop.setting.applyPatch`. These actions have no compatibility layer. Website, market, pet, and other settings remain in their dedicated action domains.

## Examples

Switch to dark mode:

```json
{
  "action": "desktop.theme.set",
  "args": {
    "themeMode": "dark"
  }
}
```

Switch the locale:

```json
{
  "action": "desktop.locale.set",
  "args": {
    "locale": "en-US"
  }
}
```

Enable Copilot on one page without changing its agent selection:

```json
{
  "action": "desktop.copilot.setPagePreference",
  "args": {
    "pageKey": "market",
    "enabled": true
  }
}
```
