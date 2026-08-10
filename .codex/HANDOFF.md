# Project Handoff

Last updated: 2026-08-10 15:30 +08:00
Project root: D:\OpenNovel
Branch: main
Status: in_progress

## Current Goal
Synchronize the local runtime-style feature with the latest GitHub `main`, preserve the D-drive LAN deployment notes, validate the merged tree, and push it to GitHub.

## Current Position
GitHub commit `6d0597c7` and local feature commit `8867c0b0` have been merged. Their only textual conflict was this handoff file; both the deployment state and runtime-style implementation have been retained. Validation and push remain.

## Recent Actions
- Fetched `github/main` and confirmed one local and one remote commit had diverged from `54a3a777`.
- Merged the remote D-drive LAN deployment documentation into local `main`.
- Resolved `.codex/HANDOFF.md` by preserving the current facts from both branches.
- Retained the remote `GIT_NOTES.md` deployment update without modification.
- Retained local runtime discovery for repository `assets/styles/*.md`, including `suspense_sex.md` and regression tests.

## Changed / Relevant Files
- `.codex/HANDOFF.md`: resolved merge conflict and records the combined state.
- `GIT_NOTES.md`: upstream D-drive LAN deployment notes.
- `assets/load.go`: runtime plus embedded style source.
- `assets/load_test.go`: runtime discovery and override tests.
- `assets/styles/suspense_sex.md`: repository writing style discovered at runtime.
- `internal/entry/web/server.go`: repository style-directory wiring.
- `internal/entry/web/styles.go`: live catalog and validation.
- `internal/entry/web/projects.go`: live validation and Host bundle loading.
- `internal/entry/web/styles_test.go`: endpoint refresh and project creation regressions.
- `D:\opennovel-runtime\start-opennovel-lan.cmd`: deployment launcher outside the repository.

## Validation
- Previous local feature validation: assets tests, focused Web style tests, relevant vet, rebuild, and live refresh probe passed.
- Previous remote deployment validation: Web/runtime builds and loopback/LAN HTTP probes passed.
- Merged-tree validation: pending.

## Blockers / Risks
- Windows Firewall may still require an elevated inbound TCP 9999 rule for reliable phone access.
- The broader Web test baseline previously had unrelated failures outside runtime-style discovery.
- Do not resume Yinglao or trigger model calls without explicit user authorization.

## Next Steps
1. Stage the resolved handoff and verify there are no remaining unmerged paths.
2. Run focused merged-tree tests, vet, and diff checks.
3. Commit the merge and push `main` to `github/main`.

## Notes For Next Session
- OpenNovel uses port 9999; do not touch the obsolete legacy port.
- Runtime Markdown overrides same-ID embedded styles; embedded styles remain the fallback.
- Use `D:\opennovel-runtime\start-opennovel-lan.cmd` to preserve D-drive runtime/profile paths for LAN deployment.
