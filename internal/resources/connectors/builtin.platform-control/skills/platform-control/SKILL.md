---
name: platform-control
description: "Discover, create, and manage Platform Agents, skills, connectors, and knowledge libraries. Create and check HTTP MCP connectors from server URLs. Inspect runtime diagnostics and manage Desktop applications."
---

Use `{action,args}` with the appropriate tool. Use catalog_query resourceTypes to discover the supported resource/operation matrix. Read [catalog](references/catalog.md) before catalog discovery or changes. Desktop validates action arguments; Platform reviews routine management actions while Desktop confirms host-impacting and market operations. Page/WorkPanel operations use the separate web-control connector.

When the user asks to create, add, or configure a Platform connector, use this skill. This includes creating an HTTP MCP connector from a server URL supplied by the user. For HTTP MCP creation, read the [creation workflow](references/catalog.md#create-an-http-mcp-connector) before preparing its definition; the supplied URL is the connector's endpoint configuration.

Prepare and validate the complete candidate, submit it through catalog_manage, and verify the published result. Use platform_inspect checkConnector for an independent HTTP MCP check. Continue with the connector authorization interface when authorization is required; finish the turn and resume the check after the user completes login. Creating and testing a connector does not require mounting it to an Agent.

Catalog and knowledge library tools require an ordinary native root Run. Standalone exposes catalog_query, catalog_manage, platform_inspect, kbase_library_query and kbase_library_manage. Kanban tools belong to builtin.kanban-control. Chat and Automation tools belong to the separate builtin.task-control connector. Never substitute files or shell commands to bypass a rejected or unavailable management operation, probe local HTTP endpoints, or search .env/identity files for tokens.

Before creating or repairing an Agent with knowledge retrieval, read [knowledge libraries](references/knowledge-libraries.md). A fixed library binding is optional for every mode, including KBASE. When the user requests one, find or create the library, bind its returned libraryId, then verify the binding and library index status separately. An unbound KBASE is valid and does not need repair. workspaceRoot is not a knowledge source, and opening the first Chat does not create an index. Report missing tools only when they prevent the requested operation.

Knowledge library creation, deletion and updates changing canonical sources or filters always require human review, even in auto_approve/full_access. Only updates preserving the source grants may follow the access level's automatic approval policy. Equivalent library reuse grants no new access.

catalog_manage apply/import and routine Desktop management use Platform review in default mode and allow server-side auto approval in auto_approve/full_access. Import one Skill, Skill-package or Connector ZIP through catalog_manage import; read the [ZIP import workflow](references/catalog.md#import-a-local-zip) first. New library bindings freeze configuration revision and canonical retrieval sources, but grant no source file editing access and add no mode-specific human approval. catalog_manage delete always requires explicit human approval. Changed baselines require a new review; unknown outcomes require reading state before retrying.

platform_inspect: runtimeStatus {component?}, where component is platform/models/mcp/connectors/kbase/containerHub/memory/catalog; omit it to read all components, and an unknown name is rejected with the accepted names; securityExplain {path,access?} or {tool,action?}. These two actions explain cached state and current policy without starting remote probes. checkConnector {connectorId,component?} explicitly checks a saved HTTP MCP component without an Agent mount; it requires an ordinary native root Run. Read [connector checks](references/catalog.md#check-an-http-mcp-connector) for its results and authorization recovery.

Every project Agent, including a knowledge-base project, must have a concrete existing `runtimeConfig.workspaceRoot` and use `isProject:true` in catalog validate/apply args. Use the project directory established by the user's request; resolve a missing directory before submitting the project. Knowledge sources and Workspace are independent, but that does not make a project's Workspace optional. Read [catalog](references/catalog.md) for directory selection, project checks and post-publication verification.

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
