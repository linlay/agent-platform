# Control Center

Use these actions for Desktop control-center services and logs.

## Actions

```text
desktop.controlCenter.listServices [read]
desktop.controlCenter.getServiceStatus [read]
desktop.controlCenter.getServiceDetail [read]
desktop.controlCenter.getServiceLogsMeta [read]
desktop.controlCenter.readServiceLog [read]
desktop.controlCenter.openLogViewer [execute]
desktop.controlCenter.installService [execute]
desktop.controlCenter.initializeService [execute]
desktop.controlCenter.startService [execute]
desktop.controlCenter.stopService [execute]
desktop.controlCenter.restartService [execute]
```

## Arguments

- Most actions require `{ "serviceId": "..." }`.
- `desktop.controlCenter.readServiceLog`: also accepts `target` (`main` or `error`), `limitBytes`, and `beforeOffset`.
- `desktop.controlCenter.openLogViewer`: also accepts `target` and `title`.

## Example

```json
{
  "action": "desktop.controlCenter.readServiceLog",
  "args": {
    "serviceId": "agent-platform",
    "target": "main",
    "limitBytes": 65536
  }
}
```

## Open a Service

`desktop.controlCenter.openService` accepts `{serviceId}` (`id` alias), checks that the service exists, navigates to its Control Center entry, and returns `{serviceId, route}`. It does not start the service.
