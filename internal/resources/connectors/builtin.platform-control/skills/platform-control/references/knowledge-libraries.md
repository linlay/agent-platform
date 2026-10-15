# Knowledge libraries

These tools belong to builtin.platform-control and use `{action,args}`. They operate on platform library definitions through the library service; they do not call HTTP or need a token. Only ordinary native root Runs with this connector mounted may use them. Planning permits query/validate, never management. `kbase_library_query status` describes a managed library's lifecycle; `kbase_status` describes the current Agent's bound retrieval capability. They are separate checks.

## Query actions

- list: `{limit?,offset?}`; limit is an integer 1–100 (default 20), offset is a nonnegative integer (default 0). Follow nextOffset while hasMore; total is the current count, not a frozen snapshot. items include invalid definitions and their diagnostics.
- get: `{libraryId}` returns the definition, collections, baseRevision and lifecycle fields. Verify the returned definition rather than treating a diagnostic record as a valid library.
- status: `{libraryId}` returns state, error, indexedAt, degraded and management fields even before the first index finishes. statistics are attached when a committed index can be read; statsAvailable:false or statsError never erases lifecycle state. It does not start indexing.
- validate: `{definition}` checks a proposed creation without writing or granting source access. Returns valid/diagnostics, the canonical candidate and whether an equivalent library would be reused. Validation cannot guarantee a later publication because directories and configuration can change.

## Manage actions

- create: `{definition,requestId?}`. definition requires name and either sourcePath (one collection named workspace) or collections, never both. It also accepts description, chunk, textEncoding, models and retrieval. Each collection requires name and sourcePath, and accepts description, include, exclude, chunk and defaultQuery. Source directories must already exist on the Platform host. Paths are canonicalized. Library definitions do not grant file/Bash access to source directories. The old editable field is rejected. include/exclude control indexing/retrieval; Workspace and hostAccess/AccessPolicy control file access. Omitted filters use platform defaults; use validate to inspect the effective scope. Boolean fields accept JSON booleans and exact strings "true"/"false".
- update: `{libraryId,baseRevision,patch,requestId?}`. Read get first and pass the exact baseRevision. patch merges objects, preserves omitted fields, and replaces arrays and scalar fields. null removes an optional setting; for example `{"models":{"reranker":null}}` removes only the reranker and preserves sibling model settings. Removing required fields such as name or collections is rejected. Changing collections requires supplying the complete desired collection array. sourcePath replaces the single-source collection set and cannot accompany collections. All proposed source directories must be online for revalidation. A malformed definition requires an explicit replacement name and collections/sourcePath; review all replacement settings. Never reconstruct a valid existing library from a minimal example: preserve descriptions, filters and settings.
- delete: `{libraryId,baseRevision,requestId?}`. Read get first. Referenced libraries cannot be deleted. This deletes the managed definition and index, never the source directory.

baseRevision is a content digest of library.yml; indexing updates do not change it. Revision conflicts require get and a new reviewed request. Source normalization compares canonical directories, filters and configuration, not just names. An equivalent create returns the existing real library ID with reused:true, without a write. Different filters or configuration do not reuse it. Concurrent creates are rechecked under the service management lock.

requestId is optional and scoped to the caller/Agent/Run; omit it to use the current tool invocation ID. Within the same process and Run, repeat exactly the same requestId and arguments after an interrupted response. Completed receipts return the original result without another mutation; they live only in process memory. Restarting cancels unfinished Runs and discards these receipts. Reusing an ID with different arguments while its receipt exists is rejected. An unknown/started receipt remains until the process ends and is not replayed: query the actual state and report the uncertainty before another request. Separate Runs cannot share a requestId receipt.

Busy libraries are rejected during preparation before consuming an approval. If indexing starts later, or publication rejects a stale revision or changed canonical source before any write, the started receipt is removed and the real error is returned. The same requestId can then be retried with a fresh approval and, for a revision conflict, the newly read baseRevision. Failures after a write may have begun retain an unknown outcome and require inspection. Legacy on-disk receipts are no longer loaded; their presence does not prove that a mutation had no effect.

New libraries and deletions always require human approval, including auto_approve/full_access. Updates changing canonical source paths, effective include/exclude also require human approval at every access level; narrowing scope still counts as a change. Updates preserving these grants use Platform review in default mode and permit server-side approval in auto_approve/full_access with the exact candidate and audit. Equivalent create reuse grants no new access and requires no mutation approval. Approval shows and binds canonical source paths, effective include/exclude, plus before/after and revision. Changed scope requires a fresh review. StateDir and protected identity files, their descendants and sources containing them are hard rejected at every access level before approval, including when loading existing definitions; exclude patterns do not waive this rule. Source and revision are checked again immediately before publication. A successful validation or a prior library approval never authorizes a new scope.

Library reuse and Agent binding have separate checks. New or changed bindings follow ordinary catalog approval policy at every mode and freeze the library revision and canonical retrieval sources for revalidation. Bindings grant no file editing access, and changing mode with an unchanged libraryId adds no library grant. Source or revision changes invalidate pending binding approval.

## Create and bind a knowledge Agent

1. List libraries and get possible matches. Use the user's explicit source directories and required filters/configuration to find an equivalent library. A matching display name alone is insufficient. Reuse only a valid definition.
2. Validate then create as needed. Preserve the returned libraryId; never derive it from an Agent key, directory name, a local .kbx index or a guessed ID.
3. Read catalog_query defaults for type:kbase, merge the current defaults, and supply key/name/model plus `kbaseConfig.libraryId` when the user wants a fixed library. The binding is optional for every mode, including KBASE; if supplied it must reference a valid library. For a knowledge-base project, also supply a concrete existing `runtimeConfig.workspaceRoot` and pass `isProject:true` in both catalog validate/apply args. Resolve its project directory before submitting; Workspace independence from the knowledge source does not make it optional for projects. Read [catalog](catalog.md) before validate/apply. Reusing an Agent requires checking its actual binding and, for a project, its Workspace, not just its name.
4. Read the published Agent definition and verify the binding equals the returned ID; for a project, also verify workspaceRoot matches the requested directory. Query library status until a definite index state is available within the task's time budget. Building/unindexed/error are real outcomes, not "index ready". Report state/error/degraded/indexedAt accurately. The library workflow requires library created/reused, Agent bound, and index state verified; project completion additionally requires its Workspace. To claim retrieval readiness, require a readable committed index; degraded means committed text may be readable while embedding is incomplete.

Example library creation (replace the directory with the user's actual existing source):

```json
{"action":"create","args":{"definition":{"name":"Project documents","collections":[{"name":"docs","sourcePath":"/absolute/existing/documents"}]}}}
```

The corresponding Agent YAML uses the actual ID returned by that call:

```yaml
key: project-docs
name: Project documents
mode: KBASE
modelConfig:
  modelKey: example-chat-model
runtimeConfig:
  workspaceRoot: /absolute/existing/workspace
kbaseConfig:
  libraryId: ID_RETURNED_BY_PLATFORM
```

Replace the model with an available key and merge defaults before submitting this example. For project creation, include `isProject:true` in catalog validate/apply args; it belongs outside the YAML. Tool availability comes from declared tools and mode presets; library binding does not inject tools. Preserve the user's requested workspace rather than assuming it is the source.

An unbound Agent of any mode, including KBASE, is valid and needs no repair. Bind an existing Agent only when the user asks for a fixed library: read its full source, create/reuse the explicitly requested source library, add libraryId while retaining all other configuration, validate/apply using the source revision, and recheck binding/status.

The service automatically watches, reconciles and indexes libraries after creation. There is no separate model-facing trigger-index tool, and the first Chat does not create the library or index. If the management tools are absent, report the missing capability and unfinished checks. Never use curl/local interfaces, file mutations or token discovery as a fallback.
