# Platform catalog

Use catalog_query for discovery and catalog_manage for approved source changes, both with `{action,args}`. Only ordinary native main root Runs with builtin.platform-control mounted may call these tools. Planning permits reads and validation only. Never bypass a rejection through Bash or file tools.

## Queries

- resourceTypes: `{}` returns the supported resource types and list/get/validate/apply/delete capabilities. These are type-level capabilities; instance restrictions and approval still apply.
- list: `{resourceType, status?, limit?, cursor?}`. Types: agent, team, skill, connector, model, provider, tool, mcp. status is all (default), valid, invalid. `limit` must be a JSON integer in range 1–100; omit it to use the default 20. Use `"limit":50`, never `"limit":"50"`; string values are rejected. Returns items, nextCursor, total (after status filtering), hasMore. Follow nextCursor with the same resourceType/status until empty before claiming a complete count; total is per request, not a frozen multi-page snapshot. Invalid entries may include diagnostics.
- get: `{resourceType, resourceKey, path?}`. Editable sources return content, baseRevision, redactedPaths and editable. Providers/models/tools/MCP components and built-in connectors are read-only. Provider/MCP do not accept path.
- defaults: `{type:"general"|"coder"|"kbase"}` returns creation defaults and available models. These defaults do not supply the user's project directory; `ready` describes configured defaults, not a complete project candidate.
- validate: `{resourceType, resourceKey, path?, content, mcpUrl?, isProject?}` validates UTF-8 candidate text without saving. `isProject` is an optional boolean for agent.yml candidates. Validation does not grant write permission.

## Agent discovery

Before choosing another Agent, use `list {resourceType:"agent",status:"valid"}` when discovery is needed. Follow pagination to inspect the full directory. There is no per-caller candidate selector; the current Agent is included and internal TEAM coordinators are excluded. Agent items add key (equal to resourceKey), name, role, description, mode and invocable. Summaries use catalog metadata, not full configuration or prompts. Invalid entries retain available metadata and have invocable:false. A warning alone does not make an Agent invalid.

invocable means static agent_invoke target eligibility: invoke/internal visibility, supported child mode or ACP backend, and no agent_invoke in the target’s effective tools. It does not grant caller permission or allow self/nested invocation, and availability is checked again at execution. chat_start does not use these target restrictions; starting your own independent Chat uses the exact Agent Identity.key. Catalog listing grants neither tool permission nor Team roster membership.

Legacy contextConfig.agents and the agents context tag are ignored with one non-blocking management warning; there is no automatic candidate prompt. Discovery remains exclusive to the explicitly mounted platform-control connector. Existing management diagnostics may include Host source paths and error details; this is a management interface, not a path-redacted public summary endpoint. The new deprecation warning does not include source paths or candidate values.

## Providers and MCP discovery

Providers are loaded registry entries, including providers without models. Their allowlisted definition contains key, protocols, credentialConfigured, defaultModel, modelKeys and modelCount. No API keys, URLs, endpoint paths, headers, environment values or raw provider source are returned. credentialConfigured only reports a nonempty local credential, not successful authentication. valid for provider/model means loaded locally; invalid source files are not separately enumerated.

A connector is a package, not an MCP server: it may contain CLI, native tools, VIEW and multiple MCP components. Connector list adds hasMcp/hasCli/hasView/hasNative, editable and mcpKeys. Do not label CLI httpx/dbx or native platform/web-control packages as MCPs.

Use `list {resourceType:"mcp"}` then `get {resourceType:"mcp",resourceKey:"connectorId/component"}` for declared MCP components. A component is explicitly scoped to connector-declaration, has availability:not_checked, transport, enabled and parent connectorId. valid means the containing package loaded, not runtime protocol validation. Disabled and unmounted components remain discoverable. No commands, arguments, environment, URLs, headers or auth payloads are exposed. An invalid package prevents a complete MCP enumeration and returns an error; inspect connector status:invalid first.

For runtime MCP discovery use `platform_inspect runtimeStatus {component:"mcp"}`: this is the existing cached Agent/version-scoped sync status, limited to 100 entries; compare count before claiming completeness. A declared component may have zero or multiple Agent runtime sessions. No network probes, MCP resources/list, prompts/list or remote tool schema enumeration are added by catalog. For actual calls, the Agent must mount the connector and use its granted runtime tools. Saving a package does not establish a session.

## Writes

- apply: `{resourceType, resourceKey, path?, content, baseRevision?, preservePaths?, mcpUrl?, isProject?}`. Read the source first; pass its exact baseRevision. Omit revision only when creating a new absent resource. Content is the complete replacement text, at most 1 MiB.
- delete: `{resourceType, resourceKey, baseRevision}` deletes the resource directory or the selected packaged skill member. Team deletion is unsupported. Referenced skills/connectors/agents are protected.

Paths: Agent defaults to agent.yml and also permits SOUL.md or AGENTS.md; Team uses team.yml or existing team.yaml; Skill defaults to SKILL.md and permits relative text files; Connector permits connector.json only. No absolute paths, dot segments, symlinks, provider/model/tool/MCP writes, or modifying the calling Agent. builtin.* is immutable. Skill member keys are package/member; package.json membership changes atomically with the member. A bare package key is rejected; create whole packages through the existing package administration flow first. Case variants do not bypass protected resource checks.

runtimeConfig.env values are redacted. To retain a key, leave its value as [REDACTED] and put its exact path (for example runtimeConfig.env.API_TOKEN) in preservePaths. Never copy a placeholder without preservePaths, ask to reveal credentials, or embed connector credentials; use connector authorization.

catalog_manage apply pauses for one-time human approval in default mode; auto_approve/full_access permits server-side automatic approval. catalog_manage delete always requires human approval, including full_access. Approval displays sanitized before/after and binds caller, Run, tool invocation, content and source baseline. Changed content or revision needs a new call and review. It cannot authorize a sibling call or a whole Run. Source publication uses staging/backup and shared mutation locks. Results distinguish applied, pending (active execution lease), and invalid (published source diagnostics). A control_rolled_back error with status and executionState rolled_back means the operation failed and the previous state was restored; resolve the cause and obtain fresh approval before retrying. Inspect status before claiming success. For an unknown outcome, read the source before retrying.

## Create an Agent definition

Start with `catalog_query defaults {type:"general"|"coder"|"kbase"}` for the requested mode and preserve its current `definitionDefaults` when composing the candidate. Defaults are a partial definition: add `key`, `name`, and the user's workspace. The YAML `key` must exactly match `args.resourceKey`; `name` is the display name, not the identifier. Use `name`, not `displayName`, for the Agent's display name.

For example, the following is a complete minimal project creation request for `catalog_manage`. Replace the example model key with an available model from defaults and the workspace with the user's existing directory; merge any additional current defaults before submitting. This example uses GENERAL; use the requested mode and its defaults for CODER or KBASE.

```json
{
  "action": "apply",
  "args": {
    "resourceType": "agent",
    "resourceKey": "project-docs",
    "isProject": true,
    "content": "key: project-docs\nname: Project Docs\nmode: GENERAL\nmodelConfig:\n  modelKey: example-chat-model\nruntimeConfig:\n  workspaceRoot: /absolute/existing/project\n"
  }
}
```

Validate the same candidate with `catalog_query` and `action:"validate"` before apply. For an existing Agent, read and preserve its full definition and pass the returned `baseRevision` to apply; the minimal example is for creating an absent resource, not replacing an existing definition. If the user requests reuse by workspace, compare existing Agent definitions' workspace roots rather than inferring a match from their keys or names.

## Project Agents and workspace directories

When the user asks for an Agent for a specific project, put that project's directory in `runtimeConfig.workspaceRoot` in the agent.yml candidate, and pass `isProject:true` in both validate and apply args. Use each directory supplied by the user; an Agent's name or description does not bind its workspace. Do not copy `@root` from an unrelated Agent to fill a missing project directory. After publication, get the source and verify that its workspaceRoot matches the requested project before reporting completion.

`isProject:true` requires a specific existing absolute directory (home expansion follows the ordinary Workspace rules). Empty values, `@root`, files, missing directories and canonical filesystem/volume/share roots, including symlinks to them, are rejected before approval and checked again before publication. The flag applies only to agent.yml; it is request intent and is not written into Agent configuration. Omitted or false leaves the ordinary Agent contract unchanged. A project may use GENERAL, CODER, KBASE or the explicit ACP engine; a Git repository is not required.

`workspaceRoot: "@root"` means a general root-directory Agent with no specific project workspace. Its tools execute at the host root, but the public catalog omits `workspaceDir`, so Desktop does not put it in Projects. This project identity is independent of the Agent's mode. Use `@root` only when the user intends that general root-directory behavior.

The HTTP creation endpoint remains `POST /api/admin/agents/create`; project callers add top-level `isProject:true` and supply `definition.runtimeConfig.workspaceRoot`. Do not put isProject inside definition or agent.yml.

## Create an HTTP MCP connector

Supply a complete connector.json candidate plus mcpUrl to apply (or validate). Use type:"mcp" and auth_mode:"mcp" for standard OAuth discovery, or explicitly "no_auth" for a public endpoint. The server generates mcp.json with a main HTTP component; both files appear in the approval and contribute to its digest. mcpUrl is creation-only, requires an absent resource, and does not allow URL userinfo or fragments. Before approval, creation applies the same local validation as login: OAuth requires HTTPS except HTTP localhost, 127.0.0.1, and ::1; no_auth continues to accept HTTP(S). Authentication runs separately through the existing connector authorization UI and .well-known discovery; never put tokens into candidate files. Saving does not mean authentication or remote tool discovery succeeded.
