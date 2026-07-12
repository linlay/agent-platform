# kbase-lance-engine

`kbase-lance-engine` is the local LanceDB 0.30.0 sidecar used by the Go KBASE
runtime. It binds to loopback only, writes one JSON ready handshake to stdout,
and writes operational logs to stderr.

Required process environment:

- `KBASE_LANCE_TOKEN`: per-process bearer token (at least 32 characters).
- `KBASE_LANCE_PARENT_PID`: optional Go parent PID; the sidecar exits when the
  parent disappears.
- `KBASE_LANCE_ALLOWED_ROOTS`: optional platform path-list restricting accepted
  `storageDir` values.
- `KBASE_LANCE_LISTEN_ADDR`: optional loopback address, default `127.0.0.1:0`.
  `KBASE_LANCE_LISTEN` remains an accepted compatibility alias.

The first stdout line is:

```json
{"protocolVersion":1,"engineVersion":"1.0.0","lancedbVersion":"0.30.0","listenAddress":"127.0.0.1:54321"}
```

Every HTTP request requires `Authorization: Bearer <token>`. JSON fields use
camelCase. The endpoints are:

- `GET /v1/health`
- `POST /v1/generations/create`
- `POST /v1/generations/release`
- `POST /v1/generations/import`
- `POST /v1/generations/validate`
- `POST /v1/indexes/build`
- `POST /v1/chunks/replace-file`
- `POST /v1/chunks/delete-file`
- `POST /v1/search`
- `POST /v1/read/chunk`
- `POST /v1/read/path`
- `POST /v1/stats`
- `POST /v1/optimize`
- `POST /v1/shutdown`

Import and replace accept JSON by default. They also accept Arrow IPC stream
bodies using `application/vnd.apache.arrow.stream`; metadata is then supplied
with `x-kbase-request-id`, `x-kbase-agent-key`, `x-kbase-generation-id`, and
for replace, `x-kbase-file-id`.

Generation validation returns `chunkIdDigest` and `fileIdDigest`. Each digest is
SHA-256 over sorted unique UTF-8 IDs, encoding every value as an unsigned
64-bit big-endian byte length followed by its raw bytes. `fileIdDigest` covers
only file IDs represented by at least one chunk in the Lance table.

The published LanceDB Rust API does not expose an `icu` FTS tokenizer in
v0.30.0. To preserve the KBASE contract, the sidecar segments both indexed text
and queries with ICU4X compiled data and stores the resulting tokens in a
LanceDB whitespace-tokenized FTS index.

Building the sidecar requires Rust 1.91 or newer and `protoc` on `PATH` (or the
`PROTOC`/`PROTOC_INCLUDE` variables expected by `prost-build`). These are build
requirements only; the packaged sidecar has no Rust or protoc runtime dependency.
