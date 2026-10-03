---
name: platform-control
description: Manage Platform definitions, conversations, diagnostics, and Desktop applications using the mounted platform control connector.
---

Use `{action,args}` with the appropriate tool. Read [catalog](references/catalog.md) before catalog changes and [chat](references/chat.md) before conversation changes. Desktop action arguments and confirmation remain owned by Desktop. Page/WorkPanel operations use the separate web-control connector.

Catalog and Chat tools require an ordinary native root Run. Standalone exposes only catalog_query, catalog_manage, chat_query, chat_manage and platform_inspect. Never substitute files or shell commands to bypass a rejected management operation.

catalog_manage and chat_manage delete always require explicit Platform approval; prepare the exact candidate before requesting it. A changed baseline requires a new review. Desktop tools use Desktop confirmation only. Unknown execution outcomes require reading state before retrying.

platform_inspect: runtimeStatus {component?}; securityExplain {path,access?} or {tool,action?}. These explain cached state and current policy without granting access.

Desktop references: [shell](references/shell.md), [settings](references/settings.md), [website](references/website.md), [webapp](references/webapp.md), [control-center](references/control-center.md), [market](references/market.md), [kanban](references/kanban.md). Action names omit the desktop. wire prefix. Do not supply source, workspaceRoot or confirmationSummary. Use @chat/ and @workspace/ paths where supported.
