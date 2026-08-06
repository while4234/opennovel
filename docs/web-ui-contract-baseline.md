# Web UI Contract Baseline

This checklist is the compatibility gate for the frontend restructuring. A UI
move or component split may change presentation, but it must not change these
transport and lifecycle contracts without a coordinated backend change.

## HTTP boundaries

- JSON mutations use their existing method, path, request keys, and
  `content-type: application/json` behavior.
- Multipart requests let the browser set the boundary and retain these fields:
  simulation uploads and library uploads use `files`; profile imports use
  `profile`; original/adaptation imports use `source`; optional provenance uses
  `from`; continuation uploads use `files`; Codex auth uses `auth_file`.
- Persistent actions submit once with `async: true` and an `idempotency_key`,
  then poll the same path with `GET ?action_id=...` until a terminal status.
- Revision mutations preserve every concurrency guard supplied by the current
  API helper: `expected_revision`, content/structure/audit signatures, digests,
  preview IDs, and idempotency keys. Do not regenerate a key between preview,
  retry, or confirm steps when the existing flow reuses it.
- Export downloads remain binary responses. The UI reads the Blob plus
  `X-AINovel-Export-Name`, `-Chapters`, `-Bytes`, `-Skipped`,
  `X-AINovel-Audit-Status`, and `X-AINovel-Audit-Digest`. The server retains
  `Content-Type` and attachment `Content-Disposition` headers.

## Event and request lifecycle

- SSE reconnects with an `after` cursor, ignores replayed sequence numbers,
  accepts newer sparse sequences, and merges a running host event by stable
  `host_event_id`. Snapshot reconciliation happens only after a broken
  connection, an offline-to-online recovery, or a watchdog-triggered reconnect.
- Connection errors use bounded backoff. `offline` closes the source and clears
  retry timers; `online` reconciles before reconnecting; the watchdog replaces
  an open source that has stopped receiving events; cleanup removes listeners,
  timers, and the source.
- Manuscript mutations use the strict projection
  `{ manuscript_mutation: { scope, stable_id } }`. Both successful local writes
  and SSE events invalidate only the affected manuscript views.
- Project, chapter, tab, or revision switches abort superseded reads. Late
  responses and errors from an old project or selection must not replace the
  latest state or clear its busy owner.

## Regression anchors

- `src/api.test.js`: paths, payload guards, multipart fields, polling, and Blob
  metadata.
- `src/sse.test.js` and `src/events.test.js`: stale detection, backoff, sequence
  deduplication, replay, and compact snapshot merging.
- `src/manuscript/manuscript-events.test.js` and
  `src/manuscript/ManuscriptWorkspace.test.jsx`: invalidation schema, cache
  refresh, stale response isolation, and request cancellation.
- Go tests under `internal/entry/web` protect server-side multipart parsing,
  export headers, async action persistence, signatures, revisions, and
  idempotency behavior.
