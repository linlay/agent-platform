# Retrieval

kbase_list discovers all libraries. The other four tools require an explicit libraryId and may read any existing readable library. mustUseKbases specifies the libraries required for the current Run; it is not an allowlist. Every tool rejects fields it does not declare, before the library is read. Booleans are native JSON values.

## kbase_list

Takes no arguments: {}. Returns libraries with id, name, description, collections (name and description), readable, indexing, degraded and an optional unavailableReason. It exposes no source directories. Discover IDs before calling retrieval tools; a fixed Agent binding is unnecessary. Libraries with a readable committed index remain readable while indexing or degraded.

## kbase_search

- libraryId (required string): exact library ID from kbase_list or the Run context. Never omitted and never inferred from chunkId.

Searches the specified library. Filters apply before recall and can never widen the library scope. Results include evidence for verification. Offset or cursor pagination is unavailable; raise limit or narrow the query instead.

Required:

- query (string): the search query.

Method:

- method (string, default query):
  - query combines available lexical, vector and graph recall and reports degradation explicitly.
  - search is local full-text BM25 for exact terms and identifiers, with no model calls.
  - vsearch is strict vector similarity.
  - gsearch retrieves entity relationships and source evidence from an already-built complete graph. Platform refresh does not build graphs.
  - vsearch and gsearch require their indexes and never silently fall back; switch method explicitly when one is unavailable.

Scope, supported by all four methods:

- collections (string array, at least one): explicit collection names within the specified library. Omit to use the defaultQuery collections; an explicit selection may include defaultQuery=false collections.
- pathPrefix (string): collection-prefixed directory prefix such as docs/. Case-sensitive and respects directory boundaries.
- pathGlob (string): KBX glob matched against collection/relativePath. Supports *, ? and **.
- type (string): case-insensitive final file extension such as md or .pdf.
- filter (string): KBX filter expression or JSON string, ANDed with all path filters and the library include/exclude policy. Examples: project = payments and owner exists; sys.size >= 1024; (ext = md or ext = pdf) and not path ^= deprecated. Typed metadata fields must already exist in the index.

Ranking and budget, for query/search/vsearch only (not gsearch):

- limit (integer 1-50): maximum chunks returned. Defaults to the library retrieval topK with explicit Agent overrides. Also accepted by gsearch.
- candidateLimit (integer 1-2000): candidate budget, at least the effective limit and no greater than the merged library/Agent candidateMax ceiling (raised to the effective limit if needed). Omit to use the merged budget.
- minScore (number): minimum KBX ranking score. Scores are method-dependent and are not factual confidence.
- exclude (string array): hard lexical document exclusions. Each entry is a KBX lexical expression; any matching document is excluded. Not a path glob.
- intent (string): positive context hint. Does not replace hard exclusions or filters.
- recencyWeight (number 0-1): freshness weight; 0 preserves relevance-only ranking.
- recencyHalfLifeDays (number > 0): freshness half-life in days, default 90.

Model-backed options:

- rerank (boolean): query only. Overrides the merged library/Agent rerank setting. true requires a configured library reranker; false disables it.
- queryExpansion (boolean): query and vsearch only. Overrides the merged expansion setting. true requires a configured library queryExpansion model; false disables it.
- noGraph (boolean): query only. true restricts hybrid retrieval to lexical and vector channels; omitted or false allows recall from an available graph.

Graph options, gsearch only:

- entities (string array): stable entity keys or unique exact entity names/aliases. Omit to link entities from the query.
- relations (string array): allowed graph schema predicate IDs such as depends_on. Omit to use all available relations.
- direction (string): auto (default), out, in or both, relative to the linked entities.
- maxHops (integer 1-3): maximum relationship hops, default 2. Returned graph explanations preserve nodes, edges and original evidence.

Example: {"libraryId":"product-docs", "query": "release acceptance checklist", "method": "search", "collections": ["docs"], "type": "md", "limit": 10}

## kbase_files

- libraryId (required string): exact library ID from kbase_list or the Run context. Never omitted and never inferred from chunkId.

Lists files and compact directory tree entries from the KBX index. It reads indexed metadata only, not the host filesystem.

- mode (string, default files): files returns a flat path list; tree returns compact directory and file entries.
- path (string): collection-prefixed directory or path prefix. Defaults to the library root.
- pattern (string): glob matched relative to path, default **. Supports *, ? and **.
- status (string): active only; KBX exposes active indexed files.
- type (string): file extension filter such as md or .pdf.
- depth (integer): maximum tree depth for mode=tree. Omitted, 0 or negative uses 2; values above 8 are capped at 8.
- headLimit (integer): maximum entries returned after offset. Omitted or negative uses 100; 0 means unlimited.
- offset (integer): entries to skip before applying headLimit. Omitted or negative skips nothing; an offset past the end returns no entries.

Example: {"libraryId":"product-docs", "mode": "tree", "path": "docs/", "depth": 3}

## kbase_read

- libraryId (required string): exact library ID from kbase_list or the Run context. Never omitted and never inferred from chunkId.

Reads an exact evidence range, or indexed extracted text by source path. Line numbers refer to extracted text, not the original file layout.

- chunkId (string): content-addressed chunkId or evidenceId returned by kbase_search, including a graph edge evidenceId, or nextEvidence from a previous read. Returns that exact range. A non-zero offset or limit alongside chunkId is rejected.
- path (string): collection-prefixed source path, used instead of chunkId for line pagination.
- offset (integer): one-based source line offset when reading by path. Omitted or 0 starts at line 1; a negative value is rejected.
- limit (integer): maximum source lines when reading by path. Omitted, 0 or negative reads 80 lines; values above 2000 are capped at 2000. Continue with nextEvidence or a later offset when hasMore is true.

Examples: {"libraryId":"product-docs", "chunkId": "sha256:<64 hex>#bytes=0-3600"} or {"libraryId":"product-docs", "path": "docs/release.md", "offset": 1, "limit": 200}

## kbase_status

- libraryId (required string): exact library ID from kbase_list or the Run context. Never omitted and never inferred from chunkId.

Example: {"libraryId":"product-docs"}. Returns the specified library ID, KBX readiness, automatic maintenance state, stale/degraded flags and file count. An unavailable chunk count is omitted and marked chunksKnown=false.

Agent retrieval overrides apply only to its fixed bound library. Other libraries use their own defaults plus this call's options. Run selections add no source directory read/write permissions. Across turns, nextEvidence remains readable by explicit libraryId while that library and its source scope remain readable.
