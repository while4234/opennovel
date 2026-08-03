# Git Notes

## Repository Purpose

This repository contains the `ainovel-cli` Go command line application and its
supporting assets, documentation, scripts, and tests.

## Ignore And Secret Policy

Local executables, generated build/release artifacts, workspaces, output
directories, the repo-local `novel/` runtime/user-data directory, editor files,
and local `.ainovel/` configuration are ignored. Never commit provider
credentials, API keys, `.env` values, private keys, cookies, or raw local auth
paths. Safe example configuration files may be tracked when they contain
placeholders only.

Project-specific exception approved on 2026-07-16: the private-repository
simulation corpus downloader tracks only
`internal/entry/web/simulation_download_credentials.enc`. It is an AES-GCM
bundle containing the DaliPan authorization value and BaiduPCS-Go config, with
the matching application key material intentionally shipped for zero-config
private deployment. Never commit the plaintext inputs, temporary browser
profiles, decrypted runtime config, or any console output containing their
values. Treat repository read access as credential access and rotate/reseal the
bundle if that access boundary changes.

## Build And Test Storage Policy

On this Windows workstation, all AINovel build, test, and local deployment
working storage must stay under `D:\ainovel\.cache`. Use `go-project.cmd` for
Go commands and `restart-web.cmd` for deployment builds. Go tests automatically
clean their build/test cache and transient files when they finish; do not allow
`AppData\Local\go-build` on the C drive to accumulate again.

## Project GitHub Rule

For this project, a completed future development task is not done until the
intentional changes are committed and pushed to the configured GitHub remote
(`origin`) unless the user explicitly says not to push for that task.

## Project Operating Preference

For this project, after each completed generation or development task that
changes the running project, backend, or Web UI, rebuild/restart the local
`ainovel-cli.exe web` service and explicitly tell the user to refresh the Web
page. The normal local Web URL is `http://127.0.0.1:9898`.

## Current Baseline
- 2026-07-31 `this commit` `fix: resume and complete legacy novels`: repairs
  confirmed Character bindings when only newly required gender metadata or
  user-rules evidence changed, creates a fail-closed legacy completion
  checkpoint only for exact 55/55-style formal coverage, enriches old layered
  completion evidence, makes Web resume execute that migration, aligns
  independent Ed25519 receipt canonicalization, and removes two deterministic
  audit false positives. Real validation completed `插翅难飞` at 55/55 chapters
  and 221,710 words with a signed 132-layer completion audit.
- 2026-07-31 `this commit` `fix: expose interrupted adaptation planning`:
  marks durable adaptation generation as paused and recoverable when no live
  proposal action exists after a server restart, while preserving the normal
  running presentation for active generation. The workflow panel now exposes
  the existing volume/detail resume action instead of showing a zombie run.
- 2026-07-31 `a641c69` `fix: allow bounded editor arc reviews`:
  gives only `agent_editor` a 96 KiB compiled-input boundary so a confirmed
  four-chapter arc package can survive one `novel_context` expansion. A real
  63.2 KiB review passed and saved its arc summary after deployment.
- 2026-07-30 `ae1eef4` `fix: route planning reviews through workflow panel`:
  wires durable workflow next actions to the matching adaptation, original
  planning, character-confirmation, or resume handler instead of leaving the
  progress panel inert. Pre-writing detail-batch audits now have a scoped
  96 KiB input boundary; a real 65,184-byte audit passed after deployment.
- 2026-07-30 `76690ed` `fix: unblock long-form planning contexts`:
  classifies the confirmed target Foundation as dynamic adaptation-planner
  evidence instead of static role-core text, and gives only
  `architect_long` a 96 KiB compiled-input boundary for one valid focal-volume
  context. Planner calls now rely on their tokenizer-aware 40k-token compiler
  hard limit instead of a conflicting 60 KiB UTF-8 byte ceiling. Focused
  agents/adaptation tests plus full Host and tools packages pass; the repaired
  Web service is healthy on port 9898 with both monitored test projects
  resuming from their saved planning checkpoints.
- 2026-07-30 `31d8a34` `fix: unify original character revision feedback`:
  distinguishes the pre-Foundation character-candidate gate from complete
  StoryFoundation review while giving both the same author-facing
  view/revise/review/confirm skeleton. Original candidate review now collects
  intent in place, starts a revision/digest-guarded Character Agent run
  directly, and retains the character workbench for candidate inspection and
  explicit independent review. Container-responsive cards remove the 1440x900
  narrow-center overlap. All 396 Web tests, embedded-Web Go tests, production
  build, live restart, and isolated installed-Chrome acceptance passed.
- 2026-07-30 `this commit` `fix: streamline Foundation review actions`:
  separates AI-intent revision from precise manual field revision, preserves
  post-edit dependency and quality audits, improves the focused Foundation and
  character-agent workspaces, and documents the writer-centered review model.
  Original-project character review now exposes exactly one central
  `确认角色卡` action wired to the real publication call; the duplicate
  `确认并继续` and workflow-progress action are removed. Adaptation review keeps
  `前往确认与修改` in the right inspector and removes the duplicate central
  `去确认`. The full 393-test Web suite, focused static-embed Go tests,
  production build, live restart, and two-project Chrome acceptance passed with
  zero console errors.
- 2026-07-30 `ace4c39` `fix: make Foundation confirmation discoverable`:
  moves complete Foundation review into a central tabbed workspace, reduces
  the right inspector to status plus a direct entry, and provides polished
  full-height and compact desktop confirmation layouts. Reviewed character
  candidates now expose an explicit confirmation gate through workflow
  progress, Resume, Foundation Center, and the character-card workspace.
  Focused UI tests (88), Go resume/progress regressions, embedded asset tests,
  production build, HTTP smoke checks, and visible 1920x1080/1440x900 browser
  checks passed.
- 2026-07-30 `this commit` `fix: split original and adaptation planning flows`:
  gives adaptation volume review and chapter-outline review independent
  progress stages, maps them to the durable planning workflow, and removes the
  volume step for original/direct-adaptation short paths once scale evidence
  says the backend will skip it. All WorkflowProgress tests and diff checks
  pass; the full Web suite retains unrelated rollback, library, and action
  revision lease fixture failures.
- 2026-07-30 `fc580ca` `fix: complete adaptation character card workflow`:
  removes the circular CoreCast resume gate, bounds Character Agent evidence
  below the structured-call limit, restricts formal source cards to protagonists
  and important supporting roles, preserves evidence-only aliases outside the
  formal cast, and projects reviewed source mappings plus target-original leads
  into the confirmed CoreCast. Both ordinary and adaptation character prompts
  now require complete cards and an independent blocking review. Focused
  domain/tools/adapt/flow/store/host/Web regressions passed; the full Web suite
  retains unrelated rollback, novel-library, legacy-migration, and adaptation
  session fixture failures. The rebuilt Web service is healthy on port 9898,
  and the live `梦中的女孩` project visibly shows three complete reviewed cards,
  including `顾衡` as `新男主（主视角）` and `目标原创`.
- 2026-07-30 `this commit` `fix: declutter foundation settings workspace`:
  hides the creative workflow while StoryFoundation is open, treats an empty
  read-only revision-zero Foundation as an in-progress state instead of a red
  validation failure, and gives the character directory and editor independent
  scrolling with compact filters and Agent controls. Focused Foundation tests,
  the Vite production build, live asset checks, and visible-Chrome checks on
  both original and adaptation projects passed.
- 2026-07-30 `c059ea6` `fix: simplify creative stage model settings`:
  moves 角色分析 and 角色审核 directly after 资料分析, removes the user-facing
  Agent advanced-route editor and provider-role selector, and keeps internal
  Agent routing as an inherited compatibility mechanism. Bootstrap/Web focused
  tests, 374 Web tests, production build, and live port 9898 asset/API checks
  passed.
- 2026-07-30 `this commit` `fix: expose complete source foundation before cocreate`:
  source chapter analysis now extracts evidence-backed gender and complete
  character-card fields, while full-book batches preserve source relationships
  and world rules. SourceFoundation version 3 removes the single final all-book
  model response: report-sized model batches are assembled deterministically
  by stable character identity, validated relationship endpoints, and
  deduplicated rules, so a multi-million-character source scales by checkpoint
  count rather than final response length. The live “梦中的女孩” analysis
  completed 17/17 reports, three bounded batches, and code assembly; the
  Foundation API returns 8 source roles, 10 relationships, and 19 rules before
  co-create. The UI exposes all three sections read-only and suppresses target
  completeness/review/mapping warnings while no target foundation exists.
- 2026-07-30 `403c1d0` `fix: make portable profile import idempotent`:
  importing the exact same portable profile again now keeps its original
  profile/corpus identity and reports every source as skipped instead of
  summing `source_count` again. The real project has 17 source files; its
  displayed 34 came from two pre-fix imports and will return to 17 after a
  successful full reanalysis rebuilds the profile from those files.
- 2026-07-30 `04fedd0` `fix: archive simulation corpus with profiles`:
  project saves and automatic simulation-profile sync now persist a verified
  sidecar copy of the original corpus beside the portable profile. Loading a
  bundled profile restores its corpus without overwriting different local
  files, which keeps rescan and full reanalysis available after profile/schema
  upgrades. The live “梦中的女孩” library entry was migrated with all 17 source
  files and loaded into “梦中的女孩改”; both reanalysis actions are enabled.
- 2026-07-30 `this commit` `fix: bound adaptation foundation merge requests`:
  changed SourceFoundation report and recursive-summary batching to enforce the
  structured-call 60 KiB limit against exact compiled request bytes, not only
  a rune estimate. Added a Chinese UTF-8 regression, rebuilt/restarted port
  9898, and verified the real 17-chapter “梦中的女孩” upgrade split its formerly
  rejected single request into two valid report batches, completed its final
  summary, and synced the library. The pre-intent role-card tab now also
  displays all analyzed source roles as read-only evidence while keeping target
  role creation gated on adaptation co-create.
- 2026-07-29 `a22a273` `fix: upgrade legacy adaptation source packages`:
  rejects unversioned SourceFoundation reuse, incrementally upgrades legacy
  novel-library packages, exposes forced source reanalysis for completed
  sources, distinguishes source analysis from co-create briefing regeneration,
  and deploys the rebuilt Web service on port 9898. Focused/full changed-package
  Go tests, 370 Web tests, production build, and live HTTP/bundle checks passed.
  The project-environment full suite remains red only in the existing Windows
  legacy-migration temp-directory rename access-denied test, reproduced twice.
- 2026-07-29 `5a1cea5d` `fix: activate reinforced simulation contracts`:
  fast-forwarded local `main` to the latest `origin/main`, rebuilt the
  production Web UI plus all three Go executables through `restart-web.cmd`,
  replaced PID `33172` with PID `33424` on port 9898, and verified `/`,
  `/api/runtime`, and `/api/projects` return HTTP 200. The deployed CLI reports
  commit `5a1cea5dace142e9f45cb2ecd20c12a0e83915c0`.
- 2026-07-29 `faad199` `fix: resume interrupted polishing gates`:
  lets a changed rewrite/polish draft resume from its persisted validation
  receipts after provider failures instead of restarting the full rewrite and
  invalidating already-passed checks. Unchanged committed prose still requires
  a real polish edit. Focused flow tests and a clean CLI build passed.
- 2026-07-29 `2580410` `fix: clarify active simulation guidance`:
  gives active analysis precedence over the previous profile health card,
  hides stale diagnostics until replacement synthesis completes, and keeps the
  next-step guidance in an explicit running state. Real Chrome validation used
  a cloned legacy 276/276 profile, eight repository corpus fixtures, and one
  newly uploaded TXT; the UI progressed to fresh 8/8 and then fresh 9/9, with
  both runs automatically replacing the project-named library profile.
- 2026-07-29 `93bb0e6` `test: compare synced portable profile digests`:
  verifies automatic simulation-library replacement through portable profile
  digests instead of depending on project-local source paths that portable
  artifacts are designed not to expose.
- 2026-07-29 `e870e9c` `test: verify simulation library replacement`:
  strengthens the automatic library-sync regression to assert that the
  project-named portable profile contains the refreshed source identity and no
  longer contains the stale source identity.
- 2026-07-29 `d218362` `fix: clarify simulation analysis actions`:
  makes first-time analysis and incremental rescans explicit, disables the
  primary action when all local corpus files have current reports, preserves
  signature/content-based zero-call rescans, and automatically creates or
  replaces the project-named portable library profile only after analysis or
  synthesis actually changes the image. Focused simulation, full Web, frontend
  helper, production build, and visual layout checks pass. The running Web
  service was intentionally not restarted because Chapter 29 was actively
  generating.
- 2026-07-29 `4198c3d` `fix: repair co-create drafts after rollback`:
  clears deleted Foundation authority when rolling back to a co-create draft,
  safely repairs the same stale binding in already-rolled-back projects, and
  routes draft confirmation back through Foundation generation. Focused
  rollback, Foundation, and co-create planning regressions pass.
- 2026-07-29 `873408a` `fix: cancel resumes that race with pause`:
  adds a pause epoch across both automatic and externally initiated resume
  preparation, so any resume started before the latest pause cannot publish a
  new running lifecycle. Focused Host resume and Web pause tests pass.
- 2026-07-29 `705a515` `fix: align model probe with runtime streaming`:
  changes the provider connection test from non-streaming `Generate` to the
  same `GenerateStream` protocol used by real creation work. The probe still
  requests only an 8-token `OK` response, but now consumes the stream until a
  terminal `done` event and rejects provider errors or streams that close
  early. Four focused Host regressions pass, the Web UI and Go executables
  rebuild successfully, port 9898 ultimately restarts from that commit as PID
  37920, and the saved `deepseek-yuanyu-0/deepseek-v4-pro` configuration passes
  three consecutive live model tests plus a final post-deployment probe through
  the new path.
- 2026-07-29 `9ca248f` `fix: preserve pause during automatic recovery`:
  makes a manual pause claim the paused lifecycle while an automatic resume is
  between runs, and prevents the pending resume from overwriting that state.
  Focused Host state-machine and Web pause endpoint regressions pass.
- 2026-07-29 `c410b6f` `fix: streamline chapter de-ai recovery`:
  keeps the first and final consistency gates while allowing digest-bound
  `de_ai_batch_repair` checkpoints to re-run only `check_de_ai` inside the
  bounded repair loop. Transient gateway `Invalid Argument` retries increase
  from two to five attempts before Host redispatch. Affected bootstrap, agents,
  host-flow, and tools tests pass, including the full tools package.
- 2026-07-29 `f9280dc` `fix: clarify manuscript chapter selector`:
  replaces the always-editable manuscript chapter combobox field with a
  button-based select trigger and chevron, moves chapter search into the opened
  dropdown, preserves keyboard and filtered selection, updates component and
  browser regressions, and rebuilds embedded Web assets. Four focused Vitest
  cases, the production build, and an installed-Chrome fixture check pass.
- 2026-07-29 `1504126` `fix: distinguish temporary style edit busy state`:
  separates permanent post-writing style locks from temporary non-writing
  background activity, adds backend/frontend regressions, rebuilds the embedded
  UI, restarts port 9898, and verifies `插翅难飞v2` can be changed to
  `romance` after rollback.
- 2026-07-29 `4e201b1` `test: classify consistency model calls`:
  adds the existing `internal/tools/check_consistency.go` direct model boundary
  to the explicit production-call inventory. Full `internal/modeldiag` and
  `internal/host/reminder` package tests pass.
- 2026-07-29 `05de16a` `fix: make cloned projects rollback-ready`:
  strips copied process-local normal-flow leases before installing a clone,
  preserves co-create checkpoints while rollback can still reach the draft
  boundary, and removes them after prose writing makes that boundary obsolete.
  The full Web package passes in 157.9 seconds, the full Store package passes,
  and focused clone/rollback/checkpoint regressions pass.
- 2026-07-29 `this commit` `test: add offline simulation regression baseline`:
  adds a repository-original synthetic corpus, identity-driven fake-LLM
  analyze-to-gate evaluator, stable JSON/human reports, explicit payload and
  bounded-performance budgets, leakage/stale negative tests, a network-free CI
  job, and fixture/golden/provider-eval documentation. The evaluator passes
  9/9 invariants twice with identical normalized reports; focused Go tests and
  vet, 362 frontend tests, and the production UI build pass. Full `go test
  ./...` retains three unrelated existing failures in Web adaptation checkpoint
  preservation, Writer out-of-budget reminder routing, and model-call inventory.
- 2026-07-29 `this commit` `feat: surface simulation profile diagnostics`:
  replaces the compact snapshot profile with a bounded, redacted canonical
  diagnostics summary; exposes health, coverage, feature, contract, check,
  capability, and role/mode-preview state in the Web UI; distinguishes scan,
  report resynthesis, and source reanalysis actions; and reports portable
  library metadata without restoring local evidence. Focused Go and frontend
  tests, the production UI build, affected-package vet, and diff checks pass.
  The full Web suite retains an unrelated adaptation co-create checkpoint
  fixture failure; its clone-project failure passes when rerun alone.
- 2026-07-29 `this commit` `feat: gate simulation safety before chapter commit`:
  adds digest/revision-bound SimulationCheckReport receipts, an in-process
  normalized rarity-aware scanner with bounded/cancelable caches, truthful
  portable-only degradation, reinforced measurable-must checks, shared
  Writer/Editor tooling, and exact-draft commit invalidation. Focused
  simulation, store, tools, agents, assets, benchmark, and vet validation are
  recorded in `.codex/HANDOFF.md`.
- 2026-07-29 `this commit` `feat: add role-bound simulation contracts`:
  centralizes normal/reinforced feature budgets and health degradation, persists
  a digest/revision-bound SimulationContract, binds ContextTool instances to
  Coordinator/Architect/Writer/Editor roles, and derives reinforced co-create
  guidance from sanitized portable feature IDs. Domain, store, tools, host
  simulation, assets, agents, and affected vet checks pass. The full host suite
  retains its existing Windows atomic-file-lock timeout in an unrelated
  adaptation revision tamper test.
- 2026-07-28 `pending` `feat: make simulation analysis evidence-aware`:
  signs source analysis and synthesis layers, records deterministic source
  coverage/content health, aggregates stable portable features in code, binds
  checkpoints to canonical report identities and synthesis signatures, removes
  deleted-source evidence, and keeps proper nouns/rare phrases in a project-local
  safety index. Domain, simulation runner, simulation store, Host simulation,
  Web simulation, assets, tools, and affected vet checks pass. The bounded
  full store run timed out in an unrelated existing Windows `MoveFileEx`
  continuation test; the simulation-focused store suite passes.
- 2026-07-28 `this commit` `feat: add portable simulation profiles`:
  introduces strict `simulation_profile.v2` portable artifacts, separates
  source paths/reports into project-local evidence, preserves v1 loading and
  existing compact-context behavior through a compatibility adapter, and makes
  the Web profile library portable by default. Simulation-focused tests and
  affected-package vet pass. A broader affected-package run hit the existing
  Windows expansion-authority concurrency/DPAPI timeout; the named test passed
  when rerun alone.
- 2026-07-28 `current HEAD` `fix: allow canonical relationship endpoints`:
  permits the domain-owned `source_character_id` relationship endpoint through
  the normal-manuscript firewall while continuing to reject adaptation source
  collections. Focused firewall tests and `git diff --check` pass.
- 2026-07-28 `current HEAD` `fix: strengthen manuscript authoring safeguards`:
  requires explicit gender for non-decorative characters, adds a narrow
  approved-foundation repair path for legacy missing gender, preserves
  semantic sidecars during exact author-owned prose saves, strengthens
  continuity and de-AI checks, and refreshes the manuscript editor wording and
  production assets. Focused Go package tests, 360 frontend tests, the
  production UI build, and `git diff --check` pass.
- 2026-07-28 `d3f3cb4` `fix: export completed chapters during writing`:
  decouples preview export from the global project busy state and uses the
  standard browser download path instead of a native picker that silently
  aborts in automation-controlled Chrome. Running or paused projects can export
  their completed chapter subset without interrupting Writer. Frontend and
  backend regressions pass, the Web service was rebuilt, and live download was
  confirmed.
- 2026-07-28 `current HEAD` `fix: regenerate severely oversized drafts`:
  routes drafts more than 10% above the hard safety ceiling to a fresh Writer
  context instead of repeated segment compression. The replacement candidate
  targets the chapter's rolling recommended range while the wider hard range
  remains an audit-only fence. The old draft remains authoritative on rejection
  and is saved under `drafts/backups/` before a valid atomic replacement.
  Smaller deviations keep the quality-preserving segment path. Every completed
  de-AI repair batch now ends its Writer turn so the Host redispatches the
  mandatory consistency-first validation from clean context.
- 2026-07-28 `current HEAD` `fix: bound writer continuity reads`: permits one
  bounded adjacent-chapter read only before a new draft exists. Older history,
  and every prior-chapter read after the active draft is saved, now returns a
  successful lightweight redirect to existing `novel_context` or the current
  draft instead of loading more prose. Writer tool tests and the full
  `internal/agents` suite pass.
- 2026-07-28 `current HEAD` `feat: add full-stage model quality benchmark`:
  extends the existing context benchmark with a resumable `stage-quality-v1`
  suite covering all eight configurable stages, reasoning-aware arbitrary/all
  configured model selection, deterministic hard checks, blinded independent
  judging, sanitized reports, and three separate Writer validation/tool-repair
  diagnostics. Also adds `writing-length-ab-v1` with explicit 2600–3200,
  4000–4500, and 5000–5500 targets, a hard minimum-length gate, and a soft
  upper target. The final three-way run ranks Grok xhigh 90.9, Grok high 90.7,
  and DeepSeek 75.8 overall; high wins six stages while xhigh wins writing and
  character analysis. The length stress test ranks high 59.1, xhigh 55.1, and
  DeepSeek 50.2 after minimum-length gating.
- 2026-07-28 `current HEAD` `fix: reject stale de-AI recovery tasks`:
  makes persisted de-AI recovery conditional on a same-draft
  `pending_de_ai_repair`; when that payload is absent or the repair receipt is
  stale, Writer is explicitly routed through `check_consistency` before
  `check_de_ai` and may repair only from the new report. Full host-flow and
  tools package tests pass.
- 2026-07-28 `current HEAD` `fix: retain compacted writer error fingerprints`:
  preserves only a normalized error-family fingerprint when Writer tool results
  are microcompacted. Repeated exact-quote and other contract errors now count
  across context compaction, so changing array indexes cannot bypass the
  clean-context recovery threshold. Targeted Writer-phase and full agents tests
  pass.
- 2026-07-28 `current HEAD` `fix: order de-AI repair validation gates`:
  corrects the contradictory `repair_de_ai_batch` receipt that told Writer to
  call `check_de_ai` even though every prose mutation invalidates the required
  consistency receipt. Repair batches now direct `check_consistency` first,
  then `check_de_ai`, then adaptation when applicable. Targeted repair tests
  pass; the only warning was a post-test Windows cache-cleanup file lock.
- 2026-07-28 `current HEAD` `fix: restart writer after failed clean recovery`:
  terminates the current Writer dispatch when repeated contract errors recur
  after its one clean-context recovery, forcing Coordinator to create a fresh
  Writer from durable state instead of carrying the second polluted repair
  transcript. Writer package tests pass. Live chapter 12 recovery subsequently
  corrected one isolated quote error, passed consistency, and advanced through
  de-AI repair without returning to the five-error loop.
- 2026-07-28 `3bbe73ff` `feat: support Grok 4.5 xhigh reasoning`:
  adds `xhigh` to the Grok 4.5 compatibility provider capability and request
  contract, exposes it only for Grok 4.5 in model and stage reasoning selectors,
  and updates the settings guidance. Bootstrap tests and the 20 model-add UI
  tests pass; the production UI build passes with only the existing large-chunk
  warning. After restart, a real project Grok OAuth request with `xhigh`
  returned `model test passed`; the Grok model default and all seven current
  Grok-backed stage routes were persisted as `xhigh`. Live chapter 12
  acceptance remains paused at its durable de-AI repair checkpoint for the
  user to resume.
- 2026-07-28 `current HEAD` `fix: remove manuscript end annotation`:
  removes the redundant “已显示本章真正结尾” helper text while preserving
  complete-chapter rendering and completed-only previous/next chapter controls.
  The targeted manuscript test and production UI build pass. Runtime acceptance
  reached committed chapter 11 (volume 1 complete); chapter 12 remains
  uncommitted because both configured model backends became unavailable. The
  rebuilt Web service is ready on port 9898, the project remains safely idle,
  and the served production bundle no longer contains the removed annotation.
- 2026-07-28 `2e61e8ae` `fix: recover writer errors and manuscript views`:
  replaces repeated Writer tool-contract repair loops with a clean
  durable-state context after limited self-correction, prevents failed
  validation calls from advancing phase compaction, publishes the exact legal
  dynamic character-state fields to commit, renders complete manuscript
  chapters with completed-only chapter navigation, and makes outline loading
  recover from aborted requests without leaking stale chapter data. Focused
  Writer/Tools tests, 74 manuscript UI tests, the production UI build, service
  restart, and repeated real-Chrome chapter/outline switching pass.
- 2026-07-28 `ce83f31` `docs: finalize character workflow migration`:
  publishes the remaining Character Agent migration documentation, relaxes
  adaptation character-brief compatibility for projects without legacy
  CoreCast signatures, clears only stale confirmed adaptation artifacts during
  proposal confirmation, stabilizes confirmation/revision-fence tests, and
  keeps Go test temporary files under the repository cache. Focused store,
  adaptation, host, and model-inventory tests pass.
- 2026-07-28 `f0c72954` `fix: accept exact evidence across paragraphs`:
  keeps consistency evidence text-and-punctuation exact while ignoring only
  Unicode whitespace, so an otherwise verbatim quote spanning Markdown
  paragraph breaks is not falsely rejected; paraphrases remain rejected.
- 2026-07-28 `ffe9bb02` `fix: pin active scene validation schema`:
  publishes the active chapter's exact planned-scene count and compact indexed
  contracts in `check_consistency` JSON Schema, with equal `minItems` and
  `maxItems`, so Writer cannot confuse prose subsections with the four planned
  scenes while the strict evidence and mismatch gates remain unchanged.
- 2026-07-28 `18521edd` `fix: preserve chapter-scoped writer validation`:
  gives Writer a 96 KiB diagnostic boundary for one active chapter, its
  canonical work package, and validation schemas while retaining the 60 KiB
  boundary for Coordinator and planning agents. This prevents a valid
  6,573-character draft review from being mistaken for whole-book context
  overflow without allowing long novels to inject full-book prose.
- 2026-07-28 `current HEAD` `fix: target chapter length before generation`:
  makes Writer allocate the first draft inside the current recommendation and
  leave headroom below the user chapter range instead of generating an
  oversized chapter for Host to trim afterward. The user range and approximate
  book total remain planning guidance; only a wider 2/3-to-3/2 runaway safety
  fence triggers segmented recovery, so the preserved 6,573-character chapter
  4 draft proceeds to quality validation instead of another deletion loop.
- 2026-07-28 `pending` `fix: stabilize resumed chapter validation`:
  restores the required consistency-before-de-AI validation order for resumed
  drafts, requires planned-scene evidence to follow narrative order, and bounds
  scene-contract error feedback so a recoverable scene-count mistake cannot
  inflate the Writer context with repeated full outline payloads. A fresh
  recovery turn now retains its newly loaded chapter contract despite older
  validation receipts, and wholly missing planned scenes can be recorded as
  blocking findings instead of entering an impossible exact-quote retry loop.
  Each new Host Writer dispatch also replaces stale prior Writer turns while
  retaining in-dispatch reminders, preventing old failed validation attempts
  from crossing the provider's compiled-request byte limit.
- 2026-07-28 `current HEAD` `fix: separate chapter word guidance from hard limits`:
  treats the dynamic per-chapter allocation and approximate book total as soft
  pacing guidance, while the saved user chapter range remains the only creation
  hard gate. Full reads, edits, commits, Writer stop guards, and Host recovery
  now agree on that policy, preventing quality-reducing trim loops when a
  complete chapter reasonably exceeds its rolling recommendation.
- 2026-07-28 `3d25cb0` `fix: isolate chapter execution context`:
  keeps raw startup preferences durable for planning but omits their duplicate
  replay once a signed chapter outline exists, retains validator-owned
  structured rules, and bounds generic Writer references to representative
  guidance. No unique chapter, character, or project fact is removed; tools,
  Writer, and flow suites pass.
- 2026-07-28 `2fcf577` `fix: deduplicate writer character context`:
  removes the legacy full-character mirror beside the authoritative bounded
  character workset. The live chapter-one package falls from 41,871 bytes to
  23,926 bytes without removing any unique outline, role, rule, or imitation
  technique; tools, Writer, and flow suites pass.
- 2026-07-28 `87aff42` `fix: clear stale generation sessions on rollback`:
  makes rollback to a planning review remove Coordinator and subagent
  generation histories while preserving the full co-create transcript. This
  prevents a restarted Writer from inheriting characters and prose from an
  abandoned novel after a transactional rollback. The targeted preservation
  regression and the full `internal/store` suite pass.
- 2026-07-28 `e0d3f281` `fix: bind prose generation to chapter contracts`:
  bounds production-sized multibyte Writer references without dropping story
  facts, blocks Writer access to planning/status payloads, marks the active
  chapter package as authoritative, rejects drafts and commits that contain no
  canonical character required by the saved outline, and prevents a
  male/female viewpoint inherited from imitation material from overriding the
  chapter's planned POV. Focused Writer, context, simulation, tools, and flow
  suites pass. The live project was transactionally rolled back from two
  contaminated prose artifacts to the intact 55-chapter outline review gate;
  full-book prose acceptance remains in progress.
- 2026-07-27 `pending` `fix: bound high-fidelity detail planning`:
  adds an arc-scoped `planning_detail` context that keeps every canonical
  character, relationship, rule identity, current-volume skeleton, adjacent
  handoff, and passed volume-audit requirement while scaling independently of
  total book length. Structured scene objects are deterministically preserved
  in the durable string-scene schema, detail generation/audits route through
  bounded contexts, and the live `插翅难飞` V1/A1 context is 36,168 bytes
  instead of replaying the complete planning history. Tools, flow, and agent
  suites pass; live 55-chapter generation validation remains pending.
- 2026-07-27 `2548860d` `fix: preserve long-form co-create planning context`:
  restores the confirmed Character/Foundation workflow without discarding prior
  co-create decisions, bounds transient coordinator/subagent receipts, and makes
  original volume planning/audit read durable scoped context instead of
  recursively embedding prior drafts and audit JSON. The live `插翅难飞`
  project retains revision 3 with 4 complete roles, 6 relationships, and 25
  rules; it generated and independently passed all 5 volume skeletons (55
  planned chapters), survived a Web service restart, and stops at the explicit
  volume-review gate with zero detailed chapters. Stress coverage preserves
  audit/index facts for a 5,000,000-word, 160-volume plan within bounded model
  context. Focused agents/flow/tools/store tests, frontend tests/build, live
  browser checks, and restart persistence checks pass.
- 2026-07-27 `2ed4f77` `fix: deduplicate co-create request context`:
  canonicalizes the transient co-create model view on every round while
  preserving every user turn, every visible Agent reply, and the latest
  complete cumulative draft. Superseded draft copies, legacy cast JSON already
  persisted in the Character workspace, and stale suggestion payloads are no
  longer resent. The live `插翅难飞` request fell from a rejected 61,618 bytes
  to a completed 12,723 bytes; visible recovery, final restart persistence,
  focused Host/Web tests, UI/Go builds, and runtime smoke checks passed.
- 2026-07-27 `pending` `feat: add character card workspace`:
  replaces the split core/all-character screens with one stable-ID character
  card workspace, including v3 field groups, search/filter/sort, guarded
  duplicate/delete/high-risk edits, and responsive accessible navigation.
  Integrates the durable Character Agent analyze/review/retry/discard APIs with
  target-only digests, non-destructive polling, selective candidate acceptance,
  finding navigation, stale-review signaling, and read-only adaptation source
  evidence. Vitest passes 344 tests, the production UI build passes, and the
  focused Playwright desktop/mobile suite passes 20 tests.
- 2026-07-27 `pending` `feat: expose durable character agent workspace`:
  adds persistent, restart-recoverable Character Agent analyze/review runs with
  exact idempotency, scoped target-only drafts, single-active-run exclusion,
  safe failure/retry/discard receipts, model-route telemetry, and bounded
  status projections. Strict Foundation Character HTTP APIs launch the existing
  Character subagent through background actions without routing orchestration
  through a model; unknown/source-controlled inputs are rejected. Foundation
  preview/apply now requires an exact current passing independent Character
  review for character or relationship changes. Focused fake-agent,
  restart/exclusion, strict-HTTP, retry, Character regression, and Foundation
  regression tests pass, as do CLI/Web compile gates and modified-package vet.
  The project wrapper still exits 1 after successful commands because the
  pre-existing live Web process locks `web-9898.stderr.log` during cleanup.
- 2026-07-27 `pending` `feat: integrate characters into writing loop`:
  gives outline entries, chapter contracts, review findings, summaries,
  snapshots, relationships, and state changes stable Character IDs; builds a
  deterministic ID-first 16 KiB chapter workset with bounded full/compressed
  cards, relevant dynamic state, and visible static/dynamic conflicts; and
  validates knowledge, voice, motivation, relationship, arc, supporting-role,
  and adaptation-source constraints before commit. Repeated temporary roles
  enter a durable independent Character analyze/review promotion workflow with
  idempotent explicit confirmation, and the writing router blocks Writer until
  the same Character Agent owns and completes that gate. Focused domain/store/
  tools/flow tests, modified-package vet, and the CLI build pass. The full
  repository run reproduces the documented assets, Web/CoreCast,
  Host/adaptation, model-inventory, and five Tools baseline failures.
- 2026-07-27 `pending` `feat: integrate adaptation character workflow`:
  imports canonical full Character profiles with strict legacy migration,
  derives a deterministic bounded source-character index, and gates every
  decision-required core or non-core source role behind explicit
  keep/rename/merge/split/exclude mappings. Adaptation co-create now routes the
  same Character Agent through analyze, independent review, explicit
  confirmation, and only then Target Foundation generation; the complete
  reviewed cast and relationships survive publication, SourceFoundation stays
  read-only, and the lifecycle is rebound to the new canonical revision.
  Focused schema, alias/homonym, merge/split/exclude, coverage, restart,
  idempotency, stale-input, legacy, Web-routing, and full-cast fixtures pass;
  `go build ./cmd/ainovel-cli` passes. The full repository run reproduces the
  documented assets, Web/CoreCast, Host/adaptation, model-inventory, and five
  Tools baseline failures.
- 2026-07-27 `pending` `feat: integrate original character workflow`:
  changes normal co-create to a four-section creative brief while migrating
  legacy cast output as an unconfirmed seed; stages full Character candidates,
  runs an independent signature-bound review, waits for explicit user
  confirmation, deterministically projects/confirms CoreCast, then publishes
  the exact full candidate behind the active Foundation generation fence.
  Architect is blocked from character/relationship ownership until the
  Character gate passes. Durable candidate/lifecycle state, restart-safe exact
  confirmation retries, Web edit/confirmation APIs and snapshot status, focused fake
  model/routing/conflict/staleness tests, Go build, and the canonical Web UI's
  330 tests/build pass. Five inherited `internal/tools` baseline tests remain
  red (four Foundation-gate fixtures and one mature-context byte cap).
- 2026-07-27 `pending` `feat: add independent character agent`:
  registers one Character subagent whose `analyze` and `review` modes run as
  separate submissions routed through `character_analysis` and
  `character_review`; adds a bounded signature-bound context tool plus strict
  candidate/review save tools with revision fences, exact idempotency,
  staleness rejection, deterministic completeness downgrades, and unified
  original/adaptation source mappings; adds model/profile/prompt budgets,
  config and Web model-route data sources, prompt guidance, embedded UI assets,
  and focused acceptance coverage. The focused nine-package matrix, non-Web
  modified-package suites, `go build ./...`, Web production build, and all 330
  Vitest cases pass. Full Web regression still has inherited CoreCast-gate
  fixture failures; a combined full run's host package was stopped after it
  stalled.
- 2026-07-27 `pending` `feat: add complete character card contract`:
  upgrades StoryFoundation to schema v3 with contrast, key-backstory,
  chapter-zero initial-state, and knowledge-boundary semantics; adds one
  deterministic tiered completeness API; adds a signature-bound lifecycle,
  review-finding, confirmation, and full-cast source-mapping sidecar contract;
  preserves supporting cast when publishing CoreCast; and covers v1/v2
  read-only compatibility, idempotent migration, cloning, JSON/signature/diff,
  staleness, source mapping, CAS persistence, and legacy snapshots/relations.
  Focused domain/store tests and the CLI build pass. The full repository run
  reached existing Web failures caused by missing CoreCast gate fixtures and
  an unavailable expansion-auditor command; no PR-01 package failed.
- 2026-07-20 `81d7396` `fix: prevent foundation tabs from shrinking`:
  all direct Foundation-center chrome outside the scrolling panel now keeps its
  intrinsic height, and the tab row has a 38px minimum height. This prevents
  `概览` / `核心角色` and the other tabs from being vertically clipped when the
  workflow header leaves limited space. The exact live 2048x1024 layout changed
  from a 10.2px tab row with `fullyVisible=false` to 42px with
  `fullyVisible=true`; all 14 desktop/mobile Foundation browser cases passed.
  `main` was pushed and Web restarted on port 9898 as PID 34384.
- 2026-07-20 `08f398b` `fix: show complete source roles without layout overlap`:
  Foundation CoreCast fallback cards now preserve and display the analyzed
  source character's role, description, traits, arc, goals, motivation,
  conflict, voice, constraints, faction, and notes instead of reducing each
  character to a name and aliases. The Foundation center now keeps its header
  and action footer in normal layout while only the content panel scrolls, so
  neither the top title nor the final role/rule content is covered. All 330 UI
  tests, the production build, and 14 desktop/mobile Playwright cases passed;
  live `梦中` validation found four detailed source roles and clean bottom
  geometry. `main` was pushed and Web restarted on port 9898 as PID 9376.
- 2026-07-20 `9e4eac1` `fix: expose analyzed source roles in foundation center`:
  Foundation state now recognizes a prepared adaptation as soon as its source
  manifest exists, instead of waiting for `plan.json`. Before target CoreCast
  generation, the center exposes the read-only SourceFoundation, lists source
  character dossiers, and shows source-role candidates in the CoreCast tab
  without persisting them as target-story roles. Host/Web tests, all 329 UI
  tests, the production build, a `main` push, restart, and live `梦中` API/asset
  probes passed; Web is running on port 9898 as PID 31548.
- 2026-07-20 `2513dfa` `feat: redesign core cast workspace`: moves editable
  CoreCast content and the long AI draft out of the right inspector into the
  central co-create workspace. Roles, planned relationships, and adaptation
  source dispositions now have separate views with direct pagination and only
  one item rendered per page. Raw relationship JSON and visible English enum
  labels are replaced by structured Chinese forms, completeness badges, editing
  guidance, and clear save/confirm semantics. All 328 UI tests and the production
  build pass; live Playwright validation on `重生` confirmed five-role and
  six-relationship pagination. Web was rebuilt and restarted on port 9898.
- 2026-07-20 `f5d3264` `fix: recover malformed cocreate responses`: ordinary
  co-create now gives malformed XML/CoreCast JSON and length-truncated protocol
  responses up to two bounded repair attempts. The system prompt enumerates the
  exact CoreCast field/type contract, requires non-empty character constraints
  and relationship coverage, and the model boundary normalizes common mode
  aliases while formal persistence remains strict. Focused `TestCoCreate` tests
  pass. Live recovery of the existing `重生` prompt produced a ready 300,000-word
  plan with five constrained characters and six relationships; Web was rebuilt
  and restarted on port 9898.
- 2026-07-19 `21e4082` `fix: repair word budget without rewriting drafts`:
  normal `draft_chapter` and `commit_chapter` word-budget rejections no longer
  instruct Writer to call `draft_chapter(mode=write)` and replace the entire
  chapter. Both preserve the persisted draft, allow at most one current-draft
  read when the active turn does not already have it, require grounded atomic
  `edit_chapter(edits=[...])` adjustments, report the current word count, and
  rerun de-AI/consistency/adaptation gates on the resulting version. This
  removes the lower-level tool receipt that overrode the Host recovery contract
  after chapter 45 reached commit at 4,269 words and sent Writer back toward
  planning/full rewrite. Focused ten-run word-gate regressions, full Tools,
  Agents, Host Flow, and Host suites, vet, and diff checks passed.
- 2026-07-19 `c75104b` `fix: allow granular writer recovery batches`:
  the atomic `edit_chapter` batch now accepts up to 24 non-overlapping local
  edits. Live Writer analysis found that preserving every plot beat, key
  exchange, emotional landing, and hook while removing roughly 600 redundant
  runes required about 20 small cuts; the former 12-item cap forced it to keep
  reconsidering a few overly broad cuts instead of invoking the tool. The
  larger batch keeps the mutation atomic and exact while favoring many gentle
  edits over quality-reducing deletions. Twenty-edit ten-run regressions, full
  Tools, Agents, Host Flow, and Host suites, vet, and diff checks passed.
- 2026-07-19 `6665cc6` `fix: retain writer recovery across dispatches`:
  Host Resume now opens a durable-draft recovery lease in the Flow dispatcher.
  Every subagent boundary in that resumed run continues through `RouteResume`
  until the draft is committed or the workflow leaves the recovery state; a
  Writer that returns analysis without completing its edit can no longer make
  the next dispatch fall back to generic “write chapter.” Repeated recovery
  notes also preserve all read/context prohibitions instead of granting the
  ordinary repeat permission to reload `novel_context`. The word-budget repair
  task now requires silent selection followed immediately by one atomic batch
  edit, preventing a long visible paragraph-by-paragraph plan from consuming
  the tool-call turn. This fixes the live fallback that reread chapter 44 and
  chapter 45 several times after the first recovery Writer stopped without an
  edit. Focused ten-run regressions, full Host Flow, Host, Tools, and Agents
  suites, vet, and diff checks passed.
- 2026-07-19 `324f17f` `fix: batch writer recovery edits before validation`:
  `edit_chapter` now accepts an optional atomic batch of 1-12 unique,
  non-overlapping exact replacements after a single grounded chapter read.
  Every single or batched edit reports the current draft rune count and the
  same dynamic budget used by the commit gate; an out-of-budget receipt tells
  Writer to reuse the original read evidence instead of reading the whole
  chapter again. The recovery route explicitly selects that batch form and
  forbids budget-confirmation rereads. Existing single-edit calls, normal new
  chapters, and chapter-level polishing remain unchanged. This fixes the live
  recovery where a first 29-rune trim returned no remaining budget, causing
  Writer to reread all 4,263 runes and begin rebuilding the same context loop.
  Focused ten-run regressions, the full Tools, Agents, Host Flow, and Host
  suites, vet, and diff checks passed.
- 2026-07-19 `de0497b` `fix: restore writer within current word budget`:
  the one-shot Writer recovery state now loads the canonical draft rune count
  and the exact dynamic chapter range used by the commit gate. If an
  interrupted draft is outside that range, recovery instructs one grounded
  `edit_chapter` pass over the existing draft, preserving every key plot beat,
  character choice, emotional payoff, and ending hook while removing only
  redundant material (or filling a necessary bridge). It explicitly forbids a
  whole-chapter rewrite. Normal new-chapter routing and chapter-level polishing
  are unchanged. This fixes the live chapter 45 recovery whose stale draft was
  4,292 words against a 2,550-3,703 range and otherwise would reach the commit
  gate without enough information to repair locally. Focused ten-run
  regressions, the full Host and Host Flow suites, vet, and diff checks passed.
- 2026-07-19 `ce08323` `fix: resume writer from durable draft stage`:
  the one-shot post-Resume route now inspects the in-progress canonical draft,
  latest chapter checkpoint, current-draft de-AI audit, and consistency digest.
  When a draft exists it dispatches a validation-stage recovery contract that
  forbids `plan_chapter`, `draft_chapter`, and unrelated chapter reads; it
  resumes bounded de-AI repair, then performs one project-context load and
  same-draft consistency checks before commit. Normal new-chapter and ongoing
  synchronous routes are unchanged. This fixes the live interruption where an
  existing chapter 45 draft at `de_ai_check` was redispatched as generic
  “write chapter 45,” moved backward to `plan`, and risked another whole rewrite.
  Focused ten-run regressions, the full Host Flow suite, vet, and diff checks
  passed.
- 2026-07-19 `58f18c5` `fix: evict late writer project context`:
  once Writer validation has begun, `novel_context` results are no longer
  eligible for the protected recent-result slots; current-draft evidence and
  validation receipts take priority. Overflow recovery applies the same rule
  even when the oversized project context is the newest tool result. This
  fixes the live recovery sequence whose late full context expanded the next
  request from 39,867 to 188,544 bytes and remained 186,268 bytes after the
  former recovery pass. Chapter prose, tool arguments, and validation reports
  remain intact, with durable project data still available in Store. Focused
  ten-run regressions, the full Agents suite, vet, and diff checks passed.
- 2026-07-18 `d926ade` `fix: tolerate stale de-ai batch entries`:
  `repair_de_ai_batch` now visibly skips only entries whose `old_string` is
  absent from the current draft, continues applying the remaining unique
  non-overlapping replacements, and requires an immediate fresh de-AI check.
  A fully stale batch is a non-mutating structured no-op. Ambiguous matches,
  overlapping patches, duplicate inputs, and malformed replacements remain
  hard failures. This fixes the live iterative repair where one sentence from
  an already-applied batch aborted seven still-valid replacements. Focused
  ten-run regressions, the full Tools suite, vet, and diff checks passed.
- 2026-07-18 `ed6f1ea` `fix: preserve latest writer write receipt`:
  persisted Writer body calls are still removed as complete protocol pairs so
  full chapter arguments and fabricated placeholder arguments never cross the
  next provider boundary. The newest completed write result is now retained as
  an ordinary, explicitly non-callable receipt containing its exact structured
  outcome and `next_step`. This fixes the live chapter 45 loop where a valid
  3,368-word draft and its `word_budget_passed=true` instruction disappeared,
  causing unnecessary 4,205- and 4,956-word rewrites. No prose, plan, or
  validation evidence is discarded. Focused ten-run regression, the full
  Agents suite, vet, and diff checks passed.
- 2026-07-18 `6824950` `fix: drop completed writer tool rationale`:
  Writer phase compaction now removes private rationale from an assistant turn
  once every tool call in that turn has a completed result, while preserving
  the schema-valid call arguments and result receipts. The live chapter 45
  failure retained about 11.5k characters of repeated deliberation beside an
  already-materialized `plan_chapter` call, inflating the following request
  from 57,191 to 72,136 bytes against a 61,440-byte boundary. This change does
  not remove chapter text, plan arguments, receipts, or project context.
  Focused ten-run regression, the full Agents suite, vet, and diff checks
  passed.
- 2026-07-18 `afaa00b` `fix: expose recovered polish draft state`:
  single-chapter `read_chapter` results now include a hash of the complete
  canonical content. Draft reads also expose the final hash,
  `differs_from_final`, and an explicit `polish_state`, so a resumed Writer can
  preserve an already-polished draft and proceed to same-version validation
  without repeatedly rereading and comparing the whole chapter. No chapter
  content is compressed or omitted by this change. Focused ten-run
  regressions, the full Tools suite, vet, and diff checks passed.
- 2026-07-18 `c44fb79` `fix: make repeated chapter edits idempotent`:
  `edit_chapter` now returns `already_applied=true` when and only when the full
  non-empty replacement already exists and the requested old text is gone.
  Recovery retries therefore continue to validation without an ERROR, while
  unrelated missing-text edits remain hard failures. Focused ten-run
  regressions, the full Tools suite, and vet passed before live redeployment.
- 2026-07-18 `c90c3eb` `fix: require targeted polish before commit`: Writer
  polish dispatches now require at least one review-grounded `edit_chapter`
  change before validation and commit. An unchanged polish draft returns a
  structured, non-error rejection with the same targeted guidance; it no
  longer orders `draft_chapter` whole rewrites or sends a passed de-AI report
  to `repair_de_ai_batch`. Recovery dispatches also preserve a draft that
  already contains a prior local edit instead of demanding an extra edit just
  to satisfy a call count. Focused ten-run regressions, full Tools/Host flow
  suites, and vet passed before live redeployment.
- 2026-07-18 `ade5c27` `fix: preserve valid writer tool history`: Writer
  context compaction now keeps the original schema-valid arguments for bounded
  read and validation calls. Persisted draft/edit exchanges are removed as
  complete call/result pairs after their data reaches the Store, instead of
  rewriting every historical call to the invalid
  `{"_context_compacted":true}` sentinel that weaker models copied into new
  tool calls. Agent, Tools, and Host flow tests plus vet passed before live
  redeployment.
- 2026-07-19 StoryFoundation development batch, PR-03 through PR-08, integrated
  onto the latest `origin/main`: PR-03
  `4c574fd7` adds the original Foundation checkpoint; PR-04 `0c721f7c` adds the
  adaptation source/target checkpoint; PR-05 `35702fc1` adds the normal
  persisted preview/apply/retry and planning-repair lifecycle; PR-06
  `42074655` adapts that same lifecycle to target-only adaptation revisions;
  PR-07 `82a1d618` adds the dual-mode Foundation Center; PR-08 `a447261c` adds
  the planned-relationship graph, schema-v2 migration, E2E, recovery, release
  assets, and licensing. After integration, the focused Foundation/CoreCast/
  clone/rollback/migration tests passed in domain, store, host, and Web; all
  323 UI tests, production UI build, Go build, vet, and diff checks also
  passed. The unfiltered Go suite still has the previously recorded legacy
  fixtures that do not establish the new confirmation gates, plus the known
  tools context-budget baseline; see
  `.codex/pr-pipeline/story-foundation/PR-08-dev-summary.md`.
  The final PR-08 build was deployed from its worktree to the unchanged
  `novels-preview` runtime root; Web smoke validation passed on port 9898.
- 2026-07-18 `f9c7738` `fix: preserve targeted polish at word gate`: when
  an already-completed chapter misses its configured word range during
  polishing, the structured rejection now tells Writer to retain the complete
  draft and make local `edit_chapter` expansions or cuts, then rerun the normal
  gates. It no longer orders a whole-chapter rewrite for a small length delta;
  normal first-pass creation keeps the existing strict whole-draft guidance.
  Focused five-run regressions, full Tools tests, vet, and diff checks passed.
- 2026-07-18 `c691a6c` `fix: sync canonical chapter edits`: `edit_chapter`
  now synchronizes the migration-aware canonical draft before and after the
  legacy-path edit operation. Previously the tool reported a successful diff
  only in `drafts/{chapter}.draft.md`, while `read_chapter`, validation, and
  commit continued reading the unchanged canonical draft. The resulting false
  retry loop eventually triggered whole-chapter rewrites and context overflow.
  A migrated-structure regression proves the edited canonical draft survives a
  reopen even when the legacy mirror is deliberately made stale. Full Tools
  tests, focused five-run regressions, vet, and diff checks passed.
- 2026-07-18 `90a0fc0` `fix: bound arc review dispatches`: arc-end Editor
  reviews now use a 5,000-rune whole-chapter batch budget instead of 12,000
  runes. Each routed batch explicitly reads only its assigned final chapter
  range, forbids `novel_context` and unrelated chapter reads, and persists the
  batch review immediately. This fixes the live V4/A2 request that combined
  chapters 41-44 with extra context into about 69.9 KiB. Writer drafting and
  de-AI repair behavior are unchanged. Full Store, Flow, and Tools tests,
  focused five-run regressions, scoped vet, and diff checks passed.
- 2026-07-18 `d19f201` `fix: infer writer chapter tool defaults`: Writer-only
  chapter tools now infer the active chapter when the model omits it. Its
  `read_chapter` also defaults the active chapter to `source=draft` and prior
  chapters to `source=final`, while Editor and other roles retain their strict
  schemas. This removes the live post-repair loop of rejected empty/missing
  `source` reads and intermittent missing-chapter edit/check calls. Agent tests
  and vet passed.
- 2026-07-18 `664a41d` `fix: reserve long draft recovery headroom`: stored-draft
  recovery is now source-bounded to 8 KiB and omits character snapshots and
  whole-book style statistics already observable in the full current draft.
  It retains the signed outline/contract, user rules, word budget, and compact
  consistency/quality/anti-AI constraints. This closes the remaining live
  66-67 KiB boundary crossings after chapter 41 grew during quality repair,
  without truncating the draft. Focused and full Tools/Agents tests plus vet
  passed.
- 2026-07-18 `084c50e` `fix: source-bound stored draft recovery`: a restarted
  normal-writing chapter with an existing uncommitted draft now enters a
  dedicated `recovering` context profile. The package keeps the current
  outline, signed chapter contract, draft metadata, user rules, compact
  character/style evidence, consistency/quality/anti-AI constraints, and word
  budget; it omits drafting-only chapter plans, future promises, repeated
  continuity memory, and simulation material because the full current draft is
  read separately. The source package is regression-bounded to 16 KiB. This
  addresses the live intermediate-state loop where full chapter-41 draft plus
  normal drafting context produced 71-75 KiB requests. Tools/Agents tests and
  vet passed.
- 2026-07-18 `c645a63` `fix: stop prior chapter reread loops`: the Writer-only
  prior-chapter wrapper now replaces the generic truncated-text instruction to
  "reread with a higher limit" with an explicit bounded-continuity receipt:
  the 600-rune tail is complete for this purpose, must not be retried, and the
  next action is planning or drafting. This removes the contradictory guidance
  that caused repeated chapter-40 draft reads even after context growth was
  bounded. Focused Agent tests and vet passed.
- 2026-07-18 `d2b8c05` `fix: retain authoritative writer package`: duplicate
  pre-draft `novel_context` calls now preserve the first authoritative work
  package and clear the later duplicate result plus its lookup rationale. Once
  a draft is persisted or validation starts, the normal newest-receipt policy
  still applies. This addresses the live 56,210 -> 67,336 byte jump caused by
  retaining the newest duplicate package rather than the original one.
  Focused Agent tests and vet passed.
- 2026-07-18 `e583702` `fix: bound writer continuity evidence`: normal Writer
  turns now keep the active `novel_context` package plus only the newest
  prior-chapter continuity read. Repeated reads of chapters 38/39/40 no longer
  accumulate across provider calls, while distinct adaptation-source chapter
  reads remain intact. Live diagnostics had confirmed the initial chapter-41
  package was reduced to 22,931 bytes but repeated history lookups still grew
  51,934 -> 61,295 -> 64,201 bytes; the new semantic evidence slot fixes that
  remaining source of unbounded growth. Focused Agent tests and diff checks
  passed before deployment.
- 2026-07-18 `8e844bf` `fix: bound writer chapter evidence`: normal Writer
  chapter work packages now source-bound references and one representative
  signal per style/pacing category to a 28-KiB ceiling. Prior-chapter reads are
  continuity tails capped at 600 runes because the active package already
  includes `previous_tail` and recent summaries, and repeated current
  `novel_context` results are semantically deduplicated before the provider
  boundary. This addresses the live chapter-41 55,111 -> 89,059 and 57,492 ->
  69,420 byte sequences without removing the active chapter contract, outline,
  continuity state, user rules, style constraints, or deterministic checks.
  Full Agent/Tools tests and vet passed.
- 2026-07-18 `c410a7f` `fix: scope writer context to active chapter`: Writer
  can now load a full chapter work package only for the active work unit; a
  request for an earlier chapter returns a sub-2-KiB redirect to
  `read_chapter` instead of another contract/simulation bundle. Exact-boundary
  recovery retains only the newest durable result so an oversized pair cannot
  be retried unchanged. This addresses the live chapter-41 route that combined
  chapter-41 context, chapter-40 full context, and chapter-40 prose (70,205 ->
  69,931 bytes after the old recovery). Focused wrapper/recovery regressions,
  the full Agent suite, and vet passed.
- 2026-07-18 `fa6437f` `fix: evict persisted writer payloads`: Writer phase
  cleanup now removes completed draft/edit/repair prose from historical tool
  arguments after the Store confirms the write, clears matching old reasoning,
  skips results already compacted by an earlier phase, and repairs legacy
  sessions where only the result had been cleared. This addresses live
  48,536 -> 63,763 and 55,209 -> 64,457 byte regressions that result-only
  cleanup could not affect. Tests cover whole-payload reduction, idempotence,
  legacy cleanup, proactive validation transitions, and forced recovery; the
  full Agent suite and vet passed.
- 2026-07-18 `560b413` `fix: advance writer context by validation phase`:
  Writer now commits a deterministic phase transition as soon as consistency,
  adaptation, or de-AI validation returns. It keeps the two newest useful
  results and clears whole prior-phase context/draft payloads that remain
  durably readable from the project store. The same strategy now handles the
  exact 60-KiB overflow recovery path, replacing the ineffective full-summary
  retry observed live (61,482 -> 61,481 bytes). Synthetic boundary coverage
  verifies at least 20 KiB of post-transition headroom without truncating the
  active draft or its validation receipt; the full Agent suite and vet passed.
- 2026-07-18 `8277ee3` `fix: recover at exact production context boundary`:
  the exact 60-KiB byte guard now reports agentcore's recoverable context
  overflow sentinel after recording its rejected-budget diagnostic. Agentcore
  therefore force-applies Writer phase compaction and retries once instead of
  terminating the subagent before the token-window manager notices pressure.
  Exact under/over-boundary and phase-compaction regressions passed.
- 2026-07-18 `fa8cd158` `fix: compact writer results by validation phase`:
  Writer projection now retains the two most recent distinct tool results. Once
  consistency/de-AI validation advances, earlier chapter-context and draft
  echoes have completed their phase and are deterministically cleared from the
  next model request. This makes final request size independent of a lucky
  2-KiB margin while preserving the current validation receipts. The focused
  phase-compaction and Writer projection regressions passed.
- 2026-07-18 `a86826a` `fix: reserve polish validation headroom`: keeps one
  representative reinforced-simulation signal per prose/pacing category during
  polishing and shortens the duplicated anti-AI reference excerpt because the
  exact current-draft findings come from `check_de_ai`. The live chapter-39
  source payload is 18,920 bytes (down from 22,165) while its contract,
  continuity evidence, formal style rules, and deterministic validation remain
  intact. Focused context/simulation regressions passed.
- 2026-07-18 `989b74b` `fix: avoid duplicate consistency payloads`: changes
  `check_consistency` from a second copy of the complete draft and global state
  into a same-draft SHA receipt referencing the chapter contract, episodic
  evidence, and already-read draft. This preserves the semantic review inputs
  while preventing the final validation turn from re-adding roughly 8,000
  Chinese characters to the Writer request. The focused compact-receipt
  regression and full `internal/tools` suite passed.
- 2026-07-18 `654a3b4` `fix: route recovery context by agent role`: binds an
  empty Writer `novel_context` call to the single active/in-progress chapter,
  gives Coordinator status-only context by default, removes duplicated
  Architect top-level mirrors, and source-bounds structural planning inputs.
  On the live mature project, planning context falls from 130,710 to 39,370
  bytes while chapter-39 polishing remains 22,165 bytes. Dispatch start now
  marks the target Agent working before its first model delta. Full tool tests,
  Agent wrapper tests, and focused Host observer tests passed.
- 2026-07-18 `9accf0e` `fix: scope writer context by workflow`: gives
  polishing a chapter-owned source profile (current outline/contract, review
  brief, draft state, relevant continuity, prose/style constraints) instead of
  loading Architect planning and duplicate mirrors. The real chapter-39 tool
  payload falls from 128,720 to 22,165 bytes without post-build truncation;
  ordinary next-chapter writing is source-bounded to 31,008 bytes. Streaming
  model deltas now also mark the actual Agent working so the workflow badge
  follows the provider/model handling the live request before tool execution.
  Full tool tests, Agent/Web integration tests, focused Host/UI regressions,
  and the production UI build passed.
- 2026-07-18 `0fe1942d` `fix: show active backend route`: the running
  workflow badge now follows the most recently working Agent route and names
  that Agent (for example, 正文写作 or 流程协调), so its provider/model
  matches the request currently being handled instead of only the configured
  workflow stage. Automatic-switch events now classify real quota and timeout
  errors and avoid repeating the full switch summary in their detail. Focused
  Go and UI regressions plus the production Vite build passed.
- 2026-07-18 `pending` `feat: enforce confirmed core cast`:
  adds the five-section CoreCast contract, persisted semantic draft/source/intent
  gates, strict confirmation APIs, target-cast-authoritative Foundation
  publication, complete formal start/resume protection, structured adaptation
  mappings, and transaction-safe adaptation confirmation startup. Four fresh
  fix/re-review rounds closed bypass, overwrite, stale-binding, strict parsing,
  recovery, late-failure, and coordinator-start ordering findings. Focused
  changed-package/direct-boundary Go/UI tests, targeted race tests, Vite build,
  gofmt/diff and sensitive-path gates passed; unrelated full suites were not
  repeated under the user's incremental-validation rule.
- 2026-07-18 `a2acfaa5` `feat: establish canonical story foundation`:
  adds the canonical `StoryFoundation` domain and recoverable CAS-backed store,
  stable legacy migration/projections, planned-relationship isolation, and
  clone/rollback lifecycle safety. Three independent fix/re-review rounds
  closed stale-journal, orphan-projection, clone-snapshot, rollback concurrency,
  and test-assertion findings. Focused changed-package/direct-boundary tests,
  focused race tests with local WinLibs GCC, scoped vet, gofmt/diff checks, and
  sensitive-path scans passed; unrelated full-repository tests were intentionally
  not repeated. Publication must exclude unrelated base commit `94da71e1`.
- 2026-07-18 `2713b7b` `fix: honor configured backend routes on resume`:
  removes novel settings and chapter outlines from backend diagnostics, exposes
  stage routes as explicit `provider / model` choices even when providers share
  a model name, resets a fresh manual or scheduled resume to the saved primary
  route, and makes runtime failover attempt the selected candidate through the
  normal request path so switch and failure events stay visible. The affected Go
  regressions and vet, all 297 UI tests, production build, restart, live route
  repair for `重生v4`, and isolated browser acceptance passed. Port 9898 runs
  PID 41972 with 正文创作 inheriting
  `deepseek-suifeng / deepseek-v4-pro`.
- 2026-07-18 `0ed4996` `fix: prevent blank screen during project open`:
  normalizes explicit null snapshot/co-create inputs before evaluating global
  co-create visibility, covering the project-open reset render that previously
  dereferenced `snapshot.Phase` and emptied the React root. The null regression,
  all 296 UI tests, production build, restart, and an isolated live click on
  `重生v4` passed with chapter progress visible and no browser errors. Port 9898
  runs PID 46124.
- 2026-07-18 `81cce909` `feat: converge manuscript workspace actions`:
  widens and aligns the manuscript reading surface, adds an accessible chapter
  combobox with Arabic/full-width/Chinese-number lookup, persists bounded
  manuscript-action clarification in the right panel, preserves the existing
  revision/expansion audit and publication gates, removes the manuscript
  discussion jump, and keeps continuation Draft co-create inside continuation.
  All 296 UI tests, the affected Go packages and vet, the production build, 20
  manuscript workspace browser scenarios, 12 expansion browser scenarios,
  restart, and live route/static smoke checks passed. Port 9898 runs PID 30392.
- 2026-07-18 `dec625f` `feat: redesign manuscript workspace`:
  moves all manuscript selection and writing controls into the right tool panel,
  keeps formal/candidate/outline/review/history content in the central reader,
  automatically completes paginated chapters with a bounded DOM window, fixes
  the `重生v4` recovery false positive without weakening publication safety, and
  exposes the actual provider/model plus durable sanitized automatic-failover
  events. All 277 UI tests, the affected Go packages, the production build, 19
  desktop/mobile Playwright scenarios, restart smoke checks, recovery inspection,
  and live `重生v4` browser acceptance passed.
- 2026-07-18 `7992979` `fix: restore outline details and trash actions`:
  loads complete outline fields only when the writing-side chapter preview is
  explicitly opened, keeps large trash lists internally scrollable with the
  destructive action visible, and removes one obsolete smoke-test assertion
  that conflicted with the repository-local test-cache policy while preserving
  the end-to-end test. All 273 UI tests, the production build, the complete Web
  Go package, restart smoke checks, and live browser acceptance passed.
- 2026-07-18 `129648f` / `eba8e26` `fix: isolate workflow progress by project`:
  removes the project-keyed remount path that accumulated duplicate workflow
  cards across snapshot updates, resets retained progress when the project ID
  changes, and adds StrictMode component lifecycle coverage. The consolidated
  main history passes all 271 UI tests, the full Go suite, `go vet ./...`, the
  production build, and a four-step live project-switch DOM check. The rebuilt
  service is running on port 9898. Final runtime inspection found that the local
  project store had independently fallen from 25 visible projects to one while
  validation was running; no recoverable copies were present in the app trash,
  user Recycle Bin, archive, or user-profile search, so that data-state anomaly
  must not be mistaken for a source-control rollback target.
- 2026-07-16 `8175a364` `fix: consume repaired chapter audit failures`:
  prevents normal-original outline review from repeatedly dispatching the same
  repaired chapter. Successful `repair_arc` saves now consume the failed audit
  that requested that arc repair, while signed revise audits whose scoped
  content has already changed are ignored as stale. Focused regressions passed,
  a clean-worktree production build succeeded, Web restarted on port 9898 as
  PID 27596, and live read-only inspection of `重生v4` advanced from the stale
  chapter-6 repair to chapter auditing. The unrelated dirty PR-03 expansion
  work still blocks an all-packages build in the shared worktree.
- 2026-07-16 `feat: add simulation corpus search and download` (pending commit):
  adds project-scoped TXT search and one-click ingestion to the simulation
  sidebar, exact normalized-title filtering, background DaliPan API search,
  headless Edge/Chrome Baidu smart-agent fallback, BaiduPCS-Go download and
  verified automatic installation, long-source splitting, and an encrypted
  private-deployment credential bundle. The real acceptance project
  `仿写语料搜索验收-20260716` ingested all three requested books into 259 non-empty
  ordered TXT parts (139 + 41 + 79); a synthetic absent title returned zero
  results and maps to the explicit `没有找到 TXT 文件` UI message. Focused/full
  Web tests, Web/full vet, 223 UI tests, production build, four live search/
  fallback probes, two live BaiduPCS downloads, service hash/restart smoke, and
  disk validation passed. The repository-wide Go run hit one known Windows
  TempDir cleanup flake; that exact test passed immediately in isolation.
- 2026-07-16 `3bd6269` `fix: make chapter planning audits callable`:
  exposes the stable chapter `scope_id` and every conditional audit field in a
  strict model-visible schema, fully describes nested dimensions/issues, and
  routes exact volume/arc/chapter repair coordinates. This prevents the editor
  from repeatedly submitting an impossible partial `save_original_planning_audit`
  call before chapter-outline generation can advance. Focused regressions passed
  10/10, the full Go suite and `go vet ./...` pass, and the production binary
  builds successfully. The fixed binary was deployed to port 9898; live project
  `重生v4` saved its chapter and arc audits without another missing-`scope_id`
  error, then advanced to `architect_long` chapter-outline repair.
- 2026-07-16 `fix: keep workflow progress panel mounted`: removes the
  project-ID key that forced the workflow progress panel to remount while a
  project snapshot was opening. This prevents React from leaving duplicate
  workflow panels in the production DOM; the UI suite and an isolated-browser
  production-build check both pass.
- 2026-07-16 `fix: retry empty context summaries`:
  retries transient empty context-compaction responses twice with cancellable
  exponential backoff before preserving the existing terminal diagnostic. Each
  retry is emitted as a durable SYSTEM event with the responsible agent so Web
  users can see recovery progress instead of an unexplained immediate stop.
  Focused and complete agents/host tests, package vet, and diff checks passed.
- 2026-07-16 `fix: preserve co-create identities during reinforced imitation`:
  injects the confirmed normal co-create brief into Architect planning context
  as the canonical story authority, rejects premise/character saves that replace
  its locked title, protagonists, or setting, and makes original-planning audit
  tool calls strict so omitted summaries/dimensions are caught before execution.
  Focused and complete tools/agents/assets tests passed; the repository-wide run
  passed every package except one Web background-event timing case that passed
  immediately in isolation. The local Web service was rebuilt without touching
  the existing UI worktree and restarted successfully on port 9898.
- 2026-07-16 accepted PR-01 (this local commit), `feat: add safe manuscript revision publication`:
  adds candidate-isolated manuscript polish/rewrite sessions, stable-ID scope and
  revision/idempotency boundaries, independently signed contract/adaptation audits,
  atomic publication recovery/rollback, normal-path recursive source isolation,
  Web/API/UI integration, and focused/full regression coverage. Eleven fresh
  fix/re-review rounds closed all Blocker/Major/Minor findings. Final independent
  gates passed full sequential Go tests/vet, 222 UI tests, production build,
  focused `-count=5` adversarial groups, gofmt/diff/static/source/planning gates.
  This pipeline creates the commit locally and does not push it.
- 2026-07-15 `7347baa` `fix: hydrate completed planning review details`:
  confirms the affected 93-chapter project has complete persisted outlines,
  detects mixed full/summary-only chapter data when generation transitions to
  pending review, and automatically fetches one full snapshot without losing
  newer runtime state. The focused and full UI suites (69/222 tests), full Go
  suite, `go vet ./...`, production UI/Go builds, restart smoke checks, and live
  resume into chapter 2 passed. Port 9898 runs PID 124012.
- 2026-07-15 `b5d7a34` `fix: complete planning audit recovery lifecycle`:
  makes the planning-audit write tool strict and fully describes nested
  dimensions/issues, carries the exact chapter repair location through routing,
  and invalidates the repaired chapter's failed audit so the gate re-audits it
  instead of dispatching the same repair forever. Chapter repair/re-audit was
  verified against the live stuck project, targeted regressions passed 10/10,
  all affected package tests pass, `go vet ./...` passes, and the full
  `go test ./... -count=1` suite passes.
- 2026-07-15 `c523df7` `fix: expose planning chapter audit scope ids`:
  adds the missing `scope_id` field to the model-visible
  `save_original_planning_audit` schema so chapter-detail audits can submit the
  stable ID already required by routing and runtime validation instead of
  repeating an impossible tool call. Schema and real persistence regressions
  pass 10/10, `go vet ./...` passes, and the full Go suite passes all affected
  packages; one unrelated Web simulation-library assertion passed 5/5 when
  rerun in isolation.
- 2026-07-15 `ee084c5` `fix: recover interrupted planning safely`:
  prevents a committed normal-planning checkpoint from being replaced by a
  stale co-create log after restart, reconciles `collecting` planning with an
  idle Host as paused/recoverable instead of permanently running, returns a
  complete workflow snapshot from manual resume, and surfaces a visible reason
  when resume does not start. Focused and full Web tests, 216 UI tests, UI/Go
  builds, Web vet, and diff checks passed. The repository-wide parallel Go run
  hit two Windows TempDir cleanup flakes; both named tests passed together when
  rerun in isolation. Port 9898 was rebuilt and restarted as PID 137628; the
  affected project resumed at `volume_plan`, reports `grok-4.5`, has two active
  agents, and emitted new streaming events.
- 2026-07-15 `8dd42ef` `fix: show planning workflow model`:
  Maps the actual normal-planning workflow IDs `volume_plan` and
  `chapter_outline` to their configured skeleton/detail-outline model routes,
  so the running workflow header now reports the active model just like
  adaptation analysis and proposal review. Focused regressions, the full Web
  package tests, 213 UI tests, UI/Go production builds, restart smoke checks,
  the live project snapshot, and a browser render all passed. Port 9898 now
  runs PID 133704 and the affected project visibly reports
  `正在运行 · grok-4.5`.
- 2026-07-15 `95d7743c` `fix: distinguish background work from prose writing`:
  `applyWritingState` now requires durable writing evidence before projecting a
  generic running snapshot as prose writing, so simulation-profile analysis on
  a blank new project no longer completes every planning stage. Existing phase/chapter/
  word-backed writing progress is preserved. The focused regression, full Go
  test suite, `go vet ./...`, and `git diff --check` pass. A clean detached
  build was deployed without including unrelated working-tree changes; port
  9898 now runs PID 133616 and the real 59-source project reports every novel
  workflow step idle after its simulation analysis completed.
- 2026-07-15 deployment checkpoint: rebuilt the Vite UI and
  `D:\ainovel\ainovel-cli.exe` from current `main` (`ba3c0f2`), stopped the old
  9898 listener only after successful builds, and started PID 45924 with the
  existing preview runtime root. `/`, `/api/runtime`, `/api/projects`, and one
  real-project summary snapshot passed; 23 projects are visible and persisted
  active adaptation work resumed.
- 2026-07-15 `b329b6c` `fix: serialize pending migration formal writes`:
  Closes PR-05's final known Major issue by making pending structure recovery
  and the following formal write share one outer revision transaction with
  `revision -> migration -> IO` lock order. Timeout/failpoint tests cover
  Progress, cross-Store retry, adaptation check writers, ResetGenerated safety,
  and byte-identical active/prepared/publication rejection. Full sequential Go,
  vet, 213 UI tests/build, and real normal/adaptation project clone validation
  passed; original real projects remained byte-identical. The GPT-5.6 Pro
  PR-06～PR-09 replanning prompt is saved under `docs/`.
- 2026-07-15 `c19dac1` `feat: checkpoint adaptation revision workflow`:
  Captures the complete uncommitted adaptation-revision implementation and its
  Store/Host/HTTP regression suite for upload to `main`. This is intentionally
  a checkpoint, not an accepted PR-05 completion: pending structure migration
  plus guarded Progress/Adaptation writers can re-enter the non-reentrant
  revision transaction and deadlock; `ResetGenerated` can partially delete
  state first. The exact call chains and required tests are recorded in
  `docs/pr05-known-pending-migration-deadlock.md`.
- 2026-07-14 `14d1de0` `feat: add normal manuscript revision workflow`:
  PR-04 wires real normal-fiction Web/Host revision entry points through
  persistent sessions, skeleton-first approval, exact scope-local audits,
  affected-detail generation, complete-book expansion, stable-ID prose rework
  intents, atomic rollback and a strict source/adaptation firewall. Three
  fresh fix/re-review rounds closed production-path, audit and sibling-detail
  injection gaps. Full Go/vet, 213 UI tests, UI build and diff/bootstrap gates
  passed. The pipeline did not push this commit.
- 2026-07-14 `80107a6` + `f4829f7`
  `fix: fail over insufficient user quota errors`:
  Provider precharge failures reported as `insufficient_user_quota` now
  classify as hard quota exhaustion and immediately enter the configured
  same-model backend pool. Empty stream-start events are buffered until actual
  text, thinking, or tool output appears, so a precharge failure after an empty
  stream prelude remains safe to retry on another backend. The exact live
  Yuanyu-to-Suifeng writing-stage shapes have sync and stream regression
  coverage. Focused fallback tests, full bootstrap tests, package vet, full Go
  tests, 213 UI tests, production build, and diff-check passed. A clean-commit
  binary was deployed on port 9898. Project and global live probes both report
  Yuanyu `Invalid token`; Suifeng passes small probes. A controlled 80,008-rune
  input failed even with `max_tokens=8`: the provider reported about $3.376
  available versus about $5.102 required; `max_tokens=4096` required about
  $5.408. The project remains safely idle at chapter 29 pending provider credit
  or an explicitly selected different model.
- 2026-07-14 `14df0ba` `feat: add adaptive manuscript structure planning`:
  PR-03 adds mode-neutral seven-operation structure planning, sealed previews,
  exact stable-ID delta validation, dynamic soft budgets, causal required/
  recommended impacts, strict output/context batch limits, failed-batch
  recovery fences, and local/per-volume/whole-book audit gates. Five fresh
  fix/re-review rounds closed preview forgery, cross-planner replay, evidence
  matching, overflow, and planner-scope lifecycle issues. Sequential final
  gates passed full Go tests/vet, 213 UI tests, UI build, and diff-check. The
  commit is local and was not pushed by this pipeline.
- 2026-07-14 `152c16b` `feat: add reversible manuscript revision sessions`:
  PR-02 adds durable candidate-isolated revision sessions, version history,
  expected-revision and idempotency checks, one-active-revision enforcement,
  cross-process Windows/Unix locking, scoped normal-flow ownership, routed
  generation fences, crash/restart invariants, staged human approvals, and
  Host/Web resume guards. Eight fresh fix/re-review rounds closed process
  races, policy reentrancy, cancellation fencing, async ownership, terminal
  handoff, Close/waitDone, and Steer error-contract regressions. Final gates
  passed sequentially: `go test ./... -count=1 -timeout 15m`, `go vet ./...`,
  213 UI tests, production UI build, and `git diff --check`. The commit is
  local and has not been pushed by this pipeline.
- 2026-07-14 `25bedb6` `fix: ignore stale co-create after writing starts`
  and `6cec895` `fix: complete prerequisites in writing progress`: writing,
  paused, and completed projects now show all five planning stages as complete;
  untouched new projects remain idle. Startup recovery treats persisted
  writing/complete progress as authoritative and removes stale co-create
  checkpoints instead of rebuilding them from legacy logs. Focused regressions,
  full Web tests, Web vet, two clean-binary cold restarts, API checks, and live
  Chrome validation passed. The affected live project retained chapters 1-2
  (5624 words) and resumed chapter 3 with no planning review or co-create state.
- 2026-07-14 `af0da9b` `feat: add stable manuscript structure identities`:
  The accepted PR-01 candidate introduces stable volume/arc/chapter IDs,
  canonical stable-ID artifact storage, numeric compatibility projections, and
  a staged migration log. Review-gated fixes add a shared recovery gate for
  Progress, Continuation, and diagnostic snapshots; durable request receipts
  for public ID-less expansion/append/SavePlan retries; one migration boundary
  for rollback structure and semantic state; and deletion of replacement
  backup/temp files before targets. Regression tests exercise migration stages,
  delayed exact retries, rollback recovery, asymmetric volume/arc summaries,
  arc-batch identity, canonical-fact authority, and adaptation ambiguity. The
  fifth independent re-review passed. Main-agent gates passed `go test ./...
  -count=1`, `go vet ./...`, 213 UI tests, the production UI build, and
  `git diff --check`. The accepted PR-01 change is committed locally and is
  not pushed.
- 2026-07-14 `7dc754c` `fix: honor co-create streaming timeouts`:
  explicit stage deadlines now remain authoritative for streamed provider
  calls, and every co-create retry receives a fresh configured timeout budget.
  Regressions reproduce both the former 120-second provider cutoff and the
  shared retry-deadline failure. `go test ./... -count=1`, `go vet ./...`, and
  `git diff --check` passed. A live Chrome retry on
  `codex-login/gpt-5.6-sol` completed after 311.164 seconds and two attempts
  with `error=null`, an 8056-character ready draft, and three clickable AI
  suggestions. The affected project now persists a 600-second co-create
  timeout. Deployment used compatibility commit `f31150b` so the active
  writing project retained the already-committed workflow recovery behavior;
  it resumed from 4/33 chapters and 11099 words without rollback.
- 2026-07-14 `b6311d5` `fix: omit unsupported thinking parameters`:
  the swappable model boundary now resolves the final per-call thinking level
  against the active provider/model capabilities. Unsupported OpenAI-compatible
  models such as DeepSeek receive no explicit `thinking` option, even when a
  configured or upstream call-level override requested one. The focused
  regression, related bootstrap/agents/host packages, `go test ./... -count=1`,
  `go vet ./...`, and `git diff --check` passed. The active Web service was not
  restarted because this fix was isolated from unrelated work on another branch.
- 2026-07-14 `0501e92` `test: cover model reasoning inheritance`:
  completed the previously uncommitted reasoning-effort regression coverage.
  Tests now verify role overrides, stage inheritance, stage overrides, and
  per-model defaults. `go test ./... -count=1`, `go vet ./...`, and
  `git diff --check` passed; all other apparent working-tree changes were
  content-identical Windows line-ending/stat noise and normalized cleanly.
- 2026-07-14 `f8541c6` `feat: audit original proposals before user review`:
  normal-original long projects now generate volume skeletons and 3-4 chapter
  arc batches behind durable, original-fiction semantic audits. Failed scopes
  are repaired and re-audited before volume or chapter review becomes visible;
  user volume/chapter feedback re-enters the same targeted quality gate. The
  co-create workspace shows full reviewed content centrally and selection plus
  feedback controls in the right panel. A cloned 100,000-word project completed
  3 volumes, 9 arcs, and 33 detailed chapters; all latest audit scopes pass.
  Full Go tests, Go vet, 213 UI tests, production build, and live Chrome
  validation passed. The Web service was rebuilt on port 9898.
- 2026-07-13 `55645c5` `fix: connect Codex credentials and reasoning controls`:
  added validated Codex credential upload and status checks, corrected the
  ChatGPT Codex Responses request/stream protocol, discovered the signed-in
  account model catalog through `codex app-server`, and added persistent
  per-model plus per-stage reasoning depth for GPT/Codex and Grok 4.5. The
  staged-source snapshot passed all 211 UI tests, a production UI build, and
  `go test ./... -count=1`; live Codex login, discovery, and model inference
  also passed.
- 2026-07-13 `93c8ac3` `fix: stabilize de-AI audit sequence`: a live
  DeepSeek continuation completed chapter 71 using two `repair_de_ai_batch`
  calls (three exact replacements per batch), each followed by a fresh audit.
  The final audit passed and the chapter committed. Prompt and tool follow-up
  contracts now require consistency/adaptation repairs to settle first, then
  require every audit to pass on the same draft revision before commit. Full
  `go test ./... -count=1` passed. The running service was intentionally not
  restarted while the separate adaptation recovery remains active.
- 2026-07-13 `4ba5fb1` `fix: recover adaptation and batch de-AI repair`: detail
  recovery now preserves validated proposal batches and repairs only the owning
  failed range; browser Resume reused 150 saved batches, passed batch 151, and
  continued to batch 152. Long project opens now use a slim progress/navigation
  snapshot, lazy-load continuation content, and treat SSE sequence as a cursor
  with legal coalescing holes. The independent de-AI stage emits a bounded
  repair plan and applies 1-8 exact, non-overlapping replacements atomically;
  whole-chapter rewrite is conditional only after two non-improving batches or
  a structural story issue. Full Go tests, 210 UI tests, and live Chrome
  switching/recovery validation passed. Service is running on port 9898.
- 2026-07-12 pending `fix: make adaptation resume stage-monotonic`: proposal
  runtimes now pin the exact co-create intent/source/prompt dependency used at
  planner start. Resumes reuse that artifact or fail closed; they never
  regenerate co-create work after skeleton/detail progress exists. Proposal
  endpoints resolve completed co-create artifacts at the downstream boundary,
  and volume generation refuses to run after detail generation starts or any
  detail batch exists, preventing destructive workflow regression. Legacy
  runtimes migrate the dependency once. Full Go tests and a local Web build
  passed; live “术士手册 - 副本” is back in `details_generating` with the pinned
  original intent and has regenerated/passed its first detail batch.
- 2026-07-12 pending `fix: restore adaptation quality workflows`: source
  chapter lineage now outranks fuzzy theme keywords; genuine outline mismatches
  reach targeted owner/alternative repair; annotated `preserve_events` are
  canonicalized before detail auditing; and identical blocking findings stop
  after two no-progress repairs instead of blindly consuming seven attempts.
  Full Go tests passed. The rebuilt Web runtime resumed “天擎” through arc-end
  review into chapter-64 polishing and resumed “术士手册 - 副本” through detail
  batches 59/60, parent/volume audits, and into batch 61/370.
- 2026-07-12 pending `fix: normalize annotated preserved event IDs`:
  arc validation now compares the leading stable `src-*` token when a planner
  returns `preserve_events` as `src-ID：description`, while prompts explicitly
  require exact IDs without appended descriptions. This removes the live false
  50-finding loop in Sorcerer's Handbook batch 59 without weakening ownership
  checks for genuinely different events.
- 2026-07-12 `377e67f2 fix: consume pending steering on resume`:
  Resume now snapshots the persisted user intervention, injects it once, then
  clears it atomically only if the same text is still pending. A newer
  intervention is preserved. This fixes repeated recovery into an old chapter
  caused by a stale `pending_steer`; controlled pause/resume recovered directly
  to chapter 32, and chapter 32 subsequently committed successfully.
- 2026-07-12 pending `fix: retry malformed outline audit responses`:
  the layered detail-outline auditor now decodes the first complete JSON value
  instead of spanning from the first to the last brace across trailing prose or
  multiple objects. Truly malformed auditor output is discarded and retried up
  to three times from the original clean payload without carrying the invalid
  response forward or consuming content-repair attempts. This closes the live
  Sorcerer's Handbook stop observed twice during legacy parent/volume review.
- 2026-07-12 pending `feat: close the detail-outline audit loop`:
  adaptation detail batches now persist candidate/audit/context signatures and
  must pass deterministic plus independent auditor review before becoming later
  planner context. Failed content is regenerated as a whole batch and fully
  re-audited; unchanged repairs cannot pass, transport failures do not consume
  content attempts, and parent/volume/global checkpoints invalidate only
  affected downstream audits. Legacy v1 batches are reviewed before reuse. The
  event whitelist contract now consistently permits stable supporting events,
  fixing the repeated `foreign_event_id` loop seen in the Sorcerer's Handbook
  copy, and auditor source reports are range-bounded. Full `go test ./...` and a
  standalone build pass; live restart is intentionally deferred while two
  writing projects are active.
- 2026-07-12 `bb591f9 fix: auto-resume incomplete creative runs`:
  an unexpected Coordinator stop during a non-complete writing run now resumes
  from persisted progress/checkpoints up to three times per unchanged completed
  chapter count. Manual pause, closed Hosts, and completed books do not trigger
  this path. This prevents transient authorization/network/tool failures from
  leaving the next chapter in progress while still bounding token spend.
- 2026-07-12 `01de103d fix: treat moderate full-rewrite budget overage as acceptable`:
  adaptation `full_rewrite/free` chapter budgets now expose an explicit soft
  allowance. A complete chapter within that allowance is accepted without a
  budget warning or length-only rewrite; preserve-details and normal book-level
  budgets remain hard. Writer prompts/context now state the same policy.
  Focused adaptation/tools tests, all 22 Web UI files / 208 tests, Vite build,
  and service restart passed. A full Go-suite rerun still has the unrelated
  asynchronous Web background-analysis event test failure; its standalone
  rerun passes.
- 2026-07-12 targeted legacy-outline repair and observability hardening:
  budget-only model reanalysis now retries against local density validation and
  persists a one-time `budget_repair` marker; old event-binding repairs are
  scoped to the current chapter and never regenerate story fields. Fixed the
  legacy backup loader path so deterministic migration backups are actually
  eligible for model reanalysis, added explicit tool-registry validation and
  unified Host/Web event persistence with SSE history recovery. Full Go tests,
  all 208 Web UI tests, and a live restart passed. Live projects confirmed
  model budget repair markers and continued through new chapter submissions.
- 2026-07-12 resume-next-chapter fix: `5e370cb8`
  Resume now queues the Host route as an agentcore follow-up and explicitly
  continues an already-idle Coordinator. Previously Resume started a Prompt
  and queued the next-chapter instruction with Steer; if that Prompt ended
  without a tool call, the project became idle with the instruction stranded
  in the queue. Normal start and in-run boundary dispatch keep their existing
  steering behavior. Added a regression test for the immediate-stop resume
  race. Full `go test ./... -count=1`, build, and `git diff --check` passed.
- 2026-07-12 outline-density and transformed-event evidence gate: `79b282b8`
  `ValidateArcChapterBudgetDensity` now blocks high-density arc chapters whose
  maximum output cannot express their planned scenes, and the planner prompt,
  outline gate, routing, and final chapter contract all share the same guard.
  Full-rewrite chapters with explicit `required_changes` now use structured
  `change_evidence` for transformed source events instead of being forced to
  reproduce those events verbatim; chapters without an explicit transform keep
  the stricter body-evidence fallback. Source-event ownership and
  `preserve_events` alignment are also checked before writing. Full Go tests,
  build, and live restart passed. The live `天擎` chapter 39 plan was repaired
  from 1200-1600 to 3400-4600 runes with six matching event owners; the live
  `武林高手 - 副本` chapter 14 stale failed check was cleared for re-evaluation.
- 2026-07-12 legacy chapter-budget auto-repair: `f427a3b`
  Confirmed arc plans now repair positive but impossible scene-density budgets
  at adaptation-state load, Writer guard, `check_adaptation`, and
  `commit_chapter`. The pre-repair adaptation snapshot is backed up with the
  `auto-budget-density-repair` label, plan totals are rebuilt, and a passed
  outline marker is reissued only after the repaired plan is persisted. The
  density gate also catches extremely small positive legacy budgets while
  leaving missing-budget placeholders to the broader plan validator. Full Go
  tests and a live restart passed; `天擎` had 22 such chapters repaired,
  including chapters 51, 54, and 55.
- 2026-07-12 adaptation outline contract gate: arc proposals now validate
  supporting/texture source-event ownership and plot-theme alignment before
  any Writer body is generated. Successful whole-plan audits persist a
  contract signature marker so new projects do not repeat the same audit on
  every chapter. Legacy confirmed plans use a current-chapter migration gate
  at routing, Writer, and commit boundaries, with a deterministic final
  fallback in `check_adaptation`/`commit_chapter`. The repaired live project
  `武林高手 - 副本` moved from 12/350 to a committed chapter 13 and entered
  chapter 14; its chapter-13 event contract was backed up before repair.
- 2026-07-12 DeepSeek repair-loop stabilization: DeepSeek retains the tested
  128K context profile. Writer threshold compaction now commits into the active
  subagent baseline, preventing the observed 20-30K -> 110-150K rebound across
  consecutive tool turns. Agentcore was upgraded from 1.7.7 to 1.7.9 so its
  summary serializer truncates long Chinese tool results only at valid UTF-8
  character boundaries; this fixes the live `messages[1]: text block must be
  valid UTF-8` failure without deleting project context. Summary calls retain
  their token cap but omit the explicit `thinking=off` rejected by the current
  DeepSeek-compatible backend. StopGuard now reissues the exact current Host
  route instead of repeatedly telling Coordinator to wait. Adaptation detail batches immediately discard
  semantically polluted prior output for foreign-event and duplicate-outline
  failures, expose the exact batch event whitelist, and require future plot
  beats to be rebuilt rather than hidden by deleting metadata. Focused tests,
  related packages, full `go test ./... -count=1`, `go vet ./...`, and
  `git diff --check` passed.
  A bare relay `authorization failed` is treated as ambiguous and receives at
  most three attempts with normal backoff; explicit invalid-token/401 failures
  and exhausted-quota errors are not allowed to burn the full 14-attempt
  planner budget after model fallback is exhausted.
  All six stage routes on all 19 current runtime projects (114 routes) were
  explicitly switched to `deepseek-suifeng-0/deepseek-v4-pro` and passed API
  read-back verification; Grok remains configured for later manual switching.
- 2026-07-12 cross-chapter adaptation deadlock repair: Writer and Editor now
  receive future-chapter ownership promises, deterministic adaptation feedback
  returns assigned event descriptions, and legacy plans can satisfy a duplicated
  event assignment only when an earlier chapter both owns the event and its
  committed prose proves it. Active projects can queue completed-chapter
  rewrites without reopening the entire book. Full Go tests and a live Web run
  passed; `武林高手 - 副本` advanced from 4/350 to 5/350 and began chapter 6.
- 2026-07-12 same-model runtime failover and active-model display: `8b5794a`
  `fix: preserve model during backend failover`. Runtime automatic switching
  now searches only configured backends that expose the currently selected
  model name, rejects cross-model targets defensively, and no longer rewrites
  or persists unrelated stage/Agent routes while selecting a fallback. The
  unified workflow header shows the active stage model beside “正在运行” while
  intentionally hiding the provider/backend. Focused Go fallback/Web tests,
  the workflow-progress UI test, `git diff --check`, and a clean Vite
  production build passed.
- 2026-07-12 project cold-open isolation: `db01753`, `28ac506`,
  `f228bcd`, `4d09a58`, and `cf6dc0e`. Project Host initialization no
  longer holds the global session-map mutex; different projects initialize in
  parallel while duplicate opens of the same project share one in-flight
  result. The UI keeps a stable target loading screen and uses a measured
  90-second cold-restore timeout instead of the previous 15-second threshold.
  The full Web Go package, five repeated concurrency regressions, the focused
  57-test UI file, and clean Vite builds passed. Live verification returned
  `女主播的秘密` in 43.85 seconds while an independent project returned in
  4.17 seconds; both were HTTP 200. Web is running on port 9898 as PID `93192`
  with `index-B_f3EFQx.js`.
- 2026-07-12 quality-first defaults for new projects: `738ff75`
  `feat: default new projects to quality stage routes`. New Web-created
  projects now explicitly route co-create, source analysis, skeleton planning,
  detailed outlines, and review/summary to Grok 4.5, while prose writing uses
  DeepSeek V4 Pro. Provider IDs are selected automatically from configured,
  enabled backends; when only one recommended model is available, all stages
  fall back to that model. Existing projects are never migrated by this
  initializer. The focused creation tests and full `go test ./... -count=1`
  suite passed. Web was rebuilt and restarted on port 9898 as PID `89560`.
  Separately, all 19 existing runtime projects were explicitly migrated through
  the live project-model API and all 114 stage routes passed read-back checks.
- 2026-07-12 adaptation detail-event ownership fix: `f696142`
  `fix: enforce adaptation detail event ownership`. Arc detail prompts now
  expose only the current child batch's assigned mainline event IDs, and local
  validation rejects every foreign `event_id` before a generated or cached
  batch can be persisted/reused. Detail splitting has regression coverage that
  assigns every parent mainline event exactly once; cached recovery retains the
  correct owner batch and regenerates only the polluted batch. Resume progress
  now distinguishes loading an existing skeleton and summarizes reused batches
  instead of replaying hundreds of misleading per-batch `Reused` messages.
  Focused real-project prompt validation, the full adaptation package, full Go
  suite, Go vet, all 206 Web tests, and the Vite production build passed. The
  clean `81b6b929` build was installed and restarted on port 9898 as PID
  `19120`; runtime, project-list, and project-snapshot endpoints returned 200,
  and the failed project still retains 368/370 completed detail batches.
- 2026-07-12 stage-aware model routing and context benchmark: `bb18f7f`
  `feat: add stage-aware model routing`. Added project-scoped model selection
  for six user-facing creative stages with provider-agnostic, deduplicated
  model names; stage routes override Agent routes and inherit them when unset.
  Added hidden DeepSeek V4 Pro / Grok 4.5 context profiles and a reproducible
  40-call-per-model benchmark/report. DeepSeek completed 40 calls; Grok
  completed 38 and retained two interrupted attempts without exceeding quota.
  Full Go tests, 206 UI tests, Vite build, report verification at desktop and
  mobile widths, live stage-route persistence round trip, and service smoke
  checks passed. Web is live on port 9898 as PID `36840`; `天擎` was resumed
  at chapter 10 and reached `RUNNING` with `draft_chapter` active.
- 2026-07-12 read-only `术士手册 - 副本` proposal-failure diagnosis:
  the skeleton was not regenerated or awaiting review. Final detail-plan
  validation repeatedly found mainline event `src-0629-e01-07d60f5b` copied
  across adjacent detail batches 130/131 (target 488-494), discarded both,
  replayed all 370 cached batches, and exhausted the final repair limit. Local
  batch validation checks required current-batch IDs but does not reject a
  foreign required mainline ID, so polluted output can pass locally and fail
  only after whole-plan assembly. Recursive repair also re-emits the
  skeleton-complete message and every retained-batch `Reused` event, creating
  the false appearance of skeleton replanning. No business code or runtime data
  was changed; the live checkpoint retains 368/370 completed detail batches.
- 2026-07-12 immediate project-switch fix: `457a242`
  `fix: switch projects immediately while loading`. Clicking another project
  now selects the target immediately, clears the previous project's detail
  view, and shows a target-specific loading state instead of leaving a running
  project visible until the new snapshot returns. SSE startup is deferred until
  the initial snapshot finishes, avoiding duplicate session-open work. All 205
  UI tests, the Vite production build, and `git diff --check` passed. The Web
  service is live on port 9898 as PID `91032` with the new bundle.
- 2026-07-12 context-window and summary-evidence fix: `ab36715`
  `fix: stabilize agent context management`. Static prompt
  assembly budgets no longer cap live agent conversations. Runtime windows now
  use role-specific caps (Coordinator 64K, Writer/Architect 96K, Editor 128K)
  while respecting the provider model window. Arc/volume summaries consume a
  compact evidence pack first and only reread chapters/arcs with explicit
  evidence gaps, preserving quality without routine full-arc rereads. Focused
  agent/flow/tool tests, all 205 UI tests, the production build, and the full Go
  suite passed; one Web timing test flaked once under full-suite load and then
  passed three focused repetitions plus the full Web package. Live DeepSeek
  Writer logs confirmed `model_window=1048576`, `runtime_window=96000`, and
  real usage at `35094/96000` instead of the former 40K cap.
- Latest stable-render/open-project fixes: `ba770ed` retains the last valid
  workflow panel at the render boundary for the same project, `43d5be3` keeps
  the current page visible while a newly clicked project loads, cancels stale
  opens, shows an explicit loading state, and times out after 15 seconds, and
  `a708ae8` includes `workflow_progress` in the initial project snapshot.
  All 205 UI tests, the full Web Go package, production build, and
  `git diff --check` passed. A live `天擎` snapshot returned in 0.46 seconds
  with `adaptation/running` and six workflow steps in the first response. A
  clean-worktree binary was deployed to port 9898 as PID `85384`; unrelated
  prompt/router/context changes in the main worktree were not included.
- Latest progress-panel stability fix: `ea5b451` `fix: remove legacy progress UI fallback`:
  SSE snapshot updates that omit `workflow_progress` now retain the most recent
  unified workflow state, preventing the header from disappearing between
  high-frequency writing events. The retired compact workspace progress bar,
  its derivation code, tests, and CSS were removed, so no project stage can
  fall back to the old UI. All 203 UI tests, the Vite production build,
  `git diff --check`, and live asset/API smoke checks passed. The served bundle
  contains the unified workflow panel and no legacy `workspace-progress`
  component or styles. Web was restarted on port 9898 as PID `88864`.
- 2026-07-12 read-only duplicate-mainline diagnosis: live `武林高手 - 副本`
  checkpoint retains 92/94 proposal detail batches after correctly discarding
  batches 16-17 (target chapters 58-65) for duplicate event
  `src-0072-e07-fcc35134` in chapters 59 and 62. The persistent recovery loop
  is caused by a validator gap: `validateArcBatchEventCoverage` enforces each
  current-batch required ID exactly once but does not reject required mainline
  IDs owned by another detail batch, allowing a polluted batch to be saved and
  reused until final whole-plan validation fails. Existing focused recovery
  tests pass but cover only discard/regeneration start, not foreign-event local
  rejection or successful directed repair. No business code or runtime data was
  changed.
- Latest workflow-status fix: `d0ff42a` `fix: show adaptation proposal generation as running`:
  active adaptation proposal generation and revision now take precedence over
  review artifacts in the unified workflow state. The workflow header and
  proposal step remain `running` until the background task finishes; only then
  can the UI show `waiting_confirmation`. The Chinese running label is now
  “正在运行”. The full Web Go package, all 204 UI tests, Vite build,
  `git diff --check`, and live HTTP asset/API smoke checks passed. Web was
  rebuilt and restarted on port 9898 as PID `89664`.
- Latest rollback-resume fix: `2f9f475` `fix: preserve adaptation review after rollback`:
  projects with a durable adaptation plan,
  proposal, volume review, or proposal runtime no longer restart stale source
  analysis when the user clicks Resume. This preserves proposal/volume review
  gates after cloning and rollback even when a later software version marks an
  older dossier schema stale. The real `武林高手 - 副本` diagnosis confirmed
  cloning retained all 451 source chapters and all 451 chapter reports; only
  the dossier schema/batch layout had advanced after the clone was created.
  Focused Web regressions, the full Web package, `go test ./... -count=1`,
  `go vet ./...`, and `git diff --check` passed. The parent project's 43
  already-completed current dossier batches were copied into the existing
  clone without changing clone-specific planning data. Web was rebuilt and
  restarted on port 9898 as PID `87824`; the clone now reports analysis
  `done`, 43 dossier batches, and an idle runtime.
- Latest mainline-binding recovery fix: `eee8841` `fix: repair duplicate
  mainline bindings`: detailed Arc planning now exposes only the current small
  batch's assigned mainline IDs, records duplicate bindings as structured
  validation errors, discards only completed detail batches containing the
  duplicated ID, and automatically regenerates those batches within the
  configured structure-repair limit. The real `武林高手 - 副本` checkpoint was
  inspected read-only: two of 121 cached batches account for all three bindings,
  so recovery will retain the other 119 batches. Focused regressions, the full
  `internal/host/adapt` package, `go test ./... -count=1`, `go vet ./...`, and
  `git diff --check` passed. Web was rebuilt from `12da231`, restarted on port
  9898 as PID `76580`, and the runtime/project API smoke checks passed with the
  intended preview runtime root and 19 visible projects.
- Latest detailed-outline repair stability change: `171d387` `fix: reset
  polluted detail repair chains`: detailed
  chapter batches, proposal-revision batches, and missing-chapter fills now
  reject multiple top-level JSON objects. Ambiguous output immediately starts
  a clean per-batch regeneration; after one unsuccessful ordinary repair,
  later attempts also omit the failed response. Missing-chapter retries retain
  already accepted chapters while clearing only the failed fill response.
  Focused regressions, the full `internal/host/adapt` package, and
  `go test ./... -count=1` passed.
- Latest adaptation skeleton stability change: `08fdb9a` `fix: reset polluted
  skeleton repair chains`: source-map
  skeleton prompts now state that `mainline_rules` and `relationship_goals`
  are string arrays, and the strict decoder rejects type drift instead of
  coercing it. Ambiguous multi-JSON responses immediately regenerate from the
  original small-batch request; after one unsuccessful ordinary structural
  repair, later attempts also clear the transient failed output instead of
  repeating a polluted repair chain. Focused regressions, the full
  `internal/host/adapt` package, and `go test ./... -count=1` passed.
- Latest adaptation library/retry fix: `66ca261` `fix: sync analyzed novels and
  live retries`: completed or newly finishing source
  analysis now upserts the full prepared novel package into `novel_library`,
  including manual analysis resumed after loading a legacy library entry. The
  upsert first matches the original `source_file`, so a display name such as
  `术士手册` is preserved even when its source file is `术师手册.txt`. The
  Web library list refreshes from the resulting library event. Adaptation retry
  dependencies now read live project settings, and the outer detail-outline
  quality retry loop re-evaluates its limit between attempts. Full `go test
  ./... -count=1`, all 203 Web tests, and the production Web build passed.
- Latest model-discovery UI fix: `26f44d4` `fix: show all discovered provider
  models`: clicking `测试并发现模型` now reports the
  discovered model count and replaces the editable model-name field with a
  true select control containing every returned model. Before discovery, the
  field remains editable for custom model IDs. All 203 Web tests and the
  production build passed; the restarted service (PID `42580`) returned 11
  Grok OAuth models from `/api/models/discover`.
- Latest upstream sync: `c4592c6a` `fix: bound adaptation detail prompt evidence`:
  fast-forwarded `main` by four commits from `f7da4ea9`, including project
  switching stability, adaptation planning convergence, first-click project
  opening, and bounded detail-prompt evidence. Rebuilt the Web UI and Go
  executable, restarted port 9898 as PID `52192`, and verified the homepage,
  runtime API, and 20-project listing.
- Latest long-brief fix: `47716556` `fix: support long adaptation briefs`:
  removes the proposal-entry 16/8 rule-count rejection, preserves the complete
  brief and durable rule set, and gives adaptation-only planner/writer prompts
  a prioritized 64-rule / 32-forbidden-rule view. Generic prompt compiler
  defaults remain unchanged. Prompt-level and end-to-end regressions confirm a
  96-rule brief compiles, reaches proposal generation, and persists all 96
  rules. Full Go tests, Go vet, focused regressions, and diff checks passed.
  Web was rebuilt/restarted on port 9898 as PID `82564`; runtime and project
  API smoke checks passed with 20 projects visible.
- Latest rollback fix: `51b6105` `fix: preserve adaptation rollback stages`:
  adaptation projects with generated
  volumes now step backward through proposal review, volume-skeleton review,
  co-create draft, and blank states without skipping a gate. Unvolumed short
  proposals retain the direct proposal-to-draft exception. The Web session and
  response restore durable co-create state after the draft rollback, and the UI
  no longer clears that recovered state. Full Go tests, Go vet, 199 Web tests,
  the production build, focused rollback tests, and diff checks passed. Web was
  rebuilt/restarted on port 9898 as PID `74016`; a live clone of `武林高手`
  passed all four transitions through blank, restored a ready/startable
  2304-character adaptation draft at the draft stage, and left the source plan
  SHA-256 unchanged. The temporary acceptance clone was moved to project trash.
- Latest scheduled-resume change: `b4f898d` `feat: add safe scheduled project resume`:
  adds global multiple daily resume times, same-day catch-up, durable scheduler
  state, project default-on switches, explicit adaptation planning gates, and a
  dedicated Web schedule tab. Full Go tests, Go vet, 198 Web tests, production
  build, API smoke tests, Playwright acceptance, and port 9898 restart passed.
  All pre-existing feature branches were audited as ancestors of `main`; after
  the final push only `main` should remain locally and on `origin`.
- Latest adaptation-audit change: `f6e89e3` `feat: add end-to-end adaptation audit safeguards`:
  adds completion and publish-export audit gates, immutable 50-run audit history,
  deterministic cross-run comparison, a dedicated resumable `auditor` model role,
  verified full-coverage semantic second review, and Web controls for history,
  comparison, model selection, cost/call limits, cancel/retry, and publish acknowledgement.
  Full `go test ./... -count=1`, `go vet ./...`, 191 Web tests, production build,
  and Playwright acceptance passed. The restarted `武林高手` audit correctly resolves
  source 1-284 / target 1-315 as legacy read-only `inconclusive`, with 13 findings,
  zero blocking findings, no repair action, and immutable history. Port 9898 was
  restarted successfully with the external preview runtime root.
- Latest project-clone changes: `25ef41f9` `feat: add independent project cloning`
  and `3d459489` `fix: preserve clone dialog text encoding`:
  adds a project-menu clone dialog, atomic full-directory copies with new
  project identity/provenance, project-owned absolute-path rebasing, and
  exclusion of temporary/runtime action state. Running projects return a
  conflict until paused. Full Go tests, Go vet, 186 Web tests, production UI
  build, diff checks, and live browser clone-dialog checks passed in a clean
  worktree. `main` was pushed through `3d459489`; the clean binary was installed
  at `D:\ainovel\ainovel-cli.exe` and restarted on port 9898 as PID `85336`.
- Latest audit-safety change: `b46b8ff` `fix: audit only completed adaptation units`:
  replaces manual source/target ranges with one titled source-ending picker;
  derives Chapter completeness from valid full source-segment contracts and Arc
  completeness from finished planner batches; filters audit inputs to the
  effective range; and converts legacy evidence gaps into non-repairable
  `inconclusive` reports. Server-side confirmation and stale-scope checks block
  direct-API bypasses. Full Go tests, Go vet, 185 Web tests, production build,
  and Playwright real-project acceptance passed. The restarted 武林高手 audit
  resolves source 1-284 / target 1-315, reports 12 invalid legacy quotes without
  claiming missing prose, and exposes no repair action.
- Web-only workbench change merged into `main` through `becea42f`: removes the
  complete TUI and Charmbracelet dependency tree; makes no-arg startup open the
  localhost Web UI; adds strict non-interactive headless answers, first-run Web
  setup, unified three-workflow progress, idempotent persistent background
  actions, responsive/accessibility improvements, a 90-day privacy-safe
  per-call usage ledger and Prompt Cache observability dashboard, and safe
  legacy output migration. Full Go tests, Go vet, 183 Web tests, production UI
  build, Docker config, release binary build, diff checks, and Playwright
  1440/1024/390 no-overflow checks passed. After the fast-forward merge, full
  Go tests, Go vet, all 185 Web tests, the production UI build, and diff checks
  passed again. The local binary was rebuilt from `23d685b2`, restarted on
  `http://127.0.0.1:9898` as PID `55416`, and `/api/runtime` returned HTTP 200.
  Race tests are unavailable because this Windows Go runtime has CGO disabled.
- Current recovery/fix change: restored the usable local model registry after a
  Web test accidentally overwrote the real global config, isolated all Web
  tests behind temporary persist paths, and made global model deletion remove
  inherited provider credentials/routes from active and trashed project
  overlays while preserving explicitly project-owned providers. A real Web
  model test confirmed `deepseek-suifeng-0` works and
  `deepseek-suifeng-1` has an invalid token; the invalid provider and the
  already-invalid `deepseek-infa` references were removed. The global registry
  now contains `deepseek-suifeng-0`, `deepseek-yuanyu-0`, and `grok-oauth`, with
  retry settings 7/7/2/2. Full Go tests passed without changing the live config.
- Current development change: surfaced the existing adaptation audit as a
  dedicated Web tool page with an explicit `read-only audit -> acknowledge and
  apply repair plan -> user clicks restore` workflow. Added plan-only quality
  gates before detailed-outline acceptance for Chapter/Arc/Free and a separate
  project-configurable `adaptation_outline_audit_retry_max_attempts` setting
  (default: two retries after the initial generation). This setting is distinct
  from model/network, structure-repair, and budget-review retries. No live
  novel audit, plan repair, resume, or regeneration is part of validation.
- Latest implementation commit: `83856d9` `feat: enforce mode-aware adaptation quality`:
  adds compact, capability-reviewed role prompts; bounded Agent contexts and a
  five-component planner compiler; Chapter 1:N SourceSegment contracts; Arc
  mainline event bindings; Free target causality/relationship/setting ledgers;
  independent evidence and anti-AI checks; and read-only audit/confirmed repair
  APIs. Full Go tests, Go vet, 162 Web UI tests, UI production build, and staged
  diff checks passed. No live novel audit, repair, resume, or regeneration was
  run as acceptance. Commits `83856d9` and `b91731d` were pushed to
  `origin/main`; Web was rebuilt/restarted on `http://127.0.0.1:9898` as PID
  `65228` with the external preview runtime root.
- Latest source/runtime change: `feat: add reviewed novel continuation workflow`:
  imported novels now stop at a durable planning gate instead of automatically
  resuming Writer. The shared continuation state machine requires source
  baseline, Draft co-create, proposal review, conditional volume review,
  detailed chapter-outline review, and an explicit final start. Web now has a
  first-class five-step “续写” workspace with targeted revisions and optimistic
  revision checks; TUI can drive the same workflow with `/cocreate` and
  `/continuation`. Final approval appends only N+1..M to the canonical outline
  through a journaled rollback-safe commit, and Host Resume/Continue cannot
  bypass review. Validation passed for the clean staged-index full Go suite,
  161 Web UI tests, production UI build, `git diff --cached --check`, read-only
  Playwright inspection, and the restarted Web service at
  `http://127.0.0.1:9898` (PID `12360`).
- Latest source/runtime change: formal single-chapter outline
  revision after writing has started. The Web status panel can now select any
  completed, in-progress, or future chapter and send a natural-language edit
  instruction to the architect model. The Host preserves chapter numbering and
  adaptation anchors/budgets/constraints, rejects unchanged output, and uses
  configured model/structure retries. Store invalidation clears stale drafts,
  finals, summaries, reviews, adaptation checks, world/cast facts, and affected
  arc/volume summaries. Completed chapters enter `PendingRewrites`; completed
  books reopen; the existing router then performs chapter rewrite/review before
  affected arc/volume postprocessing and only then continues new writing.
  Flat and layered outlines are both supported. Validation passed for focused
  Go packages, full `go test ./... -count=1`, 150 Web UI tests, production UI
  build, `git diff --check`, runtime smoke check, and read-only Playwright UI
  inspection. Web was rebuilt/restarted on `http://127.0.0.1:9898` as PID
  `59596`.
- Latest source/runtime change: current local commit
  `fix: keep adaptation on planned chapters at boundaries`:
  Flow routing now treats confirmed adaptation projects as fixed-plan stories
  even at structural boundaries. If a resumed adaptation run reaches
  `NeedsExpansion` or `NeedsNewVolume` but the confirmed plan still contains
  the next chapter, Router dispatches `writer` for that planned chapter instead
  of asking `architect_long` to call forbidden `expand_arc` / `append_volume`.
  Regression coverage includes incomplete confirmed adaptations at next-arc and
  next-volume boundaries. Validation passed for
  `go test ./internal/host/flow -count=1`, full `go test ./... -count=1`,
  and `git diff --check` with Windows line-ending warnings only.
- Latest source/runtime change: current local commit
  `fix: recover adaptation routing and review schema`:
  `save_review` now remains OpenAI-strict compatible by requiring
  `volume`, `arc`, `batch_from`, and `batch_to` on every call, with non
  `arc_batch` scopes instructed to pass `0`. `LoadState` now loads confirmed
  adaptation-plan facts before any pending arc-postprocess early return, so
  resumed adaptation projects keep their fixed chapter plan and do not route
  into forbidden `append_volume` / `expand_arc` recovery. Regression coverage
  was added for the strict tool schema and for pending arc postprocess plus
  confirmed adaptation state. Validation passed for focused
  `go test ./internal/tools ./internal/host/flow -count=1`, full
  `go test ./... -count=1`, and `git diff --check` with Windows line-ending
  warnings only before and after rebasing onto `9a3955af`
  (`fix: resume web adaptation stages`). Local Web was rebuilt/restarted on
  `http://127.0.0.1:9898` as PID `68984`; `武林高手`, `女主播的秘密`,
  `大学刑法课`, and `术士手册` were recovered to running states.
- Latest docs/test/static change: pending PR-05 (uncommitted per user request):
  Documented `simulation_mode` normal/reinforced behavior, default-normal
  semantics, Web enable path (`设定 -> 仿写画像 -> 仿写模式`), and compact-profile
  safety boundaries. Updated both example config files with safe
  `simulation_mode` comments. Added regressions for stage co-create normal vs
  reinforced/no-profile behavior, reinforced formal writing context mode,
  normal raw-report exclusion, Web project reopen persistence, and no
  `source_reports`/raw source leakage. Validation passed for full
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./...`,
  `npm --prefix internal/entry/web/ui test`,
  `npm --prefix internal/entry/web/ui run build`, README wording checks, and
  `git diff --check`; no commit/push/merge was performed.
- Latest source/runtime change: `6fcc5a8`
  `feat: apply reinforced simulation to novel context`:
  PR-04 of the reinforced simulation-mode pipeline adds
  `ContextToolOptions` and keeps the legacy `NewContextTool` constructor as
  normal-mode default. `agents.BuildCoordinator` now passes the effective
  simulation mode into `novel_context`; reinforced mode emits top-level
  `simulation_mode:"reinforced"`, uses the richer bounded compact profile, and
  marks loading summaries as `仿写模式:reinforced`. Role prompt loading now
  gives Architect, Writer, and Editor reinforced-mode guidance while preserving
  explicit-user-request priority and the ban on raw sources, `source_reports`,
  copying, characters, places, proprietary settings, and fixed plot bridges.
  Independent review passed. Validation passed for
  `go test ./internal/tools ./internal/agents ./assets`, full `go test ./...`,
  `git diff --check`, and `git diff --cached --check`.
- Latest source/runtime change: `3002468`
  `feat: inject reinforced simulation into cocreate`:
  PR-03 of the reinforced simulation-mode pipeline injects the compact
  simulation profile into cold-start and staged co-create only when
  `simulation_mode` is `reinforced` and a profile exists. Normal mode and
  missing-profile behavior keep the original prompts, `AdaptCoCreateStream`
  remains out of scope, and the co-create prompt payload is sanitized so
  `source_files`, `source_reports`, `simulate/` paths, and raw source report
  details are not sent to the model. Independent re-review passed. Validation
  passed for `go test ./internal/domain ./internal/host`, `git diff --check`,
  and `git diff --cached --check`.
- Latest source/runtime change: `2447c73`
  `feat: add simulation mode web setting`:
  PR-02 of the reinforced simulation-mode pipeline adds the Web project
  setting for `normal` / `reinforced` simulation mode, calls
  `/api/projects/{id}/simulation-mode`, warns when reinforced mode is selected
  without a loaded simulation profile, and keeps completed-but-idle projects
  editable while disabling saves during running/co-create states. Independent
  review passed. Validation passed for `npm --prefix internal/entry/web/ui
  test`, `npm --prefix internal/entry/web/ui run build`,
  `go test ./internal/entry/web`, and `git diff --check` with Windows
  line-ending warnings only.
- Latest source/runtime change: `98f4b15`
  `feat: add simulation mode backend config`:
  PR-01 of the reinforced simulation-mode pipeline adds project/global
  `simulation_mode` config support with `normal` as the default and
  `reinforced` as opt-in. The backend now validates, merges, persists project
  overlays, exposes `SimulationMode` in snapshots, and serves
  `PUT /api/projects/{id}/simulation-mode` without changing co-create prompts
  or `novel_context`. Independent review passed. Validation passed for
  `go test ./internal/bootstrap ./internal/host ./internal/entry/web` and
  `git diff --check` with Windows line-ending warnings only.
- Latest source/runtime change: current local commit
  `fix: save retry settings per project`:
  The Web retry-settings form now saves through
  `/api/projects/{id}/models/retry-settings` when a project is open instead
  of always writing `/api/models/retry-settings`. This fixes project overlays
  snapping back to old retry values after the UI refreshed project-scoped
  model settings. Validation passed for UI tests, UI build, and
  `go test ./internal/entry/web ./assets -count=1`. Web was rebuilt and
  restarted on `http://127.0.0.1:9898` as PID `15552`; a live project retry
  save for `project-20260705003848-bbd0eb` was confirmed on disk and through
  the API as 14/14/2 after rebasing onto `origin/main`.
- Latest source/runtime change: current local commit
  `fix: harden analysis structure parsing`:
  Adaptation source analysis now uses a prompt that shows bare `=== TAG ===`
  separators and the envelope parser accepts common Markdown-heading variants
  such as `### === SUMMARY ===`. Simulation-profile JSON parsing now repairs
  literal newline/carriage-return/tab control characters inside JSON strings
  before model-level structure repair retries. Web simulation source
  auto-splitting now targets roughly 8000 runes, rebalances existing generated
  `*.part_###_ch####-####.txt` files, preserves chapter numbering, and prunes
  stale profile sources/reports after re-splitting. Validation passed for
  `go test ./internal/host/imp ./internal/host/sim ./internal/entry/web ./internal/host/adapt`.
  Web was rebuilt and restarted on `http://127.0.0.1:9898` as PID `62212`.
  Project `天擎` (`project-20260708231542-466290`) was started for both
  adaptation source analysis and simulation-profile analysis; simulation
  sources were rebalanced from 108 to 215 files and early events advanced to
  `2/215` without the previous JSON newline parse error.
- Latest source/runtime change: current local commit
  `feat: auto-split long simulation sources`:
  Web simulation-profile uploads and analysis now preprocess long source files
  under each project's `simulate` directory. Files over 15000 runes are split
  with the existing `internal/host/imp.SplitFile` chapter splitter, then
  packaged into ordered `*.part_###_ch####-####.txt` files of roughly 15000
  runes. A single chapter over the target remains a single part rather than
  being cut mid-chapter. Suspicious splitter results now stop analysis with a
  clear error: very long novels (`>=500000` runes) with only a few recognized
  chapters (`<=20`) and giant chapter outliers relative to the median are
  treated as likely unsupported chapter-title formats. Validation passed for
  `go test ./internal/entry/web ./internal/host/imp ./internal/host/sim` and
  full `go test ./...`; Web was rebuilt and restarted on
  `http://127.0.0.1:9898` as PID `22424`.
- Latest source/runtime change: current local commit
  `fix: split episode-section novel headings`:
  TXT import/adaptation splitting now recognizes combined episode/section
  headings such as `第一集 奔向黎明 第一节 来自麻省理工的初中代课老师`
  and no-space variants such as `第二十一集疯狂时代 第一节最后的晚餐`.
  The rule is scoped to lines containing both `集` and `节`, so prose such as
  `第一节课是数学课` remains body content. Validation passed for
  `go test ./internal/host/imp -count=1`, and the real
  `D:\tool\068.天擎.txt` file now splits into 368 chapters and 108 upload
  packages with max package size 14911 runes and no oversize packages.
- Latest source/runtime change: current local commit
  `fix: block cross-batch adaptation outline duplicates`:
  The duplicate-outline regression occurred on latest `origin/main`
  (`d464c91b`, `fix: align arc source-range budget review`), not stale local
  code. Adaptation planner detail batches now validate deterministic duplicate
  outline promises against prior accepted detail batches and include prior
  chapters in borderline similarity review prompts. Full proposal validation
  and host confirmation also reject duplicate target outlines before confirmed
  plans reset checkpoints/generated output/progress. This guard checks repeated
  title/core_event/hook/scenes promises and does not use reduced target word
  count as a failure condition, so arc/full_rewrite compression behavior
  remains unchanged. Validation passed for focused duplicate/revision
  regressions and
  `go test ./internal/domain ./internal/host/adapt ./internal/host -count=1`.
- Latest source/runtime change: current local commit
  `fix: align arc source-range budget review`:
  Arc/full_rewrite final proposal and detail budget split validation now uses
  the same source-map review capacity exposed as
  `source_review_capacity_runes` (`ceil(5000 * 1.15) = 5750`) instead of the
  hard target chapter maximum. Per-target `word_budget.max_runes` remains
  capped at 5000. This keeps intentionally compressed arc ranges from failing
  only at final volume review after their skeleton batches were already reused,
  while still rejecting source ranges that do not allocate enough target
  chapters under the review capacity. Validation passed for focused arc budget
  split tests and full `go test ./internal/host/adapt -count=1`.
- Latest source/runtime change: current local commit
  `fix: globalize model config saves`:
  Web model provider saves now persist global provider configuration only and
  no longer switch the current model route. Project model routing keeps a
  single project default model at the top of the panel; coordinator,
  architect, writer, and editor can either choose a concrete provider/model or
  choose "默认模型" to follow that project default. Global provider edits
  synchronize safe provider metadata, auto-switch fallback candidates, and
  retry settings into active and closed project overlays without writing
  inherited API keys. Validation passed for `npm.cmd test`,
  `npm.cmd run build`, and
  `go test ./internal/host ./internal/entry/web -count=1`; embedded Web static
  assets were regenerated.
- Latest source/runtime change: current local commit
  `fix: soften arc source-map budget review`:
  Arc/full_rewrite source-map skeleton budget review now uses source-map
  review capacity (`ceil(5000 * 1.15) = 5750`) as the skeleton-stage signal,
  while final per-target `word_budget.max_runes` remains capped at 5000.
  Skeleton batches may carry optional `budget_decision` and `budget_reason`
  through runtime and volume-review state. Severe low/high budget deviations
  require matching compression/expansion rationale; retry exhaustion no longer
  accepts no-rationale severe deviations. Free/full_rewrite soft source-rune
  and shared-range behavior remains preserved. Validation passed for focused
  PR-02 source-map budget tests, free/full_rewrite regressions, and
  `go test ./internal/domain ./internal/bootstrap -count=1`; PR-02 received
  independent re-review `pass` before commit. Local Web was not restarted yet
  per user request to wait until all PRs are complete.
- Latest source/runtime change: current local commit
  `fix: tolerate source-map skeleton wrappers`:
  Source-map skeleton parsing now extracts a single deterministic JSON object
  from fenced/prose-wrapped planner responses, rejects multiple top-level
  objects as ambiguous, accepts explicit batches-array aliases and nested
  skeleton envelopes, and tolerates only top-level `mainline_rules` /
  `relationship_goals` string/object drift. Standalone batch objects, missing
  or non-array `batches`, empty batches, and core batch validation remain
  rejected. Validation passed for focused source-map parse and normalizer
  tests plus `go test ./internal/domain ./internal/bootstrap -count=1`; PR-01
  received independent re-review `pass` before commit. Local Web was not
  restarted because the current pipeline will restart only after all PRs are
  complete.
- Latest source/runtime change: current local commit
  `fix: replan invalid adaptation skeleton cache`:
  Source-map skeleton checkpoints now revalidate cached batches with exact
  per-source-chapter rune counts when a manifest is available. If a cached
  source-map range under-splits an oversized source span, only that invalid
  cached range is discarded and replanned; valid later cached ranges are
  re-offset, saved back to runtime, and reused. This addresses the
  `女主播的秘密` failure where all source-map skeleton batches appeared complete
  but final volume-review validation failed on chapter range `104-112`, then
  subsequent Start clicks reused the stale skeleton and failed immediately.
  Validation passed for focused skeleton-cache/rune-split regressions, full
  `go test ./internal/host/adapt -count=1`, and `git diff --check` with CRLF
  warnings only. Local Web was rebuilt and restarted via `restart-web.cmd` on
  `http://127.0.0.1:9898` as PID `44120`; the root page returned HTTP 200.
- Latest rebuild/static alignment change: current local commit
  `chore: align tracked web static index`:
  Pulled `origin/main` fast-forward from `bc770905` to `8f2b7c7d`
  (`fix: stabilize web static build output`), rebuilt and restarted local Web
  with the project-local Go 1.25.5 toolchain, stopped PID `47680`, and started
  `http://127.0.0.1:9898` as PID `63252`. The restart succeeded and
  `Invoke-WebRequest http://127.0.0.1:9898/` returned HTTP 200. The build
  revealed that the newly tracked static `index.html` contained one extra blank
  line not emitted by Vite; the tracked file was aligned to the actual build
  output so subsequent Web builds/restarts should stay clean.
- Latest tooling change: current local commit
  `chore: keep web static line endings stable`:
  Added `.gitattributes` rules that keep tracked embedded Web static output
  (`internal/entry/web/static/index.html`, CSS, and JS assets) as LF on
  Windows. This matches Vite build output under `core.autocrlf=true`, so
  running the Web build or restart script should no longer leave these tracked
  static files dirty with line-ending-only changes. Validation passed for two
  `npm.cmd run build` runs from `internal/entry/web/ui`; after the second
  build, Git status showed only the intentional `.gitattributes` addition and
  no static asset modifications. The running Web service was not restarted for
  this tooling-only fix per user request.
- Latest source/runtime change: current local commit
  `fix: include source-map budget notes in initial prompt`:
  Source-map skeleton prompts now include concrete per-entry
  `source_map_budget_notes` before the first model call. For long source
  ranges whose `source_runes` exceed the model chapter maximum, the prompt
  states the source rune count, the preservation-detail review target
  `chapter_count`, and the explicit compression/deletion exception up front
  instead of waiting for the first response to fail quality review. Validation
  passed for focused initial-budget-note prompt coverage, full
  `go test ./internal/host/adapt -count=1`, and `git diff --check` with CRLF
  warnings only.
- Latest source/runtime change: current local commit
  `fix: stabilize adaptation skeleton retry budgets`:
  Adaptation source-map skeleton chapter-budget quality retries now use the
  Web-configured structure-repair attempt count instead of a hardcoded 2,
  so event logs expose the configured retry denominator. Source-rune based
  chapter-count checks now act as review guidance with an explicit
  compression/deletion exception, allowing intentional condensed adaptations
  after quality retries instead of hard-failing on original-text size alone.
  General proposal skeleton normalization now ignores model-drifted
  `target_from` / `target_to` when `chapter_count` is present and reassigns
  continuous target ranges host-side, preventing failures like "batch target
  range starts at 156, want 165". Source-map skeleton normalization now drops
  pure future-range spillover batches returned by the model, such as a 41-70
  batch in a 1-40 request, while still requiring the current range to be fully
  covered. Runtime proposal checkpoints were backed up and cleared for
  `女主播的秘密` and `术士手册` so both keep co-create/source analysis state but
  can regenerate adaptation proposals with the new logic. Validation passed
  for focused skeleton retry/range/spillover tests, full
  `go test ./internal/host/adapt -count=1`, and `git diff --check` with CRLF
  warnings only.
- Latest rebuild/restart change: current local commit
  `chore: rebuild web static after latest pull`:
  Pulled `origin/main` fast-forward from `7e5b24d4` to `aaeeb2a7`
  (`fix: budget long-source adaptation splits`), rebuilt tracked Web static
  assets, and restarted local Web with repo-local Go on `PATH` via
  `restart-web.cmd -Port 9898 -RuntimeRoot C:\Users\RondleLiu\.ainovel\novels-preview`.
  The restart stopped PID `58844`, started `http://127.0.0.1:9898` as PID
  `55100`, and `/api/runtime` returned healthy with 18 visible projects and
  active project `project-20260705003848-bbd0eb`.
- Latest source/runtime change: `2f874c9`
  `fix: resume adaptation planning retries`:
  Long-form adaptation proposal planning now resumes partial source-map
  skeleton and detail runtime checkpoints instead of restarting from batch 1
  after a failure. Source-map skeleton calls ask the model for `chapter_count`
  only, while the backend assigns continuous `target_from` / `target_to`
  ranges. Chapter-budget deviations now use a separate soft quality retry loop
  that does not consume structure repair attempts; model-call connection
  retries remain scoped to each model request. High chapter budgets are no
  longer hard-failed after review, because added plot and long source chapter
  splitting can legitimately expand target chapters. Configurable caps were
  raised to 30 model-call attempts and 15 structure repair attempts, and Web
  event rows now show warning/error summary plus detail so retry counts are
  visible. Validation passed for full `internal/host/adapt`, focused
  bootstrap/web/sim packages, Web UI tests/build, `git diff --check` with CRLF
  warnings only, and local Web restart on `http://127.0.0.1:9898` as PID
  `58844`.
- Latest source/runtime change: `0bff8c7`
  `fix: switch models on text rate limits`:
  Runtime fallback now treats bare provider rate-limit messages such as
  `rate_limit_exceeded`, `rate limit`, `too many requests`, `request limit`,
  `rpm limit`, `tpm limit`, and `429` as immediately fallback-eligible
  `rate_limit` errors. Regression tests cover both a single first-error switch
  and the configured `deepseek-yuanyu-0` -> `deepseek-suifeng-0` ->
  `deepseek-suifeng-1` candidate pool, requiring each exhausted backend to be
  called only once before moving on. Validation passed for focused
  `internal/bootstrap` runtime fallback tests, and local Web was rebuilt and
  restarted on `http://127.0.0.1:9898` as PID `26484`.
- Latest source/runtime change: current local commit
  `fix: chunk long adaptation proposals by input size`:
  Long `arc`/`free` adaptation proposal routing now considers source input
  size as well as target chapter count. If the analyzed source is long by
  chapter count or dossier rune budget, the planner uses the compact source-map
  skeleton and batch-detail flow even when the brief or explicit target count
  is below the old 18-chapter threshold, preventing large novels from sending
  full `source_reports` in one model request and hitting `content_too_long` or
  HTML gateway error pages. Validation passed for full `internal/host/adapt`,
  focused Web adaptation/co-create backend tests, `git diff --check` with CRLF
  warnings only, and local Web rebuild/restart on `http://127.0.0.1:9898` as
  PID `30196`.
- Latest source/runtime change: current local commit
  `fix: split simulation retry budgets`:
  仿写语料分析、画像合成、画像导入后的重合成 now split transient
  model-call retries from structured-output repairs. Model/network failures use
  the configured `model_call_max_attempts`; invalid structured JSON uses
  `structure_repair_max_attempts`; each source, merge batch, and repair request
  counts attempts independently. SIMULATE retry events now label model-call
  retries versus structure repairs, and the Web event feed shows error/warn
  `detail` text so failures expose the concrete cause instead of only a summary
  such as `语料分析失败`. Validation passed for focused sim tests,
  `go test ./internal/host/sim ./internal/host -count=1`,
  `go test ./internal/entry/web -count=1`, Web UI tests/build,
  `git diff --check`, and local Web restart on `http://127.0.0.1:9898` as PID
  `55452`.
- Latest rebuild/restart change: current local commit
  `0b43458` `chore: rebuild web static after splitter fix`:
  After the splitter fix was pushed, local Web was rebuilt and restarted via
  `restart-web.cmd -Port 9898 -RuntimeRoot C:\Users\RondleLiu\.ainovel\novels-preview`.
  The restart rebuilt the tracked static Web index, started
  `http://127.0.0.1:9898` as PID `8896`, and `/api/runtime` returned healthy
  with 19 visible projects.
- Latest source change: current local commit
  `59875ba` `fix: support compact source headings`:
  The import splitter now recognizes compact Arabic-number headings such as
  `第03章标题` / `正文 第03章标题` and bare Chinese section headings such as
  `十一节 标题`, while keeping malformed source headings as source-data fixes
  instead of parser behavior. Validation passed for
  `go test ./internal/host/imp`. Local ignored novel outputs were regenerated:
  `novel/split_packages` for `女主播的秘密.txt` reports 490 chapters, 86 packages,
  max 14961 runes, oversize 0; `novel/split_packages_wulin` for
  `武林高手在校园-墨武.txt` reports 451 chapters, 124 packages, max 14941 runes,
  oversize 0.
- Latest source/runtime change: current local commit
  `9209524` `fix: fail over missing-token providers`:
  Runtime auto-switch and explicit role fallbacks now classify provider
  auth/token configuration failures such as `no token`, `missing token`, and
  `invalid token` as fallback-worthy `auth_failed` errors. Adaptation
  dependency creation and co-create streams now use the architect fallback
  wrapper consistently, so a broken `deepseek-yuanyu-0` backend can switch to
  another configured candidate/fallback provider instead of failing the whole
  source-analysis or co-create step. Validation passed for focused backend
  tests, full `go test ./...`, and local Web rebuild/restart on
  `http://127.0.0.1:9898` as PID `17368`. Known unrelated uncommitted local
  edits remain in `internal/host/imp/splitter.go` and
  `internal/host/imp/splitter_test.go`.
- Latest source/runtime change: current local commit
  `fix: preserve loaded novel analysis`:
  Completed loaded novels now no-op when Analyze is clicked, returning
  `analyzed=true` without starting source analysis. Prepared source snapshots
  with complete reports and foundation can backfill missing co-create dossier
  batches without re-splitting the TXT, so legacy novel-library entries that
  only lack `cocreate_dossier.json` / `cocreate_dossier_batches/` keep their
  single-chapter reports and sync back to the library after backfill. Source
  snapshot reset and novel-library load replacement now back up existing
  `meta/adaptation` first. Adapt co-create validation now accepts a complete
  prepared snapshot for the selected source path, preventing
  `selected adaptation source has not been analyzed` after loading a library
  novel. Runtime data repair was performed under
  `C:\Users\RondleLiu\.ainovel\novels-preview`: `术士手册` reports 1-946 were
  backed up and SHA-aligned to the current manifest so analysis resumes at
  chapter 947; `女神攻略`, `回档无限`, and `诡案组` project adaptation snapshots
  were restored from the novel library with backups. Current known library
  statuses: complete entries include `大学刑法课`, `国中理化课`, `回档无限：为她纯洁一身`,
  `娇妻美妾任君尝`, `梦中的女孩`, `我以我血挽山河`, `我有一座冒险屋`, `择天记`,
  and `这不是我印象中的女魔头`; `女神攻略` and `诡案组` are complete through
  reports/foundation and need dossier-only backfill. Validation passed for
  full `go test ./...`, focused Web UI Vitest, Vite build, `git diff --check`
  with CRLF warnings only, and local Web restart on `http://127.0.0.1:9898`
  as PID `30172`.
- Latest source/runtime change: current local commit
  `feat: stabilize long-form adaptation co-create and planner batches`:
  Adaptation co-create now uses an explicit `force_rebrief` path for the
  "重新分析资料包" action; ordinary follow-up comments merge as lightweight draft
  increments and do not rerun full-book briefing. Co-create decisions can be
  submitted in one bulk UI/API request, while resolved decisions are persisted
  together and merged into the draft without repeated full-draft rewrites.
  Long-form planner skeleton prompts now use a compact source-foundation digest
  that omits arc chapter details, tolerate a single returned batch object by
  wrapping it into `batches`, split oversized skeleton batches into 4-chapter
  detail sub-batches, and pass previous detail context forward for continuity.
  The Web UI assets were rebuilt and the default co-create timeout is now 180s.
  Validation passed for focused backend packages, Web UI Vitest, and Vite build;
  final restart is handled by the local Web restart step after commit.
- Latest source/runtime change: pending local changes
  `fix: repair source splitting and library backfill sync`:
  Adaptation source splitting now filters source-site duplicate `免费阅读`
  chapter headings and separator-delimited author-note blocks before chapter
  detection/body persistence, fixing the `术师手册` split that produced 1444
  noisy units and failed on empty `characters`. Web adaptation-analysis
  completion now treats only terminal `adapt.StageError` events as failed, so
  recoverable dossier warning/retry events no longer skip the existing
  novel-library refresh callback after final `StageDone`. The local
  `术师手册` project snapshot was rebuilt to 1382 chapters and stale artifacts
  from chapter 947 onward plus derived dossier/foundation caches were removed.
  Validation passed for focused and full `internal/host/imp` and
  `internal/entry/web` Go tests, splitter packaging check on `术师手册.txt`,
  runtime cleanup verification, and local Web rebuild/restart on
  `http://127.0.0.1:9898` as PID `59984`.
- Latest source change: pending local changes
  `fix: strip novel-library cocreate progress`:
  Web novel-library save/load now allow-lists adaptation source analysis and
  co-create dossier artifacts instead of copying runtime co-create progress.
  Library save omits `cocreate_intent.json`, `cocreate_briefing.json`,
  `cocreate_briefing_batches/`, proposals, plans, checks, and session files;
  library load also clears the target project's Web co-create checkpoint/log
  and resets the frontend co-create state before displaying the loaded
  snapshot. The existing `这不是我印象中的女魔头` library entry and the two
  affected local projects were cleaned so their snapshots show no active
  co-create state while adaptation analysis remains `done`.
  Validation passed for focused and full Web Go tests, full Web UI Vitest,
  Vite build, `git diff --check` with CRLF warnings only, local Web restart on
  `http://127.0.0.1:9898` as PID `57712`, and HTTP/file smoke checks confirming
  no forbidden co-create progress paths remain. This batch is not committed yet
  because the worktree already contains broad unrelated changes, including
  pre-existing edits in the same frontend/static bundle files.
- Latest source change: current local commit
  `fix: refresh adaptation cocreate briefing intent`:
  Adaptation co-create now derives the long-novel briefing intent from all
  current user planning turns plus resolved briefing decisions, not only the
  first user request. Web send/revise refreshes stale intent-driven briefing
  before the next adapt co-create response and blocks on any newly generated
  required decisions. The detailed staged adaptation proposal planner still
  runs only after the user starts generation from the final draft.
  Validation passed for
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host/adapt`
  and `git diff --check` on the changed co-create/session files, with CRLF
  conversion warnings only.
- Latest source change: current local commit
  `fix: make adaptation co-create resumable`:
  Web adaptation co-create decisions now render as a one-question pager with
  vertical wrapping option buttons, and the backend no longer limits pending
  decisions to the first four. Failed co-create sessions stay active with a
  `恢复共创` action, and `/cocreate/resume` continues from the saved prompt,
  mode, source, resolved briefing decisions, and draft state instead of losing
  progress.
  Provider gateway failures (typed or HTML 500/502/503/504 responses) are
  detected, sanitized, and treated as failover/retry-eligible in runtime
  fallback, co-create stream generation, planner generation, and briefing
  generation. Adaptation co-create briefing now checkpoints before generation
  starts and writes an explicit `COCREATE/adapt_briefing` lifecycle event, so
  briefing failures appear in the event log rather than silently ending.
  Validation passed for focused provider/co-create/briefing tests, full
  `go test ./internal/retrypolicy ./internal/bootstrap ./internal/host ./internal/host/adapt ./internal/entry/web -count=1`,
  full Web UI Vitest, Vite build, `git diff --check` on changed source files,
  and local Web restart on `http://127.0.0.1:9898` as PID `62520`. Runtime
  snapshot confirmed `这不是我印象中的女魔头`
  (`untitled-novel-20260705003748-de4f76`) is resumable with 19/19 briefing
  decisions resolved and no pending decisions.
- Latest source change: current local commit
  `fix: unblock adaptation library save and cocreate shell`:
  Web adaptation novel-library save now separates row editability from submit
  readiness, so a freshly analyzed source with an empty library name can still
  accept a name before enabling the save action. The adaptation save row now
  labels itself as `保存当前小说到库` while the simulation-profile row keeps
  `保存当前画像到库`.
  Web co-create begin/send/revise/decision/commit/cancel no longer owns the
  global shell `busy` flag; long adaptation co-create briefing requests set a
  current-project busy state instead, so the left-side new-project flow stays
  available. Awaited co-create responses capture the project ID and are ignored
  after switching or creating another project to avoid stale state pollution.
  Validation passed for focused and full Web UI Vitest, Vite build, focused Go
  Web tests, `git diff --check`, local Web restart on
  `http://127.0.0.1:9898` as PID `63112`, and HTTP smoke checks for `/`,
  `/api/runtime`, and `/api/projects`.
- Latest integrated change: current local commit
  `fix: stabilize web cocreate and source foundation resume`:
  The final integration includes the concurrent Web/co-create review UI,
  workspace display, retry-configuration, prompt-style, and static Web asset
  changes plus resumable source-foundation aggregation for adaptation analysis.
  Source foundation merge now persists report-level and summary-level
  checkpoints under `meta/adaptation/source_foundation_batches`, validates them
  with source/report/prompt/batch signatures, resumes after restart from the
  last completed merge batch, and clears checkpoints after the final
  `source_foundation.json` save. Validation passed for focused source
  foundation packages, full Web UI Vitest, Vite build, and full `go test ./...`.
- Latest prompt change: pending local changes
  `style: harden anti-ai revision guidance`:
  Writer style prompts and the shared `anti-ai-tone` reference now explicitly
  target over-smooth prose, over-complete explanatory detail, too-regular
  subject rhythm, polished metaphor/gold-line habits, over-clear psychological
  self-defense, broken-memory realism, and functionalized side characters. The
  writer rewrite/polish prompt now adds a specific anti-AI revision path:
  identify paragraph-level problem clusters first, allow local structural
  deletion/reordering, avoid replacing old lists with new lists, and avoid
  swapping old gold-line endings for new ones.
  Validation passed for focused Go style/loading/Web style tests with the
  project-local Go 1.25.5 binary and `git diff --check` on the changed prompt
  files, with CRLF conversion warnings only. The broader workspace already has
  unrelated concurrent changes; this prompt-only batch was not committed or
  pushed here.
- Latest source change: current local commit
  `fix: stabilize adaptation analysis and co-create review`:
  Adaptation source foundation merge now batches by source-report rune budget
  and recursively merges partial foundations, so large novels no longer send a
  single oversize request that can trigger HTTP 413. Source dossier, briefing,
  and structural repair attempts now honor Web-configurable retry settings, and
  loading older novel-library entries missing current dossiers starts analysis
  backfill instead of reporting them fully analyzed.
  Ordinary co-create stores a planning review gate, exposes pending
  volume/chapter-outline review in the Web UI, and requires the user to approve
  before writer resume. Web project switching resets stream/event workbench
  state to prevent cross-project display leakage, stream rounds are clipped to
  avoid text overlap, and writer `draft_chapter` can infer a missing chapter
  only when the runtime context is uniquely safe. Validation passed for
  `go test ./...`, Web UI Vitest (96 tests), and Vite build. Local Web was
  rebuilt and restarted first on `http://127.0.0.1:9898` as PID `59568`.
- Latest source change: current local commit
  `fix: neutralize adaptation dossier generation`:
  Adaptation co-create dossiers now use prompt version `v2` and store neutral
  source facts only: main causality, plot threads, character arcs,
  world/continuity constraints, relationship signals, and evidence-backed
  heroine/ambiguity/couple signals. Dossier-stage adaptation advice is no
  longer requested, displayed, or saved; malformed/empty dossier JSON now gets
  in-call repair retries before failing. Dossier batches remain capped at 40
  chapters but also split early at roughly 120k source runes for long-chapter
  novels, and current checks validate every dynamic batch range/signature.
  Validation passed for focused adapt/store/host/web tests and full
  `go test ./internal/...`; local Web was rebuilt/restarted on
  `http://127.0.0.1:9898` as PID `60036`. Current project generated
  dossier/briefing artifacts for `project-20260705003734-24bbf4` were deleted
  so Analyze can regenerate v2 dossiers from preserved source reports.
- Latest source change: current local commit
  `fix: backfill adaptation dossier batches`:
  Adaptation source-analysis resume now backfills fixed 40-chapter co-create
  dossier batches before continuing with newly analyzed chapters, and refreshes
  those batches again whenever a non-final 40-chapter boundary is reached. The
  final all-book co-create dossier still assembles only after all source reports
  are complete. Web novel-library save/load actions now append `LIBRARY`
  host events, and long-form adaptation co-create briefing progress is emitted
  as `ADAPT` events. Validation passed for focused adapt/web Go tests and full
  `go test ./internal/...`; local Web was rebuilt/restarted on
  `http://127.0.0.1:9898` as PID `42772`, and `/api/runtime` returned healthy.
- Latest source change: pending local changes
  `fix: refresh project model provider renames`:
  Web global model/provider edits now propagate the edited
  `original_provider -> provider` ID change to closed project overlays and
  active project sessions. Project configs rewrite default, role, fallback, and
  provider-map references away from the old provider ID, refresh provider
  display labels from the saved global config, and active hosts reapply the
  updated model registry without requiring each project to be opened manually.
  Validation passed for full `go test ./...`, full Web UI Vitest, Vite build,
  current runtime project-config scan with zero unknown provider refs, and one
  connectivity test per configured provider/model combo.
- Latest source change: pending local changes
  `feat: resynthesize imported simulation profiles`:
  Web simulation UI now keeps the profile library controls at the top, with an
  inline profile-name input directly above the full-width "保存当前画像到库"
  action, next to the existing "上传 JSON 到库" flow. It no longer auto-opens or
  auto-starts any save flow when analysis completes, and the "加载画像" import
  section moved above the TXT/MD corpus list so it remains reachable after large
  uploads. Importing a `simulation_profile.v1` JSON into a project that already
  has a profile now first merges sources and source reports by fingerprint, then
  uses the existing batched simulation merge pipeline to re-synthesize
  `synthesis` with the architect model; large imports are compacted and split by
  report count/prompt size, with merge checkpoints saved after successful
  batches and cleared after final save. Validation passed for full Web UI tests,
  full `go test ./...`, Vite build, `git diff --check` with CRLF warnings only,
  and local Web restart on `http://127.0.0.1:9898` as PID `59192`. The final
  static bundle no longer contains the removed save-dialog markers.
- Latest source change: current local commit
  `fix: hydrate project event history on open`:
  Web project switching now hydrates the workbench from a JSON event-history
  endpoint before relying on the live SSE stream. The new
  `/api/projects/{id}/events/history?after=N` route is read-only, does not
  append snapshots, and returns replay metadata (`oldest_seq`, `latest_seq`,
  `history_limit`) alongside `WebEvent` rows. Opening a running project applies
  those events through the existing reducer and updates the SSE cursor, so the
  event panel shows historical ADAPT/SIMULATE rows immediately after switching
  projects instead of waiting for the next live event. Validation passed for
  focused and full Web UI tests, focused Go web tests, Vite build, local Web
  restart on `http://127.0.0.1:9898`, and Playwright switching across seven
  running projects with no `暂无事件` state.
- Latest source change: `20efdd9`
  `fix: preserve project events on open`:
  Web project switching now merges the loaded snapshot into the existing
  workbench instead of replacing the whole workbench. This preserves SSE
  history replay and current running event rows when `/events?after=0` returns
  before `/snapshot`, so switching to an already-running project immediately
  shows its events. Validation passed for the focused Web event/progress tests,
  full Web UI Vitest suite, and Vite build. Local Web restart was attempted but
  blocked by unrelated dirty simulation source files that currently break Go
  build; those files were left uncommitted.
- Latest source change: current local commit
  `fix: clarify web model config flow`:
  Web model configuration now separates existing-provider editing from a clean
  `+ 新建` model flow in the right sidebar's one-column layout. New drafts no
  longer inherit the selected provider's Base URL/API key/model and no longer
  show or send an agent/role selection; they register provider/model config via
  `select_after_save:false`, leaving default/coordinator/architect/writer/editor
  assignment to the existing model selectors. `provider key` is labeled as a
  local configuration ID rather than an API key, new model drafts default request
  timeout to 120 seconds, connectivity timeout to 12 seconds, and network retry
  count to 7, and save validation surfaces inline errors instead of appearing
  inert. Existing-provider test/discovery requests now pass `original_provider`
  through the configured-provider path so testing `deepseek` no longer fails
  with a duplicate-provider add-flow error. Existing-provider saves skip upstream
  probes for local-only edits such as ID/display-name/timeouts/candidate-pool
  changes, so an exhausted or invalid upstream token does not block local
  renames; model/Base URL/protocol/auth/API-key changes still probe. Validation
  passed for focused/full Web UI tests, focused Go host/web tests before and
  after Web build, Vite build, and `git diff --check` with CRLF warnings only.
  Local Web was rebuilt/restarted on `http://127.0.0.1:9898`; old PID `18908`
  was stopped, new PID is `54724`, and `/` plus `/api/models` returned HTTP 200.
- Latest source change: current local commit
  `feat: add adaptation cocreate briefing`:
  Adapt co-create now builds an intent-driven pre-draft briefing for oversized
  source novels, using persisted all-book dossier batches as input instead of
  sending the whole dossier into one draft call. The briefing is keyed by source
  signature, dossier prompt version, briefing prompt version, and user intent
  hash; pending required decisions block draft generation until the user answers
  them one at a time. Web and TUI start/proposal paths share the readiness guard,
  Web exposes a compact decision queue, and adapt prompts prefer the resolved
  briefing snapshot before falling back to the all-book dossier. Validation
  passed for Web UI tests/build, focused Go tests, full `go test ./internal/...`,
  and `git diff --check` with CRLF warnings only. Local Web was rebuilt and
  restarted on `http://127.0.0.1:9898` with runtime root
  `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `59188`; `/`,
  `/api/runtime`, and `/api/models` returned HTTP 200.
- Latest source change: current local commit
  `feat: add adaptation cocreate dossier`:
  Adaptation source analysis now generates a resumable all-book co-create
  dossier from per-chapter source reports after source foundation, replacing
  the previous adapt co-create prompt that only exposed the first 30 chapter
  summaries. Web/TUI analysis progress includes the dossier stage; Web
  `analysis_status=done`, adapt co-create begin, and novel library save/load now
  require a current dossier. Adapt co-create uses an 8192-token output cap and
  rejects provider length truncation or incomplete XML before accepting drafts.
  Validation passed for Web UI tests/build, focused Go tests, full
  `go test ./internal/...`, and `git diff --check` with CRLF warnings only.
- Latest source change: branch `codex/project-runtime-model-fallback`, pending commit
  `feat: add project runtime model fallback`:
  Runtime model fallback is project-scoped and switches all project agents
  together without mutating the global default provider/model. Quota/auth/rate
  limit/overloaded runtime fallback errors switch candidates immediately, while
  network or stream interruptions use the configured per-model network attempt
  budget first. Web model configuration now uses one configure flow for adding
  and editing providers, including provider key/name changes, key replacement
  with blank-key preservation, Base URL/API/auth/proxy/timeouts, and candidate
  pool membership. Manual default model switches now cascade to coordinator,
  architect, writer, and editor routes. Validation passed for focused and full
  changed-package Go tests, full Web UI tests, Vite build, and
  `git diff --check` with CRLF warnings only. Local Web was rebuilt/restarted
  on `http://127.0.0.1:9898` with runtime root
  `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `56176`; HTTP smoke
  checks for `/`, `/api/runtime`, and `/api/models` returned healthy responses.
- Latest source change: current local commit
  `fix: accept legacy timeline strings`:
  `domain.TimelineEvent` now accepts legacy JSON string entries by preserving
  the text as the event field, while normal object entries keep chapter, time,
  event, and character fields. This covers import/source-analysis `TIMELINE`
  sections, existing `timeline.json` files, and `commit_chapter` append rereads,
  preventing `json: cannot unmarshal string into Go value of type
  domain.TimelineEvent` from escaping as a global Web error banner. Validation
  passed for focused domain/import/store tests, full `go test ./...`, Web
  rebuild/restart on `http://127.0.0.1:9898` as PID `57820`, HTTP smoke checks,
  and `git diff --check` with CRLF warnings only.
- Latest source change: current local commit
  `fix: unblock concurrent web analysis controls`:
  Web simulation/adaptation analysis and simulation-profile library import now
  start as background project actions and return immediately, so long-running
  analysis does not keep browser requests open or exhaust the browser's
  per-origin connection pool while other projects are active. The React side
  panels restore per-project simulation/adaptation running state from snapshots
  and SSE host events, scope upload/analyze button disabling to the current
  project action instead of one global busy flag, and show the active project's
  default model route in the model panel. Runtime project configs for the
  user's current source/corpus projects were switched to
  `deepseek/deepseek-v4-pro`; missing uploaded source/corpus files were restored
  from `D:\ainovel\novel` and `D:\ainovel\novel\split_packages`.
  Validation passed for focused Web UI tests, full Web UI tests, Vite build,
  full `go test ./...`, `git diff --check` with CRLF warnings only, Web restart
  on `http://127.0.0.1:9898` as PID `55992`, and HTTP smoke checks for runtime,
  project listing, and the affected source/corpus project snapshots.
- Latest source change: current local commit
  `fix: restore simulation uploads after refresh`:
  Web snapshots now include uploaded simulation TXT/MD source files from each
  project's `simulate/` directory, and the React simulation panel restores that
  list after refresh/open instead of showing `暂无上传语料`. Project switching no
  longer waits on the global busy state before visually selecting the clicked
  project; stale project-open, SSE, simulation-analysis, and adaptation-analysis
  responses are ignored after the user switches elsewhere. Validation passed for
  full Web UI tests, Vite build, full `go test ./...`, and local Web restart on
  `http://127.0.0.1:9898` as PID `23060`.
- Latest source change: current local commit
  `fix: remove text source upload size limit`:
  Web TXT/MD source uploads no longer use the old 10 MiB per-file limit or the
  64 MiB multipart request cap. This applies to simulation/仿写语料 uploads,
  adaptation/小说改编 source uploads, and external novel imports. JSON simulation
  profile uploads still keep the 5 MiB per-file limit and 64 MiB multipart cap.
  Validation passed for focused Web upload regressions, full
  `./internal/entry/web`, full `go test ./...`, `git diff --check` with CRLF
  warnings only, Vite build via `restart-web.cmd`, and HTTP smoke checks on
  `http://127.0.0.1:9898`; local Web restarted as PID `53056`.
- Latest source change: current local commit
  `feat: allow parallel analysis and partial export`:
  Web export remains a read-only current-progress operation and can run without
  pausing or aborting writing/analysis. `ProjectSession` now tracks action kinds
  instead of one global action mutex, allowing adaptation source analysis to run
  concurrently with simulation profile analysis/import while still rejecting
  writing, continue, revision, proposal, and duplicate profile actions that
  would conflict. The React side panels no longer hold global `busy` for
  adaptation/profile analysis, so users can upload original text to the
  simulation-profile flow and start profile analysis while adaptation source
  analysis is still running. Validation passed for Web UI tests, Vite build,
  focused and full Go tests, and `git diff --check` with CRLF warnings only.
  The local Web service was rebuilt/restarted on `http://127.0.0.1:9898` with
  runtime root `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `35376`;
  HTTP `/`, `/api/runtime`, and `/api/projects` returned 200.
- Previous source change: current local commit
  `feat: save exports through browser picker`:
  Web export changed from "server writes under project exports" as the primary
  UI path to a browser-save flow. `POST
  /api/projects/{id}/export/download` returns the exported TXT/EPUB bytes and
  metadata headers; the frontend opens the native save-file picker when
  available and writes the returned blob to the chosen location, with browser
  download fallback. The workbench stream view now keeps more recent tool
  output blocks and no longer clips long stream cards, so adaptation/rewrite
  summaries can be read fully inside the central workspace.
- Latest prompt change: current local commit
  `style: add punctuation anti-ai review criteria`:
  `assets/references/anti-ai-tone.md` now includes an explicit
  "标点与句式 AI 味" section. Writer/editor shared AI-tone guidance now treats
  repeated explanatory em dashes as a reviewable aesthetic issue and recommends
  short sentences, action pauses, environmental detail, or natural paragraph
  breaks instead.
- Latest source change: current local commit
  `fix: preview completed chapter revisions`:
  Completed-book single-chapter revision now shows the selected chapter in the
  central workspace. The Web API `GET /api/projects/{id}/chapters/{chapter}`
  reads the current final chapter body from `chapters/NN.md`, falls back to
  draft text when no final exists, and returns word count/source metadata. The
  React UI lazy-loads only the selected chapter, shares the existing right-pane
  selection state, and renders loading/error/empty/body states in the main
  work area. Validation passed for focused Web API/UI tests, the broader
  `./internal/entry/web ./internal/host/flow ./internal/tools ./internal/store`
  Go package set, UI build, `git diff --check` with CRLF warnings only, and a
  live smoke on `http://127.0.0.1:9898`; project `梦中的女孩3` returned 59 outline
  rows and chapter 1 final content through the new endpoint. The local Web
  service was rebuilt and restarted with runtime root
  `C:\Users\RondleLiu\.ainovel\novels-preview` as PID `40604`.
- Latest source change: current local commit
  `feat: add completed chapter revision backend`:
  Completed-book single-chapter revision is implemented across Web UI and
  backend orchestration. The Web API `POST /api/projects/{id}/chapters/revise`
  accepts a completed chapter, instruction, and `rewrite`/`polish` mode, then
  reopens the completed book through `PendingRewrites` and resumes the existing
  writer flow. The UI adds a completed-book chapter revision panel, with tests
  covering visibility, payload validation, and API wrapper behavior. Backend
  validation passed for `./internal/entry/web ./internal/host/flow
  ./internal/tools ./internal/store`; frontend validation passed for
  `npm --prefix internal/entry/web/ui test -- --run`.
- Latest prompt change: current local commit
  `style: tighten style prompt anti-ai guidance`:
  All embedded style prompts now add non-NSFW guidance against numbered
  report-like reasoning, frequent explanatory em dashes, and psychology written
  as model-style inference lists. Validation passed for focused assets tests
  with the repo-local Go toolchain and `git diff --check` with CRLF warnings
  only.
- Latest source change: branch `main`, current commit
  `fix: infer explicit cocreate word count`:
  Web normal co-create now inspects the initial idea for an explicit total word
  count such as `5000字`, `三万字`, `十万字`, or `30w字`. When present, it sends the
  inferred `target_total_words` directly with the first co-create request. When
  the user gives only a vague length label such as `长篇`, the Web UI opens the
  target-word confirmation step instead of mapping that label to a fixed number.
  Per-chapter counts such as `每章5000字` are ignored for total-budget inference.
  The local Web service was rebuilt and restarted on `http://127.0.0.1:9898`
  with runtime root `C:\Users\RondleLiu\.ainovel\novels-preview` and PID
  `43080`.
- Previous source change: `a03cab2`
  `fix: normalize web export file extension`:
  Web export resolves the selected format before normalizing user-provided
  filenames. Relative names without an export suffix get `.txt` or `.epub`
  appended from the selected format, so title-only inputs such as
  `梦中的女孩改` no longer produce extensionless files. Explicit `.txt`/`.epub`
  suffixes that conflict with the selected format are rejected before the host
  export is called.
- Previous docs checkpoint: `21eb5c6`
  `docs: record adaptation mode guidance fix`.
- Previous source change: `ee471d2`
  `fix: split adaptation mode guidance`:
  adaptation mode prompts and runtime writer context now separate
  `chapter/preserve_details`, `arc/full_rewrite`, and `free/full_rewrite`
  instead of mixing all mode rules into one prompt. `free` mode now carries an
  explicit `adaptation_effective_mode` contract: source refs are background
  anchors only, `preserve_details` is not applicable, and `read_chapter`
  against original source is optional fact-checking rather than a required
  per-chapter step. The Web static assets were rebuilt to include the
  source-label UI update.
- Previous source change: `976a1fb`
  `fix: hide free adaptation source labels`:
  proposal chapter cards/rows and staged volume review details no longer render
  original-source mapping labels such as `原 17-17` or `原作范围` for
  `granularity=free`; `chapter` and `arc` keep those labels.
- Previous source change: `916f7fc`
  `fix: label source chapter reads`:
  `read_chapter` events now distinguish original-source reads from generated
  chapter reads by displaying `·原文` for `source:"source"`, so adaptation
  reference calls like `read_chapter(第17章·原文)` no longer look like the
  writer is reading an old generated chapter while drafting chapter 52.
- Previous source change: `ffb3dc2`
  `fix: repair malformed tool call args`:
  added a deterministic blind `ToolCallRepairModel` wrapper for
  architect/writer/editor/coordinator models. It repairs only lossless JSON
  object tool-call framing mistakes such as `{}""` or `{"chapter":1}""` before
  agentcore validation, while truncated JSON stays invalid so the originating
  model must retry with real content. `save_review` now opts into strict schema
  with required empty-default fields, and observer events downgrade the first
  malformed tool-argument validation failure per agent/tool to a yellow warning
  before escalating repeated consecutive failures to red errors.
- Previous source change: `1824d24`
  `fix: validate cocreate suggestions and dedupe volume review`:
  Web co-create suggestion buttons now only render backend-provided suggestions.
  When the streamed co-create reply omits structured suggestions, the backend
  runs a bounded JSON-mode model judge to decide whether real user-clickable
  suggestions exist; generic planning prose and "is this expected" follow-ups
  return no buttons. The staged adaptation volume-review UI also deduplicates
  mirrored `VolumeReviewSummary` and `AdaptationVolumeReview` volumes before
  rendering selectors, so each planned volume appears once while preserving the
  detailed review payload.
- Previous source change: `a9c0485`
  `fix: reject collapsed adaptation cocreate drafts`:
  adaptation co-create now treats placeholder drafts such as
  `同前轮（完整保留）`, `上一轮`, or previous-draft references as incomplete
  instead of letting them become the canonical proposal brief. Bad drafts still
  enter the existing model repair path; if repair fails, the session rolls back
  to the previous stable draft and blocks proposal generation until the latest
  user turn is consolidated. The Web adapt co-create entry also falls back to
  the current adaptation brief when the side-panel input is empty, avoiding an
  empty initial direction from the generic Adapt button.
- Previous source change: `99e66b4`
  `fix: let adaptation volume planner choose expansion`:
  selected-volume proposal revisions now always give the volume skeleton model
  an explicit `expansion_decision` choice (`expand` or `keep`) instead of using
  local keywords to force expansion. Detail batches are validated before merge;
  missing or invalid `word_budget`/chapter fields now trigger the existing
  batch repair loop instead of failing only at final proposal validation.
- Previous source change: `55c4162`
  `fix: enforce adaptation volume expansion revisions`:
  selected-volume proposal revisions now distinguish optional expansion from
  required expansion. If the user's volume revision asks to add/supplement plot
  content, a returned volume skeleton must increase `target_to`; unchanged
  chapter counts are repaired or rejected. Revised volume title/theme/goal/
  summary metadata is also written back to the saved proposal volume.
- Previous source change: `44e0986`
  `fix: expand adaptation volume revisions`:
  selected-volume proposal revisions now re-plan the volume skeleton first,
  then regenerate detailed chapter outlines; volume expansion shifts later
  chapters and later volume ranges by the added count.
- Previous source change: `2379137`
  `fix: dedupe adaptation proposal volumes in web ui`.
- Previous docs checkpoint: `051f4c7`
  `docs: record adaptation volume dedupe validation`.
- Previous source change: `af1b10d`
  `fix: remove stage wording from adaptation guidance`.
- Previous source change: `2f68cc2`
  `fix: stabilize adaptation proposal coverage`.
- Previous source change: `331162c`
  `fix: restore web cocreate action controls`.
- Earlier planner stability checkpoint: `411c1df`
  `fix: retry adaptation planner calls`.
- Branch: `main`
- Remote: `origin` -> `https://github.com/while4234/ainovel-cli.git`
- Working tree: clean after committed adaptation mode guidance and free-source
  label UI fixes, plus
  ignored local `.codex/`, `.playwright-mcp/`, and output/runtime artifacts.
- Runtime: local Web was rebuilt and restarted on `http://127.0.0.1:9898` from
  source commit `ee471d2`; the restarted process is PID `47552`.
- Validation:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/startup ./internal/entry/tui ./internal/entry/web ./internal/host ./internal/tools -run "Test(DefaultAdaptationBrief|AdaptationModeContract|AdaptCoCreateOpener|AdaptModeConfirmStartsCoCreateWithSelectedMode|ContextToolClarifiesFreeFullRewriteSourceRefsAreAnchors|ContextToolShowsDisabledWordToleranceForFullRewrite|AdaptationFreePlanDoesNotRequireSourceRead|PreserveDetailsPlanAndDraftSteerTowardFullSourceBackedChapter|LoadKeepsAdaptationGuidanceOutOfBaseWriterPrompt|ProjectAdaptCoCreateCheckpointRestoresModeAndCommits)" -count=1`
  passed;
  `npm.cmd --prefix internal/entry/web/ui test -- src/workspace-progress.test.js --run`
  passed in the UI worker, 1 file / 23 tests;
  `npm.cmd --prefix internal/entry/web/ui test -- --run` passed, 6 files / 65
  tests;
  `npm.cmd --prefix internal/entry/web/ui run build` passed and produced
  `/assets/index-Bs5-fN-Z.js`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./... -count=1`
  passed;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` passed after prepending the project-local Go
  1.25.5 bin directory to PATH, rebuilding Web UI and Go executable, stopping
  PID `44864`, and starting PID `47552`;
  `GET /`, `GET /api/runtime`, `GET /api/projects`, and the active project
  snapshot returned HTTP 200. Project `untitled-novel-20260704010240-cccef4`
  restored as READY in writing phase with 53/59 chapters completed and recovery
  at chapter 54.

  Previous validation:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web -run "TestProjectAdaptCoCreate(CommitRepairsPreviousRoundPlaceholderDraft|RollsBackRejectedDraftWhenRepairFails|CommitRepairsRestoredRegressedDraft|RepairsRegressedDraftBeforeProposal|RepairsStaleDraftBeforeProposal)$" -count=1 -v`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web -count=1`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./...` passed;
  `npm.cmd --prefix internal/entry/web/ui test -- src/workspace-progress.test.js --run`
  passed;
  `npm.cmd --prefix internal/entry/web/ui test -- --run` passed, 6 files / 64
  tests;
  `npm.cmd --prefix internal/entry/web/ui run build` passed;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` initially failed because `go` was not on PATH,
  then passed after prepending the project-local Go 1.25.5 bin directory,
  rebuilding Web UI and Go executable, stopping PID `5276`, and starting PID
  `37084`;
  `GET /`, `GET /api/runtime`, and `GET /api/projects` returned HTTP 200.

  Earlier validation:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -run "TestBuildAdaptationProposal(FreeDefaultsLongSourceToChunkedPlanner|CoversSparseSourceAnchorsByExplicitRange|ClearsChunkedRuntimeAfterFinalValidationFailure|FreeUsesChunkedPlannerForLongTargetChapters|ResumesChunkedPlannerRuntimeAfterBatchFailure)$" -count=1 -v`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`
  passed;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web -run TestProjectSessionBuildAdaptationProposal -count=1`
  passed; `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt and restarted Web twice during live
  validation, most recently stopping PID `34544` and starting PID `41644`;
  `GET /api/runtime` and `/api/projects` returned HTTP 200.
  Live retry for project `untitled-novel-20260703122424-2e6c6b` generated an
  18-chapter free/full_rewrite proposal covering all 17 source chapters,
  confirmed it into `plan.json`, and started writing chapter 1
  (`RuntimeState=running`, `Phase=writing`, `InProgressChapter=1`).
  Focused wording validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./assets ./internal/tools -run 'TestLoadKeepsAdaptationGuidanceOutOfBaseWriterPrompt|TestContextToolInjectsAdaptationWriterGuidanceOnlyForAdaptation'`.
  Volume dedupe validation passed:
  `npm test -- src/workspace-progress.test.js`;
  `npm run build`;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI/Go executable, stopped PID
  `28960`, and restarted Web on `http://127.0.0.1:9898` as PID `11268`;
  Playwright verified the live project proposal still has 60 chapters / 5 stored
  volumes and the UI shows `全卷` plus exactly 5 volume options, with exactly 5
  proposal workspace volume blocks.

  Volume revision validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI and Go executable, stopped
  PID `16704`, and restarted Web as PID `31592`;
  snapshot structure check for project `untitled-novel-20260703215407-d41d36`
  confirmed the existing proposal is still status `proposal`, 60 chapters, 5
  volumes, last volume `47-60`.

  Required-expansion follow-up validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -run "TestReviseAdaptationProposal" -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI and Go executable, stopped
  PID `31592`, and restarted Web as PID `10368`;
  `GET /api/runtime` and `/api/projects` returned HTTP 200;
  current user project `untitled-novel-20260703215407-d41d36` still has saved
  proposal status `proposal`, 60 chapters, 5 volumes, last volume `47-60`.

  Model-decision expansion follow-up validation passed:
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -run "TestReviseAdaptationProposal" -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/host/adapt -count=1`;
  `D:\ainovel\.codex\tools\go1.25.5\go\bin\go.exe test ./internal/entry/web ./internal/host ./internal/host/adapt -count=1`;
  `git diff --check` passed with CRLF warnings only;
  `.\restart-web.cmd -Port 9898` rebuilt the Web UI and Go executable, stopped
  PID `10368`, and restarted Web as PID `5276`;
  `GET /api/runtime` and `/api/projects` returned HTTP 200;
  current user project `untitled-novel-20260703215407-d41d36` still has saved
  proposal status `proposal`, 60 chapters, 5 volumes, last volume `47-60`.

## Change Log

- 2026-07-11 `26f44d4` `fix: show all discovered provider models`: replace the
  filtered model `datalist` with a real select after discovery, show the number
  of supported models, rebuild embedded assets, restart port 9898, and verify
  the live Grok OAuth discovery returns 11 models.

- 2026-07-05 current local commit `test: stabilize legacy library backfill test`:
  captured the fake adaptation-analysis start channel in the legacy
  novel-library load regression before issuing the request, avoiding a mutable
  field read in the select path.

- 2026-07-05 current local commit `fix: stabilize adaptation analysis and co-create review`:
  added recursive batched foundation aggregation, configurable model/structure
  retries, normal co-create planning approval UI, stream/workbench isolation
  fixes, safe writer chapter inference, and refreshed Web static assets.

- 2026-07-05 pending `fix: allow Grok OAuth save without OpenAI endpoint`:
  added Grok OAuth provider-config normalization in bootstrap/Web/host save
  paths and focused regression tests; rebuilt/restarted local Web PID `28572`;
  left uncommitted to avoid staging mixed pre-existing worktree changes.

- 2026-07-05 current local commit `feat: add adaptation cocreate dossier`:
  source analysis now builds an all-book adaptation co-create dossier in
  resumable batches from source reports, Web/TUI readiness waits for it, adapt
  co-create consumes it instead of first-30 summaries, adapt co-create output
  cap is 8192, and truncated/incomplete XML drafts are rejected. Validation
  passed for Web UI tests/build, full `go test ./internal/...`, and
  `git diff --check`.

- 2026-07-05 current local commit `fix: remove text source upload size limit`:
  removed application-layer size caps from Web TXT/MD source uploads for
  仿写语料, 小说改编 source analysis, and external novel import while keeping JSON
  simulation profile uploads bounded. Added regressions proving both simulation
  and adaptation source uploads accept files larger than the former 10 MiB
  limit, rebuilt/restarted the local Web service on `http://127.0.0.1:9898`, and
  verified HTTP `/`, `/api/runtime`, and `/api/projects`.

- 2026-07-04 current local commit `style: tighten style prompt anti-ai guidance`:
  Added three prose-quality bullets to each `assets/styles/*.md` prompt directly
  under the style section and before the NSFW section. The bullets discourage
  numbered possibility lists, overused explanatory `——`, and model-like
  psychological inference checklists.

- 2026-07-04 current commit `fix: infer explicit cocreate word count`:
  Normal Web co-create now skips the target-word confirmation only when the
  initial idea contains an explicit total word count. Vague labels such as
  `长篇` still require user confirmation because `30万字` and `50万字` can both be
  long-form targets. Validation passed for all Web UI tests, Vite build, focused
  co-create backend tests, full `internal/entry/web` tests, and `git diff
  --check` with CRLF warnings only. The local Web service was rebuilt/restarted
  on port `9898` after prepending repo-local Go to PATH, and `/api/runtime` plus
  `/api/projects` returned HTTP 200.

- 2026-07-04 `a03cab2` `fix: normalize web export file extension`:
  Web export appends the selected `.txt`/`.epub` suffix when users enter a
  title-only filename, rejects `.txt`/`.epub` suffixes that conflict with the
  selected format, and keeps all paths scoped under the project `exports`
  directory. Validation passed for focused Web export tests, export renderer
  tests, full `internal/entry/web` tests, and the restarted local Web service
  on `http://127.0.0.1:9898` (PID `45780`).

- 2026-07-04 `ee471d2` `fix: split adaptation mode guidance`:
  splits adaptation prompting/runtime guidance by effective mode so
  `free/full_rewrite` no longer inherits chapter-only `preserve_details`
  wording. `novel_context` now exposes `working_memory.adaptation_effective_mode`
  with source-ref semantics, `plan_chapter` treats free source refs as optional
  background anchors, co-create openers no longer inject the mixed
  `rewrite_policy_rule=chapter=>...;arc/free=>...` line, and embedded Web assets
  were rebuilt after the UI source-label fix.

- 2026-07-04 `976a1fb` `fix: hide free adaptation source labels`:
  hides original-source mapping labels such as `原 17-17` and `原作范围` from
  free-mode proposal chapter cards/rows and volume review details while keeping
  source coverage data and preserving labels for chapter/arc modes.

- 2026-07-04 `1824d24` `fix: validate cocreate suggestions and dedupe volume review`:
  co-create suggestion chips no longer come from frontend prose/keyword
  guessing. Backend structured suggestions remain authoritative, and empty
  suggestion sets get one bounded JSON-mode model judge pass that returns either
  short user-voice suggestions or an empty list. Staged adaptation volume review
  now deduplicates mirrored summary/review volumes before rendering the volume
  selector, with frontend/backend regressions and regenerated embedded Web
  assets.

- 2026-07-04 `a9c0485` `fix: reject collapsed adaptation cocreate drafts`:
  adaptation co-create now rejects placeholder drafts such as `同前轮（完整保留）`
  or previous-round references before they can become the proposal brief. The
  session asks the model to repair incomplete drafts, rolls back to the previous
  stable draft if repair fails, keeps proposal generation blocked until the
  latest user turn is consolidated, and sends the current adaptation brief when
  the generic side-panel Adapt button starts co-create with an empty input.

- 2026-07-04 `99e66b4` `fix: let adaptation volume planner choose expansion`:
  selected-volume proposal revisions now ask the model to return
  `expansion_decision:"expand"` or `"keep"` during volume re-planning, and the
  backend validates that the decision matches the returned `target_to`.
  Volume/detail batch validation now runs before merge/save so missing
  `word_budget` and related chapter contract issues enter the repair loop
  instead of surfacing only as final validation failures. Failed revisions still
  do not overwrite the last saved proposal.

- 2026-07-04 `55c4162` `fix: enforce adaptation volume expansion revisions`:
  when a selected-volume proposal revision explicitly asks to add/supplement
  plot content, the volume skeleton must expand beyond the original `target_to`
  before detailed chapters are generated; unchanged counts trigger repair and
  then rejection. The revised volume metadata is persisted so the proposal
  workspace shows the new volume introduction after a successful re-plan.

- 2026-07-04 `44e0986` `fix: expand adaptation volume revisions`:
  proposal revision for a selected volume now runs in two stages: first re-plan
  the volume skeleton/plot range, then regenerate detailed chapter outlines for
  the revised volume. If the volume-level instruction asks to add/expand
  chapters, the selected volume can grow and all following chapter numbers and
  volume ranges shift by the added count. Fixed single-chapter/range revisions
  still reject count changes, and no-change revisions are rejected instead of
  being saved as successful.

- 2026-07-04 `2379137` `fix: dedupe adaptation proposal volumes in web ui`:
  front-end proposal review now deduplicates summary/proposal mirrored volumes
  by chapter range before rendering revision options and workspace sections;
  includes a Vitest regression and rebuilt embedded Web assets.
- 2026-07-04 `af1b10d` `fix: remove stage wording from adaptation guidance`:
  replace "舞台提示" with "提示性括注" and "舞台大小" with "故事规模"; confirmed no
  `舞蹈` / `跳舞` / `舞台` / word-boundary `dance` / `dancing` terms remain in
  tracked project text outside historical Git notes.
- 2026-07-03 current `HEAD` `fix: stabilize adaptation proposal coverage`: default
  long source-backed `arc`/`free` proposals into chunked planning when no
  explicit target chapter count is present, count explicit `source_range`
  coverage and expand sparse `source_chapters` before saving, clear invalid
  chunked proposal runtime checkpoints on final validation failure, add
  regression tests for the `does not cover source chapter` failure class,
  rebuild/restart Web on `http://127.0.0.1:9898` as PID `41644`, and verify the
  failed project now has a confirmed 18-chapter adaptation plan and active
  chapter-1 writing run.
- 2026-07-03 `331162c` `fix: restore web cocreate action controls`: consumes
  backend `can_start` in the React co-create state, disables launch until the
  draft is fresh/startable, parses paragraph-style AI choice questions into
  clickable suggestions, keeps long right-pane draft previews scrollable so
  action buttons remain visible, rebuilds embedded Web assets, restarts local
  Web on `http://127.0.0.1:9898` as PID `36560`, and verifies UI/backend paths.
- 2026-07-03 `pending` `docs: record latest restart check`: fetched
  `origin/main` with no new commits beyond the current branch, rebuilt the Web
  UI and Go executable, restarted local Web on `http://127.0.0.1:9898` as PID
  `15252`, and verified `/`, `/api/runtime`, and `/api/models`.
- 2026-07-03 `pending` `docs: record latest pull and restart`: fast-forwarded
  `main` from `e838341` to `21200be`, rebased the maintenance checkpoint after
  `origin/main` advanced to `861b527`, rebuilt the Web UI and Go executable,
  restarted local Web on `http://127.0.0.1:9898` as PID `26304`, verified `/`,
  `/api/runtime`, and `/api/models`, and recorded that restart commands in this
  shell need the project-local Go 1.25.5 bin directory prepended to `PATH`.
- 2026-07-03 `411c1df` `fix: retry adaptation planner calls`: wraps adaptation
  planner model calls in the shared 7-attempt retry policy with visible progress
  messages, keeps malformed-JSON/schema repairs bounded, fills missing
  `word_budget` data locally from legacy budget fields or source chapter anchors,
  adds regression tests for transient model failure and missing chapter budgets,
  restarts `http://127.0.0.1:9898`, and verifies a safe 20-chapter / 4-volume
  proposal plus single-chapter, range, and volume proposal revisions.
- 2026-07-03 `1ed3100` `fix: stabilize long-form adaptation proposal batches`:
  keeps model-chosen long-form skeleton/volume planning but splits oversized
  detail ranges into at most eight chapters per model call, fills partial
  missing chapter returns by sending accepted chapters plus the previous model
  output back to the model, limits repair attempts, adds detailed Web ADAPT
  progress/warn events for proposal generation and revision, recomputes totals
  after proposal revisions, restarts `http://127.0.0.1:9898`, and verifies with
  `go test ./...`.
- 2026-07-03 `389b0ab` `feat: add adaptation proposal review UI`: moves saved
  adaptation proposals into the central workbench, adds readable chapter cards,
  right-side single-chapter/range/whole-volume proposal revision controls,
  preserves saved proposals across refresh even when the upload source cannot be
  reconstructed, expands frontend/API tests, regenerates embedded Web assets,
  restarts `http://127.0.0.1:9898`, and verifies the UI with Playwright on a
  safe test project. The later two-stage volume-review workflow remains a
  follow-up checkpoint.
- 2026-07-03 `checkpoint` `checkpoint: persist adaptation proposal review foundation`:
  adds durable proposal volume metadata, saves model-planned long-form batches
  as proposal volumes, uses saved volumes when confirming adaptation plans into
  layered outlines, adds backend/session/API plumbing and visible events for
  proposal revision, starts front-end revision state/API wiring, and regenerates
  embedded Web assets. The later two-stage volume-review workflow remains a
  follow-up PR/checkpoint.
- 2026-07-03 `78c3fa6` `fix: chunk long-form adaptation planner proposals`:
  added target chapter-count inference for adaptation briefs, routed long
  `arc/free` proposals through a model-driven skeleton plus batch-detail
  planner flow, preserved strict proposal validation and no-activation proposal
  semantics, rejected skeletons that ignore long-form scale, covered 20+ and
  50-60 chapter regressions, restarted local Web, and verified the current
  restored co-create state without triggering unsafe generation.
- 2026-07-03 `5c0d9a4` `fix: preserve multi-turn adapt cocreate drafts`:
  tracks co-create draft freshness, repairs missing/stale/regressed drafts,
  keeps all user planning turns in repair context, runs a final consolidation
  before multi-turn adapt proposal generation, clears stale adapt co-create
  checkpoints, and rejects planner no-chapter output instead of silently saving
  a fallback proposal.
- 2026-07-03 `07eeaae` `fix: make adapt cocreate proposal start robust`:
  restored Web adapt co-create mode/source state across refresh/restart,
  changed adapt co-create commit to build a proposal instead of silently doing
  nothing, made planner parsing tolerant of common JSON shapes, added a
  deterministic proposal fallback for unusable planner output, synchronized the
  frontend proposal key after co-create commit, and regenerated embedded Web
  assets.
- 2026-07-02 `pending` `feat: add web model deletion`: added global and
  project-scoped model deletion, protects the active default route, removes
  deleted routes from role/fallback config, refreshes in-memory model routing,
  persists updated config, exposes Web delete controls, rebuilds embedded Web
  assets, restarts `http://127.0.0.1:9898`, and verifies backend/frontend/live
  browser behavior.
- 2026-07-02 `ff9d2c4` `docs: record latest update and restart`: fetched
  latest `origin/main` from `79703a9` to `bba6eea`, rebased the local
  maintenance commit, rebuilt and restarted the local Web service via
  `.\restart-web.cmd` with the project-local Go 1.25.5 toolchain on PATH,
  stopped old PID `89184`, started PID `236716`, and verified
  `http://127.0.0.1:9898` plus `/api/runtime`.
- 2026-07-02 `fef41a9` `docs: record latest pull and restart`: pulled latest
  `origin/main` with `git pull --ff-only`, fast-forwarded to `79703a9`, rebuilt
  and restarted the local Web service via `scripts\restart-web.ps1 -Port 9898`,
  verified `http://127.0.0.1:9898`, `/api/runtime`, `/api/projects`, and
  confirmed port 9904 had no listener.
- 2026-07-02 `a83b2af` `feat: surface adaptation planning state in UI`:
  completed the strict-agent adaptation proposal/UI pipeline after independent
  review and fix rounds. The sequence added additive planning contracts
  (`c772872`), real `arc/free` planner-backed proposals (`0e91abc`), explicit
  build/confirm/start flow (`91be2eb`), persisted hard/soft adaptation word
  budgets (`1cccc2f`), and snapshot/UI visibility for loaded profiles,
  creative blueprints, proposals, full outlines, budget/source coverage, and
  safe stale-proposal handling (`a83b2af`). UI assets were rebuilt.
- 2026-07-02 `074fd6b` `fix: stabilize web short story creation`: changed
  normal creation word-budget misses and repeated-draft detection from hard
  local errors into structured tool feedback, added draft-stage word-budget
  guidance, normalized one-entry outline chapter numbers, removed remaining
  Ctrl+S start copy from Web co-create responses, and verified a real 5000-word
  one-entry Web co-create completed without local ERROR.
- 2026-07-01 `af33ae7` `fix: add global web model providers`: added
  `POST /api/models/add`, enabled no-project `Add and use` for global provider
  registration, allowed logged-in Grok OAuth to become visible in the global
  default model selector after confirmation, added backend/frontend coverage,
  regenerated embedded Web assets, rebuilt the local binary, and restarted port 9898.
- 2026-07-01 `aba5224` `feat: add web default model control`: added global
  `/api/models` and `/api/models/default` endpoints, synchronized server/session
  config snapshots for newly opened projects, added the Web "瑜版挸澧犳妯款吇濡€崇€? selector,
  kept Grok callback actions usable outside global busy state, added tests,
  regenerated embedded Web assets, rebuilt the local binary, and restarted port 9898.
- 2026-07-01 `6a52385` `fix: allow global web grok login`: added global Web
  Grok OAuth start/poll/complete/status endpoints, routed frontend Grok login
  calls to them when no project is active, kept Login/Poll/Status clickable
  without an open project, added backend/frontend coverage, regenerated embedded
  Web assets, rebuilt the local binary, and restarted port 9898.
- 2026-07-01 `5c9ff38` `fix: open web grok oauth login`: added an opt-in
  backend system-browser opener for Web Grok OAuth login start, frontend pending
  and no-project feedback, popup/manual-link fallbacks, API/backend tests, and
  regenerated embedded Web static assets.
- 2026-07-01 `7bee635` `feat: add word budget progress UI`: added workspace progress
  display, normal co-create total-word controls, sticky right-side co-create
  launch/planning area, overflow/mobile layout fixes, total-vs-per-chapter
  user-rule wording, regenerated embedded Vite assets, rebuilt the local binary,
  and restarted only port 9898.
- 2026-07-01 `6549c2a` `Implement backend word budget contract`: added the
  persisted `WordBudget` contract, quick/co-create payload handling, startup
  prompt injection, `novel_context` runtime budget payload, outline-derived
  chapter budgets, and non-adaptation commit gating.
- 2026-07-01 `feat: improve web project and cocreate controls`: added Web
  project rename and internal-trash APIs, ChatGPT-style project context menu
  UI, active-session cleanup on trash, one-click equal-width co-create
  suggestion buttons, editable pre-commit co-create user messages with revise
  regeneration, backend/frontend tests, and regenerated embedded Vite assets.
- 2026-07-01 `9d40f9b` `feat: add web grok oauth login`: added Web Grok OAuth
  login start/poll/complete/status APIs, surfaced the flow in the model add
  form, registered final Grok providers via `auth:"grok_oauth"` and
  `account_id`, added backend/frontend tests, and regenerated embedded Vite
  assets.
- 2026-07-01 `2017558` `feat: close web tui operation gaps`: added Web APIs
  and UI controls for fresh quick start, pause/abort, external novel import,
  export, diagnostics, and generic model add; aligned Web adaptation/co-create
  behavior with TUI and regenerated embedded Vite assets.
- 2026-07-01 `6f38d2a` `style: refresh web workbench UI`: refreshed the React
  Web UI with a Windows 11 Fluent-inspired light workbench, compact right
  inspector tabs, fixed-height command bar, responsive narrow layout, and
  regenerated embedded Vite assets.
- 2026-07-01 `96a384b` `docs: finalize web packaging and smoke coverage`:
  added Web UI static embed and HTTP smoke coverage, CLI default parser
  regression, README Web runtime docs, `runtime_root` config example comments,
  and `/novel/` ignore protection.
- 2026-07-01 `860d487` `feat: add web model admin status pages`: added project
  model routing, usage/cache reporting, backend status/testing APIs, UI tabs,
  and project-overlay persistence fixes accepted by re-review.
- 2026-07-01 `c084600` `feat: add web co-create workflow`: added normal,
  stage, and adapt co-create flows with suggestion chips, stream state, and
  ready draft launch/restore behavior accepted by re-review.
- 2026-07-01 `66b4539` `feat: add web adaptation workflow`: added source upload,
  preparation analysis, constrained `chapter` / `arc` / `free` mode selection,
  rewrite policy mapping, and adaptation start UI/API accepted by re-review.
- 2026-07-01 `d315035` `feat: add web simulation profile flow`: added project-scoped
  simulate file upload, analysis trigger, JSON profile import, upload safety
  checks, UI panel, and embedded assets accepted by review.
- 2026-07-01 `7331eda` `feat: add web session workbench`: added project
  session management, snapshot/resume/continue/steer/events APIs, SSE history
  replay, embedded React/Vite workbench, and concurrency/error-propagation
  fixes accepted by re-review.
- 2026-07-01 `4c5d7e8` `feat: add web runtime entrypoint`: added
  `ainovel-cli web`, `internal/entry/web`, runtime-root and project manifest
  tests, and `Host.SimulateFromDir`; independent review passed and mechanical
  validation succeeded before commit.
- 2026-07-01 `202b21b` `docs: clarify adaptation mode selection`: pulled
  latest upstream code, installed project-local Go 1.25.5 under ignored
  `.codex/tools/`, rebuilt `D:\ainovel\ainovel-cli.exe`, and verified the
  PowerShell/cmd `ainovel-cli` entrypoints show commit `202b21b`.
- 2026-06-30 `fix: bound TUI frame height`: top-level TUI rendering
  now pads/truncates every view to the active terminal size, with a done-state
  regression test covering stale multi-line input height so completed/export
  prompts cannot keep scrolling as repeated old frames.
- 2026-06-30 `docs: record global CLI sync`: rebuild
  `D:\ainovel\ainovel-cli.exe` from `7bafcc6`, add `D:\ainovel` to the current
  user's PATH, install `D:\grok\bin\ainovel-cli.cmd` for already-opened PATH
  environments, and verify `ainovel-cli --version` from outside the repo in
  both PowerShell and cmd.
- 2026-06-30 `7bafcc6` `fix: scope adaptation writer guidance`: pulled latest
  upstream code from GitHub and rebuilt the local runtime executable.
- 2026-06-30 `1dd5d60` `fix: stabilize retries and TUI input rendering`: shared
  retry policy now uses 7 attempts with increasing delay for co-create,
  structured source analysis, user-rule normalization, and subagent calls. TUI
  co-create failures now show an actionable retry/start prompt, and global input
  rendering is clamped to one line so completed/export states do not flood the
  terminal with repeated placeholders.
- 2026-06-30 `928a008` `fix: choose adaptation mode before cocreate`: mode
  selection for novel adaptation now happens immediately after source
  preparation and before adaptation co-create. Validation passed for startup,
  TUI, headless, host/adapt/flow, CLI parsing, and local/global build sync;
  `internal/tools` still hit existing Windows TempDir cleanup failures only.
- 2026-06-30 `3bb376f` `feat: stabilize adaptation source preparation`: pulled
  latest upstream code from GitHub.
- 2026-06-30 `docs: record restart validation`: record successful local rebuild
  and restart after updating to the latest GitHub code.
- 2026-06-30 `docs: update git notes after latest pull`: refresh baseline
  after updating to the latest GitHub code.
- 2026-06-30 `68e1f33` `fix: auto-run simulate from palette`: pulled latest
  upstream code from GitHub.
- 2026-06-30 `docs: add project git maintenance notes`: record project GitHub
  push rule and baseline.

## Rollback Notes

- To inspect the latest upstream pull: `git log --oneline --decorate -5`.
- To inspect or revert PR-01 after commit, use `git show --stat <commit>` or
  `git revert <commit>`; do not touch `novel/`.
- To undo only the local notes commit after review, use `git revert <commit>`.
- Do not delete or stage `novel/` unless the user explicitly asks to track that
  local runtime/workspace directory.

