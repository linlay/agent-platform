# Platform catalog

Use catalog_query for discovery and catalog_manage for approved source changes. Only ordinary native main root Runs with builtin.platform-control mounted may call these tools. Planning permits reads and validation only. Never bypass a rejection through Bash or file tools.

## Queries

- resourceTypes: `{}` returns the supported resource types and list/get/validate/apply/delete capabilities. These are type-level capabilities; instance restrictions and approval still apply.
- list: `{resourceType, status?, limit?, cursor?}`. Types: agent, team, skill, connector, model, provider, tool, mcp. status is all (default), valid, invalid. limit 1–100 (default 20). Returns items, nextCursor, total (after status filtering), hasMore. Follow nextCursor with the same resourceType/status until empty before claiming a complete count; total is per request, not a frozen multi-page snapshot. Invalid entries may include diagnostics.
- get: `{resourceType, resourceKey, path?}`. Editable sources return content, baseRevision, redactedPaths and editable. Providers/models/tools/MCP components and built-in connectors are read-only. Provider/MCP do not accept path.
- defaults: `{type:"general"|"coder"|"kbase"}` returns creation defaults and available models.
- validate: `{resourceType, resourceKey, path?, content, mcpUrl?}` validates UTF-8 candidate text without saving. Validation does not grant write permission.

## Providers and MCP discovery

Providers are loaded registry entries, including providers without models. Their allowlisted definition contains key, protocols, credentialConfigured, defaultModel, modelKeys and modelCount. No API keys, URLs, endpoint paths, headers, environment values or raw provider source are returned. credentialConfigured only reports a nonempty local credential, not successful authentication. valid for provider/model means loaded locally; invalid source files are not separately enumerated.

A connector is a package, not an MCP server: it may contain CLI, native tools, VIEW and multiple MCP components. Connector list adds hasMcp/hasCli/hasView/hasNative, editable and mcpKeys. Do not label CLI httpx/dbx or native platform/web-control packages as MCPs.

Use `list {resourceType:"mcp"}` then `get {resourceType:"mcp",resourceKey:"connectorId/component"}` for declared MCP components. A component is explicitly scoped to connector-declaration, has availability:not_checked, transport, enabled and parent connectorId. valid means the containing package loaded, not runtime protocol validation. Disabled and unmounted components remain discoverable. No commands, arguments, environment, URLs, headers or auth payloads are exposed. An invalid package prevents a complete MCP enumeration and returns an error; inspect connector status:invalid first.

For runtime MCP discovery use `platform_inspect runtimeStatus {component:"mcp"}`: this is the existing cached Agent/version-scoped sync status, limited to 100 entries; compare count before claiming completeness. A declared component may have zero or multiple Agent runtime sessions. No network probes, MCP resources/list, prompts/list or remote tool schema enumeration are added by catalog. For actual calls, the Agent must mount the connector and use its granted runtime tools. Saving a package does not establish a session.

## Writes

- apply: `{resourceType, resourceKey, path?, content, baseRevision?, preservePaths?, mcpUrl?}`. Read the source first; pass its exact baseRevision. Omit revision only when creating a new absent resource. Content is the complete replacement text, at most 1 MiB.
- delete: `{resourceType, resourceKey, baseRevision}` deletes the resource directory or the selected packaged skill member. Team deletion is unsupported. Referenced skills/connectors/agents are protected.

Paths: Agent defaults to agent.yml and also permits SOUL.md or AGENTS.md; Team uses team.yml or existing team.yaml; Skill defaults to SKILL.md and permits relative text files; Connector permits connector.json only. No absolute paths, dot segments, symlinks, provider/model/tool/MCP writes, or modifying the calling Agent. builtin.* is immutable. Skill member keys are package/member; package.json membership changes atomically with the member. A bare package key is rejected; create whole packages through the existing package administration flow first. Case variants do not bypass protected resource checks.

runtimeConfig.env values are redacted. To retain a key, leave its value as [REDACTED] and put its exact path (for example runtimeConfig.env.API_TOKEN) in preservePaths. Never copy a placeholder without preservePaths, ask to reveal credentials, or embed connector credentials; use connector authorization.

catalog_manage apply pauses for one-time human approval in default mode; auto_approve/full_access permits server-side automatic approval. catalog_manage delete always requires human approval, including full_access. Approval displays sanitized before/after and binds caller, Run, tool invocation, content and source baseline. Changed content or revision needs a new call and review. It cannot authorize a sibling call or a whole Run. Source publication uses staging/backup and shared mutation locks. Results distinguish applied, pending (active execution lease), and invalid (published source diagnostics). A control_rolled_back error with status and executionState rolled_back means the operation failed and the previous state was restored; resolve the cause and obtain fresh approval before retrying. Inspect status before claiming success. For an unknown outcome, read the source before retrying.

## Create an HTTP MCP connector

Supply a complete connector.json candidate plus mcpUrl to apply (or validate). Use type:"mcp" and auth_mode:"mcp" for standard OAuth discovery, or explicitly "no_auth" for a public endpoint. The server generates mcp.json with a main HTTP component; both files appear in the approval and contribute to its digest. mcpUrl is creation-only, requires an absent resource, and does not allow URL userinfo or fragments. Before approval, creation applies the same local validation as login: OAuth requires HTTPS except HTTP localhost, 127.0.0.1, and ::1; no_auth continues to accept HTTP(S). Authentication runs separately through the existing connector authorization UI and .well-known discovery; never put tokens into candidate files. Saving does not mean authentication or remote tool discovery succeeded.
