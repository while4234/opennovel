# Production model-call inventory

Every production content call is budgeted against the exact compiled provider input before invocation. For agentcore calls this is the serialized messages plus tool names, descriptions, and schemas; for structured host calls it is the canonical system and user payload. The ceiling is 60 KiB per attempt; adaptation source selectors remain capped at 10,000 units. Each retry is a distinct metadata-only diagnostic with terminal status, actual provider usage when available, output rune/token estimates, and output signature. Prompts, source text, prose, raw responses, paths, and credentials are never persisted.

| Boundary | Diagnostic owner | Batching |
|---|---|---|
| manuscript plan/write/audit and contract roles | `internal/host/manuscript_model.go` | stable chapter segment / audit role |
| co-create and suggestion judge | `internal/host/cocreate.go` | attempt |
| chapter-outline and continuation planning | corresponding host planner | attempt |
| one-line expansion | `internal/host/expansion_model.go` | signed single-chapter bundle |
| adaptation planning | `internal/host/adapt/runner.go` | stage batch and attempt |
| adaptation semantic audit | `internal/host/adapt/semantic_audit.go` | bounded map/reduce window |
| imported-book analysis/foundation | `internal/host/imp` | chapter map plus bounded partial reduce |
| simulation source/profile synthesis | `internal/host/sim/retry.go` | source or <=60 KiB rolling merge batch |
| user-rule normalization | `internal/userrules/normalize.go` | source and retry attempt |
| coordinator main agent | `internal/agents/model_boundary.go` (`agent_coordinator`) | actual agentcore provider attempt after context compaction |
| short/long architect main agents | `internal/agents/model_boundary.go` (`agent_architect_short`, `agent_architect_long`) | actual subagent provider attempt after context compaction |
| writer/editor main agents | `internal/agents/model_boundary.go` (`agent_writer`, `agent_editor`) | actual subagent provider attempt after context compaction |
| agent context summary | `internal/agents/context_manager.go` | explicit project Store; summary retry/stream attempt; main-agent decorator is unwrapped to prevent duplicate ownership |

The normal agent context engine is the bounded summary/compaction path for whole-book history; if its exact post-compaction messages and tools still exceed 60 KiB, the model boundary rejects the attempt before provider I/O. The only direct `Generate`/`GenerateStream` sites excluded from owning another diagnostic are provider/failover/decorator plumbing in `internal/bootstrap`, `internal/globalprompt`, and `internal/agents/toolcall_repair_model.go`; those functions forward the same logical attempt and recording them again would create duplicate terminal records. `internal/host/model_probe.go` is an explicit operator connectivity test, not a production content workflow. `semanticAuditUsageModel` only adds billing attribution; the adaptation semantic map/reduce boundary owns the terminal content diagnostic. `internal/modeldiag/inventory_test.go` fails whenever a new direct call site or an unwrapped coordinator/architect/writer/editor route is introduced without updating this inventory.
