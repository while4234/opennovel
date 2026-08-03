# Simulation Offline Evaluation

Run the complete deterministic baseline from the repository root:

```powershell
.\scripts\run-simulation-e2e.ps1
```

The command prints a human summary, writes
`.cache/simulation-e2e/report.json`, and returns a non-zero exit code when any
invariant fails. It uses an identity-driven fake LLM and makes no network or
provider calls. Run focused unit tests with:

```powershell
.\go-project.cmd test ./internal/domain ./internal/host/sim ./internal/simulationcheck ./internal/tools
```

## What the baseline proves

- the original synthetic corpus reaches source analysis, deterministic evidence
  aggregation, fresh profile storage, role contracts, contexts, checking, and
  the exact-draft commit gate;
- normal and reinforced modes differ in selected feature budget and measurable
  obligations, while Coordinator receives status only;
- report order does not change feature IDs or evidence statistics;
- portable and Agent-visible payloads exclude local reports, paths, safety
  material, fixture proper nouns, and API-key-shaped strings;
- copy scanning catches a punctuation/whitespace variant, avoids a technique-
  only false positive, and invalidates a receipt after draft mutation;
- v1 projection is idempotent and reports legacy/portable/fresh capability
  honestly; and
- portable profile, contracts, contexts, snapshots/check reports have explicit
  byte limits, with a bounded 512-report aggregation/scanner stress case.

The report records per-invariant duration, relevant payload size, item count,
and a failure reason. Stable structure is asserted instead of timestamps or
digests, which remain intentionally dynamic.

## Limits

This is an engineering regression suite, not a subjective style score, a legal
opinion, or evidence that any provider will produce good prose. The fake model
only validates orchestration and deterministic boundaries. Real-provider A/B is
deliberately not implemented by this command: it stays default-off, and any
future entry point must require explicit authorization, disclose cost, sanitize
inputs/outputs, and remain outside CI.

## Fixture and golden updates

`testdata/simulation-e2e` is repository-original. Never copy user uploads,
production profiles, or published text into it. Preserve fixture IDs unless the
protocol itself changes. Review each expected payload/budget change separately;
there is no bulk golden rewrite command. If a failure represents an intentional
policy change, update the exact assertion and this document in the same PR,
including why the previous invariant is no longer correct.

CI runs only this offline command. No browser account, user directory, API key,
network service, or real model is required.
