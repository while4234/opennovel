# Project Handoff

Last updated: 2026-08-07 12:39 +08:00
Project root: D:\OpenNovel
Branch: main
Status: done

## Current Goal
Make the Web UI style refresh discover new `assets/styles/*.md` files without rebuilding, then rebuild and restart OpenNovel so `suspense_sex.md` is available.

## Current Position
Implementation and deployment are complete. Port 9999 is running the rebuilt executable, `/api/styles` includes `suspense_sex`, and a live add/remove probe proved that subsequent Markdown changes are detected without another restart.

## Recent Actions
- Added `assets.StyleSource`, which merges runtime Markdown over embedded defaults on every catalog/load call.
- Wired Web catalog, style validation, project creation, style switching, and project Host loading to the same source.
- Added assets and Web regression tests for discovery after server startup, runtime overrides, uppercase extensions, and project creation.
- Rebuilt the UI, main executable, expansion auditor, and completion auditor with `restart-web.cmd -Port 9999`.
- Verified `suspense_sex` appears as `悬疑色情风格` in the live API.
- Added and removed `__refresh_probe.md`; the running service reflected both changes without restart.

## Changed / Relevant Files
- `assets/load.go`: runtime plus embedded style source.
- `assets/load_test.go`: runtime discovery and override tests.
- `assets/styles/suspense_sex.md`: new user-provided writing style.
- `assets/README.md`: refresh behavior documentation.
- `internal/entry/web/server.go`: repository style-directory wiring.
- `internal/entry/web/styles.go`: live catalog and validation.
- `internal/entry/web/projects.go`: live validation and Host bundle loading.
- `internal/entry/web/styles_test.go`: endpoint refresh and project creation regression.
- `GIT_HISTORY.md`: scoped change record.

## Validation
- `go test ./assets -count=1` -> PASS.
- Focused Web style tests -> PASS.
- `go vet ./assets ./internal/entry/web` -> PASS.
- `restart-web.cmd -Port 9999` -> PASS; service ready on `http://127.0.0.1:9999`.
- Live `/api/styles` -> includes `suspense_sex` / `悬疑色情风格`.
- Live add/remove refresh probe without restart -> PASS.
- Full `go test -p 1 ./internal/entry/web -count=1` -> FAIL on unrelated existing co-create, adaptation, library, clone, and session tests; sampled failures were missing core-cast contract (409) and adaptation generation not starting.

## Blockers / Risks
- No blocker for runtime style refresh.
- The broader Web test baseline remains red outside this change.
- Do not touch port 9898; OpenNovel owns port 9999.
- Do not resume Yinglao or trigger model calls without explicit user authorization.

## Next Steps
1. Review the local commit if needed.
2. Use the Web UI refresh button after adding or editing future `assets/styles/*.md` files.
3. Address the unrelated full Web test baseline in a separate task.

## Notes For Next Session
- Runtime Markdown overrides same-ID embedded styles; embedded styles remain the fallback when no repository directory exists.
- Active Hosts pick up style content when a project is opened or its style is changed; catalog refresh itself does not mutate an already-running Host.
- OpenNovel is currently running at `http://127.0.0.1:9999`.
