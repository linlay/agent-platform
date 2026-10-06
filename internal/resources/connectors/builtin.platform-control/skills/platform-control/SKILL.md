---
name: platform-control
description: Manage Platform definitions, conversations, diagnostics, and Desktop applications using the mounted platform control connector.
---

Use `{action,args}` with the appropriate tool. Use catalog_query resourceTypes to discover the supported resource/operation matrix. Read [automation](references/automation.md) before managing schedules. Read [catalog](references/catalog.md) before catalog discovery or changes and [chat](references/chat.md) before conversation changes. Desktop validates action arguments; Platform reviews routine management actions while Desktop confirms host-impacting and market operations. Page/WorkPanel operations use the separate web-control connector.

Catalog, Chat and Automation tools require an ordinary native root Run. Standalone exposes only catalog_query, catalog_manage, chat_query, chat_manage, automation_query, automation_manage and platform_inspect. Never substitute files or shell commands to bypass a rejected management operation.

catalog_manage apply and routine Desktop management use Platform review in default mode and allow server-side auto approval in auto_approve/full_access. catalog_manage delete, chat_manage delete and automation_manage delete always require explicit human approval. Prepare the exact candidate; a changed baseline requires a new review. Market resource management, service lifecycle, and WebApp install/uninstall/publish keep Desktop confirmation. Unknown execution outcomes require reading state before retrying.

platform_inspect: runtimeStatus {component?}; securityExplain {path,access?} or {tool,action?}. These explain cached state and current policy without granting access.

For project Agent creation, use each user-supplied project directory as `runtimeConfig.workspaceRoot` in the Agent definition and set `isProject:true` in catalog validate/apply args. `@root` denotes a general root-directory Agent with no specific project workspace; it does not appear in Desktop Projects. Read [catalog](references/catalog.md) for the project checks and post-publication verification.

Desktop references: [shell](references/shell.md), [settings](references/settings.md), [website](references/website.md), [webapp](references/webapp.md), [control-center](references/control-center.md), [market](references/market.md), [kanban](references/kanban.md). Action names omit the desktop. wire prefix. For Desktop action args, do not supply source, workspaceRoot or confirmationSummary. This restriction does not apply to runtimeConfig.workspaceRoot inside an Agent definition. Use @chat/ and @workspace/ paths where supported.

Automation creation, updates, enable/pause and manual trigger use Platform review in default mode and permit auto approval in auto_approve/full_access. Saved query.accessLevel controls future runs independently of the calling chat.
