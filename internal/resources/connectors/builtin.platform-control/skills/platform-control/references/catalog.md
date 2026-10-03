# Platform catalog

Use catalog_query for discovery and catalog_manage for approved source changes. Only ordinary native main root Runs with builtin.platform-control mounted may call these tools. Planning permits reads and validation only. Never bypass a rejection through Bash or file tools.

## Queries

- list: `{resourceType, status?, limit?, cursor?}`. Types: agent, team, skill, connector, model, tool. status is all (default), valid, invalid. limit 1–100 (default 20). Follow nextCursor; invalid entries may include diagnostics.
- get: `{resourceType, resourceKey, path?}`. Editable sources return content, baseRevision, redactedPaths and editable. Models/tools and built-in connectors are read-only.
- defaults: `{type:"general"|"coder"|"kbase"}` returns creation defaults and configured templates/models.
- validate: `{resourceType, resourceKey, path?, content, mcpUrl?}` validates UTF-8 candidate text without saving. Validation does not grant write permission.

## Writes

- apply: `{resourceType, resourceKey, path?, content, baseRevision?, preservePaths?, mcpUrl?}`. Read the source first; pass its exact baseRevision. Omit revision only when creating a new absent resource. Content is the complete replacement text, at most 1 MiB.
- delete: `{resourceType, resourceKey, baseRevision}` deletes the resource directory or the selected packaged skill member. Team deletion is unsupported. Referenced skills/connectors/agents are protected.

Paths: Agent defaults to agent.yml and also permits SOUL.md or AGENTS.md; Team uses team.yml or existing team.yaml; Skill defaults to SKILL.md and permits relative text files; Connector permits connector.json only. No absolute paths, dot segments, symlinks, model/tool writes, or modifying the calling Agent. builtin.* is immutable. Skill member keys are package/member; package.json membership changes atomically with the member. A bare package key is rejected; create whole packages through the existing package administration flow first. Case variants do not bypass protected resource checks.

runtimeConfig.env values are redacted. To retain a key, leave its value as [REDACTED] and put its exact path (for example runtimeConfig.env.API_TOKEN) in preservePaths. Never copy a placeholder without preservePaths, ask to reveal credentials, or embed connector credentials; use connector authorization.

Every catalog_manage call pauses for one-time human approval even with full_access. Approval displays sanitized before/after and binds caller, Run, tool invocation, content and source baseline. Changed content or revision needs a new call and review. It cannot authorize a sibling call or a whole Run. Source publication uses staging/backup and shared mutation locks. Results distinguish applied, pending (active execution lease), invalid (published source diagnostics), and rolled_back. Inspect status before claiming success. For an unknown outcome, read the source before retrying.

## Create an HTTP MCP connector

Supply a complete connector.json candidate plus mcpUrl to apply (or validate). Use type:"mcp" and auth_mode:"mcp" for standard OAuth discovery, or explicitly "no_auth" for a public endpoint. The server generates mcp.json with a main HTTP component; both files appear in the approval and contribute to its digest. mcpUrl is creation-only, requires an absent resource, and does not allow URL userinfo or fragments. Authentication runs separately through the existing connector authorization UI and .well-known discovery; never put tokens into candidate files. Saving does not mean authentication or remote tool discovery succeeded.
