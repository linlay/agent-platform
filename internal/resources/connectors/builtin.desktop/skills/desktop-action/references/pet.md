# Desktop Pet

Use these actions for the current public Desktop pet API.

## Actions

```text
desktop.pet.state [read]
desktop.pet.show [execute]
desktop.pet.hide [execute]
desktop.pet.list [read]
desktop.pet.set [execute]
```

## Arguments And Behavior

- `desktop.pet.state`: returns support, visibility, settings, and appearance option state.
- `desktop.pet.list`: returns the current `appearanceId` and available `appearances`.
- `desktop.pet.show`: shows the Desktop pet.
- `desktop.pet.hide`: hides the Desktop pet.
- `desktop.pet.set`: pass `{ "appearanceId": "..." }` or `{ "id": "..." }`.

## Example

```json
{
  "action": "desktop.pet.set",
  "args": {
    "appearanceId": "default"
  }
}
```
