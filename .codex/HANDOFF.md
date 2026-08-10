# Project Handoff

Last updated: 2026-08-10 09:20 +08:00
Project root: D:\OpenNovel
Branch: main
Status: done

## Current Goal
Synchronize the local runtime-style feature with the latest GitHub `main`, preserve the D-drive LAN deployment notes, validate the merged tree, and push it to GitHub.

## Current Position
GitHub commit `6d0597c7` and local feature commit `8867c0b0` were merged as `bae1a4d0` and pushed to `github/main`. Their only textual conflict was this handoff file; both the deployment state and runtime-style implementation were retained.

## Recent Actions
- Fetched `github/main` and confirmed one local and one remote commit had diverged from `54a3a777`.
- Merged the remote D-drive LAN deployment documentation into local `main`.
- Resolved `.codex/HANDOFF.md` by preserving the current facts from both branches.
- Retained the remote `GIT_NOTES.md` deployment update without modification.
- Retained local runtime discovery for repository `assets/styles/*.md`, including `suspense_sex.md` and regression tests.
- Pushed merge commit `bae1a4d0` to `https://github.com/while4234/opennovel.git`; local and remote `main` matched afterward.

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
- `go test ./assets -count=1` -> PASS.
- Focused runtime-style Web tests -> PASS.
- `go vet ./assets ./internal/entry/web` -> PASS.
- `git diff --cached --check` before the merge commit -> PASS.

## Blockers / Risks
- Windows Firewall may still require an elevated inbound TCP 9999 rule for reliable phone access.
- The broader Web test baseline previously had unrelated failures outside runtime-style discovery.
- Do not resume Yinglao or trigger model calls without explicit user authorization.

## Next Steps
1. Review GitHub `main` at merge commit `bae1a4d0` if desired.
2. Add the elevated Windows Firewall rule if phones still cannot reach TCP 9999.
3. Address unrelated broader Web test baseline failures in a separate task if needed.

## Notes For Next Session
- OpenNovel uses port 9999; do not touch the obsolete legacy port.
- Runtime Markdown overrides same-ID embedded styles; embedded styles remain the fallback.
- Use `D:\opennovel-runtime\start-opennovel-lan.cmd` to preserve D-drive runtime/profile paths for LAN deployment.
