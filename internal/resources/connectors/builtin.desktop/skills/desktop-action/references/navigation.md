# Navigation

Use this action to move the Desktop shell to a route.

## Action

```text
desktop.navigate.toRoute [execute]
```

## Arguments

- Pass `{ "route": "/settings" }`.
- `path` is accepted as an alias for `route`.
- The route must start with `/`.

## Example

```json
{
  "action": "desktop.navigate.toRoute",
  "args": {
    "route": "/settings"
  }
}
```
