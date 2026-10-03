Use `desktop_shell` with `{action,args}`.

# Navigation

Use this action to move the Desktop shell to a route.

## Action

```text
navigate.toRoute [execute]
```

## Arguments

- Pass `{ "route": "/settings" }`.
- `path` is accepted as an alias for `route`.
- The route must start with `/`.

## Example

```json
{
  "action": "navigate.toRoute",
  "args": {
    "route": "/settings"
  }
}
```


# Help

Use this action to open Desktop Help or an allowed Help-related route.

## Action

```text
help.openTopic [execute]
```

## Arguments

- Accepts `{ "route": "/help" }`, `{ "topic": "settings" }`, or `{ "id": "control-center" }`.
- Allowed built-in routes are `/help`, `/settings`, `/market`, and `/control-center`.
- Defined agent webclient routes may also be opened.
- With no topic or route, it opens `/help`.

## Example

```json
{
  "action": "help.openTopic",
  "args": {
    "topic": "control-center"
  }
}
```


# Runtime Information and Assistant Chat

- `runtime.info`: no args. Returns startup-cached `{productName, version, buildTime}`.
- `runtime.diagnostics`: no args. Returns sensitive device/path/runtime/service diagnostics and a credential summary, never raw credentials. Desktop controls confirmation before collecting these details; WebApp callers are forbidden.
- `assistant.chat`: `{message}` with non-empty text. Sends the request to Desktop's configured helper agent. Use only when the task calls for that assistant; do not delegate the same request back recursively.

`assistant.image` and `assistant.image.cancel` require a local WebApp-page identity and cannot be called by Platform's `desktop_shell` tool.


# Display Effects

Use `display` for a transient visual effect in the current runtime's trusted display target:

- Desktop runtime routes it to the Desktop Main Window.
- Standalone runtime routes it to the originating agent-webclient page.

It is a low-risk display-only action. It does not read data, take focus, or accept ownership and window-selection arguments.

## Contract

The action requires exact `args` with only these fields:

```json
{
  "kind": "effect",
  "effect": "fireworks",
  "durationMs": 8000
}
```

- `kind` must be `effect`.
- `effect` must be `fireworks`, `snowfall`, or `nationalDay`.
- `durationMs` is optional and defaults to `8000`.
- When present, `durationMs` must be an integer from `1000` through `30000`. Invalid values return `invalid_args`; they are not clamped.
- Do not include additional fields. In particular, never include transport-owned `source` in `args`.

## Effects

- `fireworks`: colorful particle bursts.
- `snowfall`: white falling snowflakes.
- `nationalDay`: red-and-gold ribbons and star particles with the localized greeting “欢度国庆” or “Happy National Day”.

When reduced motion is preferred, the runtime uses static decoration with a fade instead of continuous particle movement.

## Function Tool Examples

Show fireworks with the default duration:

```json
{
  "action": "display",
  "args": {
    "kind": "effect",
    "effect": "fireworks"
  }
}
```

Show snowfall for five seconds:

```json
{
  "action": "display",
  "args": {
    "kind": "effect",
    "effect": "snowfall",
    "durationMs": 5000
  }
}
```

Show the National Day effect:

```json
{
  "action": "display",
  "args": {
    "kind": "effect",
    "effect": "nationalDay"
  }
}
```

The action returns immediately after starting the effect:

```json
{
  "status": "accepted",
  "kind": "effect",
  "effect": "fireworks",
  "durationMs": 8000
}
```

Only one effect runs at a time. A new accepted request replaces the current effect and restarts the timer.

## Failure Handling

- `invalid_args`: the payload has an unknown field, unsupported kind/effect, or invalid duration. Re-read this contract and retry once with the exact shape.
- `display_target_unavailable`: in Desktop runtime, the Main Window is absent, hidden, or minimized. Report the limitation; do not redirect the effect to another client or window.
- `desktop_shell_target_unavailable`, `desktop_shell_client_disconnected`, or `desktop_shell_client_timeout`: the current run has no usable reverse connection. Report the connection limitation instead of selecting another target.


`agent.open {agentKey}` and `skill.open {skillId}` navigate to details only. Use catalog_manage for approved source changes. `general.deviceName {}` reads the device name.
