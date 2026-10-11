---
name: kbase-control
description: Search, browse and read the knowledge library bound to this Agent, and cite the evidence it returns.
---

# Knowledge Base

Requires builtin.kbase-control mounted and an Agent with kbaseConfig.libraryId. The four tools are read-only and operate on the bound library's KBX index, never on the host filesystem.

| Tool | Use it to | Reference |
| --- | --- | --- |
| `kbase_status` | Check readiness, stale/degraded flags and file count before relying on results, or when a search unexpectedly returns nothing | [kbase_status](references/retrieval.md) |
| `kbase_search` | Find evidence by hybrid, full-text, vector or graph retrieval | [kbase_search](references/retrieval.md) |
| `kbase_files` | See which files and directories are indexed | [kbase_files](references/retrieval.md) |
| `kbase_read` | Read the exact range of a search hit, continue it, or page a source by path | [kbase_read](references/retrieval.md) |

Tool definitions only name a capability. Read the reference for a tool before calling it; methods, applicability, defaults, constraints and examples live there, not in the tool schema.

Paths are always collection/relativePath. Answer factual questions from returned evidence and cite the sources the tools publish. Scores rank results within one method and are not factual confidence. When evidence is missing, stale or degraded, say so instead of filling the gap from memory.

This connector grants retrieval only. Creating, editing or deleting libraries requires builtin.platform-control. It grants no Bash or file access to collection source directories.
