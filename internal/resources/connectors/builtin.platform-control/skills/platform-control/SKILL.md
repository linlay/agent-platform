---
name: platform-control
description: Manage Platform definitions, diagnostics, and Desktop applications using the mounted platform control connector.
---

Use `{action,args}` with the appropriate tool. Use catalog_query resourceTypes to discover the supported resource/operation matrix. Read [catalog](references/catalog.md) before catalog discovery or changes. Desktop validates action arguments; Platform reviews routine management actions while Desktop confirms host-impacting and market operations. Page/WorkPanel operations use the separate web-control connector.

Catalog tools require an ordinary native root Run. Standalone exposes only catalog_query, catalog_manage and platform_inspect. Kanban tools belong to builtin.kanban-control. Chat and Automation tools belong to the separate builtin.task-control connector. Never substitute files or shell commands to bypass a rejected management operation.

catalog_manage apply and routine Desktop management use Platform review in default mode and allow server-side auto approval in auto_approve/full_access. catalog_manage delete always require explicit human approval. Prepare the exact candidate; a changed baseline requires a new review. Market resource management, service lifecycle, and WebApp install/uninstall/publish keep Desktop confirmation. Unknown execution outcomes require reading state before retrying.

platform_inspect: runtimeStatus {component?}, where component is platform/models/mcp/connectors/kbase/containerHub/memory/catalog; omit it to read all components, and an unknown name is rejected with the accepted names; securityExplain {path,access?} or {tool,action?}. These explain cached state and current policy without granting access or starting remote probes.

For project Agent creation, use each user-supplied project directory as `runtimeConfig.workspaceRoot` in the Agent definition and set `isProject:true` in catalog validate/apply args. `@root` denotes a general root-directory Agent with no specific project workspace; it does not appear in Desktop Projects. Read [catalog](references/catalog.md) for the project checks and post-publication verification.

Tool definitions only name a capability. Read the reference for a tool before calling it; action arguments, types, constraints and examples live there, not in the tool schema.

| Tool | Reference |
| --- | --- |
| catalog_query, catalog_manage | [catalog](references/catalog.md) |
| platform_inspect | the platform_inspect paragraph above |
| desktop_shell | [shell](references/shell.md) |
| desktop_settings | [settings](references/settings.md) |
| desktop_site | [website](references/website.md) |
| desktop_webapp | [webapp](references/webapp.md) |
| desktop_service | [control-center](references/control-center.md) |
| desktop_market | [market](references/market.md) |

Action names omit the desktop. wire prefix. For Desktop action args, do not supply source, workspaceRoot or confirmationSummary. This restriction does not apply to runtimeConfig.workspaceRoot inside an Agent definition. Use @chat/ and @workspace/ paths where supported.
