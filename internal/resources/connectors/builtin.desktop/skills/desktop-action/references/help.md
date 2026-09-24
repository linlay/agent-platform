# Help

Use this action to open Desktop Help or an allowed Help-related route.

## Action

```text
desktop.help.openTopic [execute]
```

## Arguments

- Accepts `{ "route": "/help" }`, `{ "topic": "settings" }`, or `{ "id": "control-center" }`.
- Allowed built-in routes are `/help`, `/settings`, `/market`, and `/control-center`.
- Defined agent webclient routes may also be opened.
- With no topic or route, it opens `/help`.

## Example

```json
{
  "action": "desktop.help.openTopic",
  "args": {
    "topic": "control-center"
  }
}
```
