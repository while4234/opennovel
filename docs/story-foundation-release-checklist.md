# StoryFoundation 发布检查清单

本清单是发布前的机械门禁；只有实际命令输出可作为通过证据。不要把“未运行”记录成通过。

## 验收矩阵

| 场景 | 主要自动化证据 |
|---|---|
| 普通 Character analyze/review/confirm、Foundation 前禁止规划 | `TestConfirmOriginalCharacterCandidatePublishesReviewedCandidateAndIsIdempotent`、Character Workspace HTTP/Playwright tests、`TestAuthoritativeOutlineGateBlocksDirectBypassesAndPreservesAdaptationSemantics` |
| Foundation 确认后分卷/详细大纲、局部重建与重审 | `TestFoundationRevisionAwaitsExistingOutlineApprovalBeforeCompletion`、`TestFoundationImpactAllowsStructuredLocalScope`、`TestFoundationDiffIsCanonicalAndStableIDBased` |
| 缺依赖、核心角色、hard rule 扩大全书 | `TestFoundationImpactRequiresEvidenceAndExpandsHardRules`、`TestFoundationRevisionRequiresCoreCastReconfirmationBeforeApply` |
| 改编无预置 CoreCast、来源角色处置、原创人物、SourceFoundation 只读 | `TestAdaptationCharacterWorkflowPublishesCompleteCastAndTargetFoundation`、`TestAdaptationFoundationPreviewApplyAndRetryPreserveSource`、Foundation HTTP source-mutation tests |
| target Foundation 后才生成 proposal；source-fidelity 重审 | `TestAdaptationFoundationReviewCompletionUsesSameSessionAndDoesNotStartBody`、adaptation Foundation preview/baseline tests |
| 发布后 host 失败 retry 不重复 revision | `TestFoundationRevisionApplyRetryDoesNotPublishTwice`、`TestFoundationRevisionRouteLaunchFailureRetriesSameSession` |
| stale 草稿、正文后只读 | Foundation Center Playwright stale/readonly 场景、`TestFoundationRevisionServiceTreatsPersistedDraftAsBodyStarted` |
| 旧 sparse card、旧普通/改编项目与旧 CoreCast checkpoint | `TestCharacterWorkspaceStateLazilyReportsLegacyCardCompletenessWithoutMutation`、`TestFoundationLoadsSchemaV1AndV2WithoutRevisionOrFileMutation`、legacy co-create/parser tests |
| Character stale、retry、reload、并发与幂等 | `TestCharacterWorkspaceServiceAnalyzeReviewAndRetry`、`TestCharacterWorkspaceStoreIdempotencyExclusionAndRecovery`、Character HTTP stale/reload tests |
| 稳定 ID workset、成熟项目上下文预算与模型路由 | `TestCharacterWorksetPrefersIDsAndStaysBounded`、`TestContextToolMaturePlanningContextIsSourceBounded`、`TestProductionModelCallInventoryIsExplicit`、Character model route/failover tests |
| pending journal、进程重启、active revision recovery | Foundation recovery failure-point tests、`TestFoundationRevisionStorePersistsAndDetectsTamperedPreview`、pending migration recovery tests |
| clone、rollback 与文件集合 | `TestCloneProjectCopiesCompletedStoryFoundationAndRejectsPendingJournal`、projection/clone concurrency tests、Foundation rollback lifecycle tests |
| 100/300 图、方向/筛选/一跳/布局/移动端/降级 | `relationshipGraphModel.test.js`、`FoundationGraphErrorBoundary.test.jsx`、Foundation Center Playwright graph 场景 |

## 发布命令

从仓库根目录执行：

```powershell
npm --prefix internal/entry/web/ui ci
npm --prefix internal/entry/web/ui test
npm --prefix internal/entry/web/ui run build
npm --prefix internal/entry/web/ui run test:browser
go test ./...
go build ./cmd/ainovel-cli
go test -race ./internal/domain ./internal/store ./internal/host ./internal/entry/web
go vet ./...
git diff --check
```

另外确认：

- `package.json` 与 `package-lock.json` 一致，`npm ci` 不改 lockfile。
- build 后 `internal/entry/web/static/index.html` 只引用当前存在的哈希资源，且 Go embed 测试通过。
- `THIRD_PARTY_LICENSES.md` 随 GoReleaser archive 和容器镜像分发。
- `node_modules`、`test-results`、trace、video、coverage、日志和本机绝对路径不在 diff。
- Foundation/normal/adaptation 定向测试可重复运行；任何跳过或环境失败保留原始输出与风险。
