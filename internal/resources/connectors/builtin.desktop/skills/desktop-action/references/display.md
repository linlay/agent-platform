# Display Effects

Use `desktop.display` for a transient visual effect in the current runtime's trusted display target:

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
  "action": "desktop.display",
  "args": {
    "kind": "effect",
    "effect": "fireworks"
  }
}
```

Show snowfall for five seconds:

```json
{
  "action": "desktop.display",
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
  "action": "desktop.display",
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
- `desktop_action_target_unavailable`, `desktop_action_client_disconnected`, or `desktop_action_client_timeout`: the current run has no usable reverse connection. Report the connection limitation instead of selecting another target.
