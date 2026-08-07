# Git History

## Repository Policy

- Local Git is used for rollback, review, and bug investigation.
- Secret files such as `.env`, keys, and credential files are intentionally ignored.
- This project is published to the configured GitHub remote after validated development rounds.

## Quick Commands

- Status: `git status --short`
- Recent history: `git log --oneline --decorate -n 20`
- Inspect a commit: `git show <hash>`
- Restore a file from a commit: `git restore --source <hash> -- <path>`

## Change Log

| Date | Commit message | Type | Files | Validation | Notes |
| --- | --- | --- | --- | --- | --- |
| 2026-08-07 | feat: refresh runtime writing styles | feat | Runtime style source, Web catalog/validation/Host loading, refresh tests, style docs, and `suspense_sex.md` | Assets package and focused Web style tests passed; related `go vet` passed; restart succeeded; live add/remove probe refreshed without restart | Full Web package still has unrelated baseline failures in co-create/adaptation/library/session tests. |
| 2026-08-06 | fix: paginate architect planning review context | fix | Architect planning review context budgeting, pagination, cursor continuation, and focused regressions | Focused tools, Host, agents, and assets tests passed; real 30,086-byte review content paginated into 10,690 / 12,382 / 11,671-byte pages; compiled rounds remained below 98,304 bytes; deployed on port 9999 with HTTP 200 | The latest cursor fix is deployed, but live model validation was not repeated because the user has no available model tokens. |
| 2026-08-06 | feat: rebuild OpenNovel web workspace | feat | Routed application shell, project/settings/knowledge/workspace pages, six-family global prompts, Foundation Character recovery and bounded context pagination, tests, docs, and embedded web assets | Vitest 60 files / 478 tests; Vite 1827-module production build; focused Go and `go vet` gates; external-9999 Chrome matrix 26 passed / 49 conditional skipped; `git diff --check` | Prepared for final commit after independent review. The real `樱牢` flow resumed once, read two bounded Character pages, passed review, and stopped at `waiting_confirmation`. |
| 2026-08-05 | feat: redesign mobile workspace navigation | feat | Mobile workspace shell, responsive foundation/manuscript views, unit and browser tests, embedded web assets | `npm.cmd test`; `npm.cmd run build`; Chrome-backed Playwright at `430x932` and `932x430` | Phone breakpoint is `767px`; LAN startup and connectivity were intentionally not tested. |
| 2026-08-03 | chore: initialize OpenNovel baseline | init | Source baseline and project metadata | `go test ./cmd/ainovel-cli ./internal/version` passed; `go test ./...` did not finish within five minutes on Windows and was terminated without failure output | Based on `while4234/ainovel-cli` commit `2585a8f`; README rebranded for OpenNovel. |
