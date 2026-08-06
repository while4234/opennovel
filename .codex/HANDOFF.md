# Project Handoff

Last updated: 2026-08-06 13:18 +08:00
Project root: D:\OpenNovel
Branch: main
Status: ready_for_final_review

## Current Goal
Complete the OpenNovel UI rebuild, close the four user-reported Phase 8 defects, independently review the result, then commit and push the accepted work.

## Current Position
Phase 7, Phase 8 defect repairs, the Foundation Character-context budget repair, and final integration acceptance are complete. The rebuilt application is running at `http://127.0.0.1:9999` as PID 6496 (`D:\OpenNovel\ainovel-cli.exe`) with embedded asset `assets/index-CUs0vrIP.js`. The real `樱牢` Foundation flow was resumed exactly once and reached `waiting_confirmation` after its independent Character review passed. No commit or push has been made.

## Final Integration Run
- `POST /api/projects/project-20260805031009-6eb25a/resume` was sent once after confirming the project was idle, paused at Foundation, and recoverable. It returned HTTP 200 with `running=true`.
- Event `25332` read Character context page 1 and event `25353` read page 2; both completed in about 22 ms with no context-budget or cursor error.
- Event `25398` saved the Character review, event `25404` reported review success, and event `25406` completed Character dispatch successfully.
- Final state is `RuntimeState=idle`, workflow `waiting_confirmation`, `analysis_status=candidate_ready`, `review_status=passed`, `confirmation_status=unconfirmed`, and an empty recovery error. No stale candidate conflict or `69636 > 40960` failure occurred.

## Phase 8 Changes
- Fenced Foundation/character rendering by project ID and request version; project switches now clear the previous project before late responses can render.
- Added desktop and mobile `本项目模型` shortcuts to `/settings/models?project=<projectId>`; the Settings scope selector loads the matching project model contract without replacing the active writing session.
- Removed `提示词` from the primary global sidebar while retaining `/settings/prompts` and the Settings-center entry.
- Reconciled both unconfirmed stale states during Resume: a stale candidate Base that returns `tool conflict`, and a canonical Base paired with a diverged lifecycle/run binding. Confirmed candidates keep strict rebind/error behavior.
- Added revision-and-digest fencing plus a canonical-output-path shared CharacterCard lock, preventing different Store instances from racing candidate/lifecycle writes and stale discard.

## Phase 8B Character Context Budget Repair
- Root cause: original-project review returned the complete staged Character candidate. The real `樱牢` packet was 69,636 bytes: complete characters were about 28.6 KiB, the creative brief about 23.3 KiB, relationships about 7.3 KiB, user constraints about 5.6 KiB, plus lifecycle/signatures/premise. This is not Writer draft history; the 40,960-byte limit is `characterContextMaxBytes = 40 * 1024`.
- Existing compaction covered adaptation/analyze and raw-source omission, but the independent original/review path intentionally kept full cards and its large-input test coverage was missing.
- Small packets retain the compatible unpaged response. Oversized packets use deterministic, lossless JSON-Pointer evidence pages; long strings are split only at rune boundaries and include ordered chunk metadata.
- Opaque cursors bind run ID, mode, attempt, Foundation revision/audit signature, input digest, and the complete evidence digest. The run registry enforces in-order traversal and rejects missing, repeated, tampered, cross-run, or stale-snapshot pages. Save tools fail closed until every page is read.
- The Character task prompt and role prompt explicitly require following `next_cursor` without skipping or repeating pages before the single save call.

## Relevant Files
- `internal/entry/web/ui/src/foundation/FoundationCenter.jsx`
- `internal/entry/web/ui/src/App.jsx`
- `internal/entry/web/ui/src/shell/AppShell.jsx`
- `internal/entry/web/ui/src/shell/route-config.js`
- `internal/entry/web/ui/src/settings/SettingsCenter.jsx`
- `internal/entry/web/ui/src/workspace/ProjectWorkspaceShell.jsx`
- `internal/entry/web/ui/tests/browser/knowledge-assets.spec.js`
- `internal/entry/web/ui/tests/browser/settings-center.spec.js`
- `internal/host/character_workflow.go`
- `internal/tools/save_foundation.go`
- `internal/store/character_cards.go`
- `internal/entry/web/character_confirmation_resume_test.go`
- `internal/store/character_cards_test.go`
- `internal/tools/character_agent.go`
- `internal/tools/character_agent_test.go`
- `internal/agents/character_task.go`
- `assets/prompts/character.md`

## Validation
- Final frontend unit suite: 60 files, 478 tests passed.
- Final external-`9999` Playwright matrix on system Chrome: 26 passed, 49 device-conditional skipped, 0 failed across desktop, `430x932`, and `932x430`; mobile composer/navigation overlap was zero and submit hit-testing passed.
- Final screenshots: 30 files under `internal/entry/web/ui/test-results/ui-refactor/phase9`, including the real post-resume `sakura-after-resume.png` waiting-confirmation state.
- Phase 8 Playwright on external `9999`: 12 desktop tests passed, 2 device-conditional tests skipped, 0 failed.
- Project switch browser race: delayed project A response cannot replace project B's character card; screenshot `characters-project-switch.png` generated.
- Project model shortcut browser contract: URL, scope selector, and `GET /api/projects/<id>/models` all verified; prompts absent from primary nav and present inside Settings.
- Focused Web Go tests passed for the confirmation gate, candidate-Base tool conflict cleanup, diverged lifecycle cleanup, and strict preservation of stale confirmed candidates.
- Focused store tests passed for revision/digest CAS, same-directory shared-lock blocking, and deterministic writer-vs-stale-discard concurrency; the new revision survives and stale discard returns a revision conflict.
- Existing confirmed Character rebind tool tests passed.
- All focused Character tool tests passed, including a 65-80 KiB original/review fixture, exact UTF-8 brief reconstruction, deterministic paging, strict final-page byte accounting, cursor fencing, incomplete-read save rejection, and successful save after complete traversal.
- `internal/agents` Character tests, the two affected Host Character tests, and `go vet ./internal/tools ./internal/agents` passed. Broad Agents/Host runs only reproduced the known Windows `TempDir RemoveAll: directory is not empty` cleanup race; every reported test passed when rerun individually.
- A disposable copy of the live `樱牢` output passed read-only Character-context acceptance without a model or save call: the former 69,636-byte logical packet became two final JSON pages of 39,947 and 33,049 bytes, both below 40,960 with exact `context_budget.bytes` metadata.
- Real `樱牢` acceptance now copies the production output into a fresh `t.TempDir` on every run. The current production candidate was regenerated at 12:05 and is no longer stale, so the test injects the old Base only in the fresh clone, proves the exact `CurrentCharacterWorkflow` tool conflict, repairs to pending, and verifies production candidate revision/digest are unchanged.
- Production frontend build passed (1827 modules); only the existing large-chunk warning remains.
- Final focused Go gates passed for global prompts/bootstrap, all Phase 8 Resume repair cases, Character-card CAS/shared-lock concurrency, Character context pagination/real-project acceptance, and Character task/budget behavior. `go vet` passed for the affected packages.
- `git diff --check` passed with line-ending warnings only.
- `GET /api/runtime` and `/` on 9999 returned 200; PID 6496 resolves to `D:\OpenNovel\ainovel-cli.exe`, and the embedded entry asset is `assets/index-CUs0vrIP.js`.

## Known Baseline / Notes
- Broad concurrent Go package testing on Windows produced pre-existing mutation/temp cleanup failures. Prefer focused tests or `go test -p 1` for mutation-heavy packages; do not attribute baseline failures to Phase 8 without a clean-HEAD comparison.
- `go test -race` is unavailable because this Windows Go environment has CGO disabled and no approved C toolchain. Deterministic concurrency tests passed without the race detector.
- The state-level acceptance copy remains under `C:\Users\Hi\AppData\Local\Temp\opennovel-phase8-2c2b0aab53ac4eeab443751ce5e02ae0`; automatic recursive deletion was blocked by the command policy. It is outside the repository and contains only a disposable copy.
- The read-only context-budget acceptance copy remains under `C:\Users\Hi\AppData\Local\Temp\opennovel-context-budget-37a6f046633a48f7a1b2d2c138838b64`; it is outside the repository and contains only a disposable output copy.
- OpenNovel owns port 9999. Do not inspect, stop, reuse, or route tests through port 9898.

## Next Steps
1. Run the final independent review/check-work gate over the complete working tree and integration evidence.
2. If approved, update `GIT_HISTORY.md`, commit the intended refactor files, and push `main` to the configured GitHub remote without force.
3. Leave `樱牢` at `waiting_confirmation`; the user can inspect and explicitly confirm the reviewed Character candidate in the UI.
