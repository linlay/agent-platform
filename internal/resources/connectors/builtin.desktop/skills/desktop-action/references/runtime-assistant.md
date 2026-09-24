# Runtime Information and Assistant Chat

- `desktop.runtime.info`: no args. Returns startup-cached `{productName, version, buildTime}`.
- `desktop.runtime.diagnostics`: no args. Returns sensitive device/path/runtime/service diagnostics and a credential summary, never raw credentials. Desktop controls confirmation before collecting these details; WebApp callers are forbidden.
- `desktop.assistant.chat`: `{message}` with non-empty text. Sends the request to Desktop's configured helper agent. Use only when the task calls for that assistant; do not delegate the same request back recursively.

`desktop.assistant.image` and `desktop.assistant.image.cancel` require a local WebApp-page identity and cannot be called by Platform's `desktop_action` tool.
