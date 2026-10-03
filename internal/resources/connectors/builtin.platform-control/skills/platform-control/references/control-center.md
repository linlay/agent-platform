Use `desktop_service` with `{action,args}`.

# Control Center

Use these actions for Desktop control-center services and logs.

## Actions

```text
controlCenter.listServices [read]
controlCenter.getServiceStatus [read]
controlCenter.getServiceDetail [read]
controlCenter.getServiceLogsMeta [read]
controlCenter.readServiceLog [read]
controlCenter.openLogViewer [execute]
controlCenter.installService [execute]
controlCenter.initializeService [execute]
controlCenter.startService [execute]
controlCenter.stopService [execute]
controlCenter.restartService [execute]
```

## Arguments

- Most actions require `{ "serviceId": "..." }`.
- `controlCenter.readServiceLog`: also accepts `target` (`main` or `error`), `limitBytes`, and `beforeOffset`.
- `controlCenter.openLogViewer`: also accepts `target` and `title`.

## Example

```json
{
  "action": "controlCenter.readServiceLog",
  "args": {
    "serviceId": "agent-platform",
    "target": "main",
    "limitBytes": 65536
  }
}
```

## Open a Service

`controlCenter.openService` accepts `{serviceId}` (`id` alias), checks that the service exists, navigates to its Control Center entry, and returns `{serviceId, route}`. It does not start the service.
