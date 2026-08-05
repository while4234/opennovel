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
| 2026-08-05 | feat: redesign mobile workspace navigation | feat | Mobile workspace shell, responsive foundation/manuscript views, unit and browser tests, embedded web assets | `npm.cmd test`; `npm.cmd run build`; Chrome-backed Playwright at `430x932` and `932x430` | Phone breakpoint is `767px`; LAN startup and connectivity were intentionally not tested. |
| 2026-08-03 | chore: initialize OpenNovel baseline | init | Source baseline and project metadata | `go test ./cmd/ainovel-cli ./internal/version` passed; `go test ./...` did not finish within five minutes on Windows and was terminated without failure output | Based on `while4234/ainovel-cli` commit `2585a8f`; README rebranded for OpenNovel. |
