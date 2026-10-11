---
name: platform-control
description: "Discover, create, and manage Platform Agents, skills, connectors, and knowledge libraries. Create HTTP MCP connectors from server URLs. Inspect runtime diagnostics and manage Desktop applications."
---

Use `{action,args}` with the appropriate tool. Use catalog_query resourceTypes to discover the supported resource/operation matrix. Read [catalog](references/catalog.md) before catalog discovery or changes. Desktop validates action arguments; Platform reviews routine management actions while Desktop confirms host-impacting and market operations. Page/WorkPanel operations use the separate web-control connector.

When the user asks to create, add, or configure a Platform connector, use this skill. This includes creating an HTTP MCP connector from a server URL supplied by the user. For HTTP MCP creation, read the [creation workflow](references/catalog.md#create-an-http-mcp-connector) before preparing its definition; the supplied URL is the connector's endpoint configuration.

Prepare and validate the complete candidate, submit it through catalog_manage, and verify the published result. Continue with the connector authorization interface when authorization is required.

Catalog and knowledge library tools require an ordinary native root Run. Standalone exposes catalog_query, catalog_manage, platform_inspect, kbase_library_query and kbase_library_manage. Kanban tools belong to builtin.kanban-control. Chat and Automation tools belong to the separate builtin.task-control connector. Never substitute files or shell commands to bypass a rejected or unavailable management operation, probe local HTTP endpoints, or search .env/identity files for tokens.

Before creating or repairing a knowledge Agent, read [knowledge libraries](references/knowledge-libraries.md). Find or create the library first, bind its returned libraryId, then verify the Agent binding and library index status separately. workspaceRoot is not a knowledge source, and opening the first Chat does not create an index. Missing tools mean the workflow is incomplete; report the missing capability.

Knowledge library creation, deletion and updates changing canonical sources, filters or editable always require human review, even in auto_approve/full_access. Only updates preserving the source grants may follow the access level's automatic approval policy. Equivalent library reuse grants no new access.

catalog_manage apply and routine Desktop management use Platform review in default mode and allow server-side auto approval in auto_approve/full_access. An Agent's new or changed library binding to a library with editable collections requires human review at every access level. Enabling native KBASE mode for an existing binding requires human review only when the library has editable collections; read-only bindings and mode activation follow the ordinary catalog approval policy. Library configuration revision and canonical grants are bound to the binding review. catalog_manage delete always requires explicit human approval. Prepare the exact candidate; a changed baseline requires a new review. Market resource management, service lifecycle, and WebApp install/uninstall/publish keep Desktop confirmation. Unknown execution outcomes require reading state before retrying.

platform_inspect: runtimeStatus {component?}, where component is platform/models/mcp/connectors/kbase/containerHub/memory/catalog; omit it to read all components, and an unknown name is rejected with the accepted names; securityExplain {path,access?} or {tool,action?}. These explain cached state and current policy without granting access or starting remote probes.

For project Agent creation, use each user-supplied project directory as `runtimeConfig.workspaceRoot` in the Agent definition and set `isProject:true` in catalog validate/apply args. `@root` denotes a general root-directory Agent with no specific project workspace; it does not appear in Desktop Projects. Read [catalog](references/catalog.md) for the project checks and post-publication verification.

Read [configuration maintenance](references/configuration.md) for explicitly requested Provider/Model YAML and external connector component source changes. Catalog read-only discovery does not grant source writes; this workflow requires independently available file tools and path permissions.

Tool definitions only name a capability. Read the reference for a tool before calling it; action arguments, types, constraints and examples live there, not in the tool schema.

| Tool | Reference |
| --- | --- |
| catalog_query, catalog_manage | [catalog](references/catalog.md) |
| kbase_library_query, kbase_library_manage | [knowledge libraries](references/knowledge-libraries.md) |
| platform_inspect | the platform_inspect paragraph above |
| desktop_shell | [shell](references/shell.md) |
| desktop_settings | [settings](references/settings.md) |
| desktop_site | [website](references/website.md) |
| desktop_webapp | [webapp](references/webapp.md) |
| desktop_service | [control-center](references/control-center.md) |
| desktop_market | [market](references/market.md) |

Action names omit the desktop. wire prefix. For Desktop action args, do not supply source, workspaceRoot or confirmationSummary. This restriction does not apply to runtimeConfig.workspaceRoot inside an Agent definition. Use @chat/ and @workspace/ paths where supported.
