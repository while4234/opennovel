# Project Handoff

Last updated: 2026-08-06 19:32 +08:00
Project root: D:\OpenNovel
Branch: main
Status: ready_for_review

## Latest Paging Fix
- The 30,086-byte post-repair `planning_review(volume=3)` is now a deterministic, lossless sequence of JSON-Pointer evidence pages capped at 14 KiB. Real pages are 10,690 / 12,382 / 11,671 bytes; reconstruction preserves the target theme and every arc goal byte-for-byte.
- Every Host Editor dispatch creates a fresh random `review_id`. The shared in-memory registry binds selector, Foundation revision/signature, full skeleton digest, and evidence digest; enforces exact cursor order; revokes prior same-selector runs; clears on pause/reset; deletes after save; and caps active runs at 64.
- `save_original_planning_audit` fails closed for all `skeleton_*` scopes until every authorized page is read. Duplicate, skipped, cross-run, stale, early-save, reused, and invented review flows are rejected.
- Real Editor compile rounds with production KeepRecent=1 are 27,482 / 30,474 / 30,501 bytes, all below 98,304 bytes and not linear in page count.
- Focused tools/agents/host-flow/assets tests, real-fixture tests, Go vet, and `git diff --check` pass. Yinglao remains paused; no restart or resume occurred during this fix.
- Third live integration failed before page zero: the real Editor dispatch did not expose the Host-assigned `review_id` in a form the model used. Editor first omitted it, then guessed unauthorized IDs, so the registry correctly rejected every read and the process looped.
- The dispatch-ID defect is now fixed statically. Page zero may omit `review_id` and resolves only the unique active Host authorization for the complete selector; zero or ambiguous matches fail closed. Later calls may rely on the signed cursor, while save still requires the canonical page-zero ID.
- A shared Host task preparer now covers normal Dispatcher messages, initial Start/Resume prompts, and StopGuard reminders. New dispatches create a fresh ID; StopGuard reuses the active ID and cannot revoke an in-flight cursor. Without a provable active run, StopGuard does not reissue Editor work.
- Real Coordinator-message capture, initial Resume, StopGuard reuse, unauthorized-reminder fallback, three-page cursor traversal, canonical-ID save, focused tests, related `go vet`, and `git diff --check` all pass.
- Final live integration proved page zero can now omit the ID and resolve the canonical Host authorization. It then exposed a narrower continuation defect: cursor-only page reads fail as stale/different evidence, while the same signed cursor succeeds when the canonical `review_id` is also supplied.
- Cursor continuation is now normalized before any evidence is built: the cursor is signature-checked, its review ID and selector are verified against the current active Host run, explicit conflicting ID/volume/range values are rejected, and omitted values inherit the canonical cursor selector. Tools still cannot create or revive authorization.
- Synthetic and real Yinglao regressions now use page zero `scope+volume` without ID and every later page with only `scope+cursor`. Real pages remain 10,690 / 12,382 / 11,671 bytes; compiled Editor rounds are 27,995 / 30,935 / 30,909 bytes and pass the 98,304-byte boundary.

## Current Goal
Fix repeated `architect_long` context overflow during the real Yinglao volume-skeleton repair without raising production limits, dropping canonical signatures, or mutating unrelated production state.

## Current Position
The bounded projection, page-zero authorization, Host dispatch exits, and cursor-only continuation pass focused synthetic and real-fixture tests. The latest working tree is now rebuilt and running on `127.0.0.1:9999`, but cursor continuation was intentionally not live model-validated because the user has no model tokens. Yinglao is non-running at `outline / volume_plan`; no Agent-driven or scheduled Resume is allowed.

## Recent Actions
- Reproduced the old request read-only: `planning` was 74,575 bytes and the post-tool Architect request compiled to 100,558 bytes against the unchanged 98,304-byte limit.
- Added bounded aggregate `planning`, required-selector `planning_volume`, and final post-marshal JSON gates for every planning-family scope.
- Replaced an established creative brief with a deterministic digest/identity projection while retaining full pre-premise input and canonical Foundation artifacts.
- Routed first/append/repair volume Host tasks through `planning_volume`; detail/review routes retain their existing scoped contracts.
- Rebuilt and restarted only port 9999. Current process is `D:\OpenNovel\ainovel-cli.exe`, PID 33024, runtime root `C:\Users\Hi\.ainovel\novels-preview`.
- Confirmed Yinglao pre-resume state was non-running `outline / volume_plan`, with `resume_project` as the next action. Issued exactly one UI-equivalent `POST /api/projects/project-20260805031009-6eb25a/resume`, which returned HTTP 200 and `running=true`.
- Live sequence evidence: 28636 `novel_context` success; 28691 `save_foundation[repair_volume]` success; 28983 Architect dispatch success; 29123 volume 1 audit save success. The flow subsequently completed volume 2 and 3 audits and was running volume 4 audit at sequence 29482.
- Searched all post-resume events through sequence 29344 for `100558`, `98304`, `context window overflow`, and the prior compiled-request error: zero matches.
- Final-review follow-up found repeated volume-3 context reads because target theme/arc goals were truncated. The project was still running at sequence 30338, so one UI-equivalent `POST /pause` returned HTTP 200 with `stopped=true`; final sequence 30348 is `paused / outline / volume_plan`, `PlanningReview=collecting`.
- Reworked the projection around the explicit selector instead of `Progress.CurrentVolume`: the selected target remains byte-for-byte canonical, adjacent volumes retain bounded structural summaries, and detailed `planning_audit` uses its own compact target skeleton because its exact 1-4 chapter evidence is carried separately.
- Added an Editor prompt and Host route contract that permits only `planning_review` for volume-skeleton review and rejects Architect-owned `planning_volume`.
- Second integration rebuilt only port 9999 (PID 33456), confirmed the persisted non-running outline checkpoint, and issued exactly one new resume request (HTTP 200).
- Live selector proof: Architect requested `planning_volume(volume=3)` at sequence 30459; its complete target theme and all three complete arc goals were visible without `...`, and `save_foundation[repair_volume]` succeeded at 30507.
- Editor requested `planning_review(volume=3)` at sequences 31103, 31151, 31195, and 31290 and never requested `planning_volume`. Each full review projection failed closed at 30,086 / 28,672 bytes (`foundation_memory=16,533`, `planning_memory=12,359`). The repeated reads and fallback to `status`/generic `planning` constituted a loop.
- The loop was stopped with exactly one `POST /pause` (HTTP 200, `stopped=true`). Final sequence 31444 is `paused / outline / volume_plan`, non-running, with review status `collecting`.
- Third integration rebuilt only port 9999 (PID 7948), confirmed the paused checkpoint, and issued exactly one new UI-equivalent resume request (HTTP 200).
- At sequence 31537 Editor called `planning_review(volume=3)` without `review_id`; the tool correctly returned `planning_review requires Host-assigned review_id`. Editor then guessed IDs such as `vol3_skeleton` and `skeleton_volume_3`, which were correctly rejected as unauthorized. No page-zero response or `next_cursor` was reached.
- The invalid loop was paused. The existing 18:00 scheduled-resume job immediately restarted it without a user/API resume request, so a second pause was required to stop that separately scheduled run. Final sequence 32141 is `paused / outline / volume_plan`, non-running.
- Final integration rebuilt only port 9999 (PID 11660), confirmed the paused checkpoint, and issued exactly one new UI-equivalent resume request (HTTP 200).
- Page zero succeeded at sequence 32253 from `planning_review(volume=3)` with no ID and returned canonical `planning-review-da3f...` plus the page-1 cursor. The first cursor-only continuation failed at 32276 with `cursor belongs to a stale or different evidence snapshot`; Editor then repeated page zero at 32311 and was correctly rejected because page 1 was next.
- The identical page-1 cursor succeeded at 32378 after Editor included the canonical `review_id`. Cursor-only page 2 failed again at 32389, while canonical-ID plus page-2 cursor succeeded at 32406. This error/retry pattern violated strict ordered acceptance, so the run was paused once. Final sequence 32439 is `paused / outline / volume_plan`, non-running; no audit save occurred.
- Deployment-only restart rebuilt and restarted only port 9999. Current process is `D:\OpenNovel\ainovel-cli.exe`, PID 21080, binary timestamp `2026-08-06 19:31:37`; runtime root remains `C:\Users\Hi\.ainovel\novels-preview` and the home page returned HTTP 200.
- Post-restart Yinglao snapshot is `idle / non-running / outline / volume_plan` with `resume_project` as the next action. No Resume, model call, schedule change, Playwright run, commit, or push occurred during this deployment step.

## Changed / Relevant Files
- `internal/tools/novel_context.go`: new scope dispatch and final bounded serialization.
- `internal/tools/novel_context_builders.go`: general/volume aggregate projections and pre-premise compaction.
- `internal/tools/novel_context_budget.go`: planning-family final JSON hard gates.
- `internal/tools/novel_context_compact.go`: target plus adjacent-volume projection.
- `internal/tools/creative_brief.go`: established-brief digest projection.
- `internal/host/flow/router.go`: scoped first/append/repair volume calls.
- `assets/prompts/architect-long.md`: explicit scope-selection contract.
- `internal/tools/novel_context_budget_test.go`: selector and fail-closed gate tests.
- `internal/tools/novel_context_real_test.go`: read-only real-project byte regression.
- `internal/agents/architect_planning_real_test.go`: actual compiled request regression.

## Validation
- Latest isolated real-project regression: `planning` 27,960 bytes; `planning_volume(volume=1)` 27,096 bytes; `planning_review(volume=1)` 26,123 bytes; `planning_review(volume=3)` 27,676 bytes.
- Real volume 3 target theme and every arc index/goal are byte-for-byte equal to the canonical saved outline; synthetic regression confirms adjacent volume theme/goals are bounded.
- Actual failed-task compile chain after the selector fix: 50,978 / 98,304 bytes; acceptance threshold 80 KiB.
- Focused `internal/tools` gates for selector fidelity, adjacent compression, final review budget, and detailed-audit budget -> PASS.
- Focused `internal/host/flow` Editor route contract and `assets` prompt quality gates -> PASS.
- `go vet ./internal/tools ./internal/agents ./internal/host/flow ./assets` -> PASS.
- Real-output focused tests with `AINOVEL_REAL_PLANNING_OUTPUT=<Yinglao output>` -> tools PASS; agents PASS.
- `git diff --check` -> PASS with line-ending warnings only.
- `GET http://127.0.0.1:9999/` -> HTTP 200 after restart.
- Live acceptance -> Architect post-tool round, repair save, and multiple subsequent volume audits succeeded; no overflow recurrence or workflow error.
- Second integration focused gates -> selector exactness PASS; real bytes PASS; compiled Architect request PASS; Host selector contracts PASS; assets PASS; focused `go vet` PASS; `git diff --check` PASS.
- Second live acceptance -> FAIL: target fidelity was correct, but post-repair `planning_review(volume=3)` exceeded its final JSON budget and caused repeated tool reads. No compiled-agent context-window overflow occurred.
- Third integration focused gates -> registry ordering and misuse rejection PASS; incomplete-read save gate PASS; real three-page byte regression PASS; compiled Editor rounds PASS; fresh Host `review_id` test PASS; assets, `go vet`, and `git diff --check` PASS.
- Third live acceptance -> FAIL before pagination: the authorized Host `review_id` was not usable from the real Editor task, causing omitted/guessed-ID retries. Registry enforcement behaved correctly; Host-to-model task injection did not.
- Final focused gates -> page-zero unique authorization PASS; Host message and Resume/StopGuard authorization PASS; paging order PASS; save gate PASS; real Editor compiled rounds PASS; assets, `go vet`, and `git diff --check` PASS.
- Final live acceptance -> FAIL after page zero: canonical authorization worked, but cursor-only page lookup failed twice and caused a duplicate page-zero read. No unauthorized guessed ID, `planning_volume`, context overflow, or audit save occurred.
- Final deployment smoke -> PASS: restart script completed, listener PID/path and runtime root were verified, the runtime API and home page responded, and Yinglao remained non-running. The latest cursor fix has not been live model-validated.

## Blockers / Risks
- Yinglao remains intentionally non-running after the deployment restart. The latest cursor fix is in the running binary but has not been live model-validated.
- The user currently has no model tokens. Do not let an Agent, StopGuard, scheduler, or other automation Resume the project. Do not spend tokens on a live validation run.
- A broad `internal/tools` run exposed the detailed-audit budget coupling and a Windows TempDir cleanup race. The budget coupling was fixed and its exact regression now passes; the transient Windows cleanup test was not rerun because the parent requested no additional broad testing.

## Next Steps
1. Keep OpenNovel running on port 9999; never touch port 9898.
2. Keep Yinglao non-running. Do not automatically Resume from an Agent or scheduler while the user has no model tokens.
3. When the user later authorizes token-consuming validation, Resume at most once and verify page zero plus cursor-only page 1/page 2 and final audit save.

## Notes For Next Session
- Never access, inspect, stop, or reuse port 9898.
- Do not raise the 96 KiB Architect boundary; this fix depends on bounded source views plus a fail-closed final JSON gate.
- Real fixture tests copy the output tree and perform no model or save calls.
- Only one resume request was sent in this integration phase.
