# PR-05 pending migration 正式写入自死锁：已解决

## 结论

PR-05 最后一个已知 Major 问题已修复。pending structure migration 与后续正式写入现在共享同一个最外层 revision transaction，不会再次获取同一项目的非重入 `sync.Mutex`。

修复后的锁顺序统一为：

```text
revision transaction -> structure migration -> artifact IO
```

活动修订、prepared command 或 prepared publication 存在时，非法 legacy 写入会在迁移日志或正式稿件发生任何变化前被拒绝。

## 原根因

旧实现存在两类重复进入：

```text
ProgressStore.withWriteLock
  -> structureMigration.withRead
  -> pending migration fenced recovery
  -> RevisionStore.withLegacyMutation
  -> ProgressStore.withLegacyFormalMutation
  -> RevisionStore.withLegacyMutation（第二次进入）
```

以及：

```text
AdaptationStore.ResetGenerated / SaveCheck / DeleteCheck
  -> RevisionStore.withLegacyMutation
  -> structureMigration.withIndexRead
  -> pending migration fenced recovery
  -> RevisionStore.withLegacyMutation（第二次进入）
```

第二次进入在进程内非重入互斥锁处永久等待。`ResetGenerated` 还可能在进入嵌套恢复前先删除 plan、proposal、workflow 和 check 文件。

## 修复设计

- `RevisionStore.withLegacyMigrationMutation` 成为迁移感知正式写入的唯一外层事务入口。
- 它先读取并校验 revision state；活动修订或 owner fence 不允许 legacy 写入时立即拒绝。
- 所有权检查通过后，通过私有 `recoverWithinRevisionTransaction` 恢复 pending migration。该 helper 只获取 migration lock，不再获取 revision transaction。
- 迁移恢复成功后，在同一 revision transaction 内执行正式写入。
- Progress 写路径不再以 migration read 包住 legacy transaction；配置正式事务时直接使用上述外层入口。
- Outline、Adaptation、Progress 及结构感知的章节提纲修订、续写提交、提纲修复、返工进度、回滚、扩弧和追加卷路径统一接入迁移感知事务。
- 续写提交不再在持有多个 IO 锁后恢复 migration，消除了反向的 `revision -> IO -> migration` 顺序。
- `ResetGenerated` 在任何删除动作前完成迁移恢复；瞬时恢复失败时，生成态文件逐字节保持不变。

## 动态回归覆盖

`internal/store/pending_migration_write_test.go` 使用 failpoint 在同一个 live Store 留下 migration journal，并为所有调用设置超时保护，覆盖：

- Progress 正式写入：恢复一次、mutation 一次，第二次写入不重复恢复；
- 跨 Store 实例恢复与写入；
- `SaveCheck`、`DeleteCheck`；
- `ResetGenerated` 恢复失败前不删除，以及恢复后安全重试；
- 活动修订、prepared command、prepared publication 下逐字节不变拒绝；
- migration journal 完成后被清理。

聚焦测试连续运行 5 次通过。

## 真实项目克隆验证

验证仅在临时克隆上执行，原项目只读：

- 普通共创克隆：33 个已完成章节；pending migration 后 Progress 正式写入恢复成功。
- 小说改编克隆：34 个已完成章节、7,172 个文件；pending migration 后改编审校写入恢复成功，改编计划仍完整。
- 测试前后两个原项目的整树 SHA-256 完全一致。
- 临时克隆在验证后已安全删除，未进入 Git。

## 最终门禁

- `go test ./internal/store -run 'TestPendingMigration' -count=5 -timeout 5m`：通过。
- `go test ./internal/store -count=1 -timeout 15m`：通过。
- `go test -p 1 ./... -count=1 -timeout 30m`：通过。
- `go vet -p 1 ./...`：通过。
- UI Vitest：22 个文件、213 个测试通过。
- UI production build：通过，无非预期静态产物差异。
- changed-file gofmt 与 `git diff --check`：通过。

## 后续边界

本修复只关闭 PR-05 的最后锁与恢复问题，没有开始 PR-06。PR-06～PR-09 的重规划提示见 `docs/pr06-pr09-gpt56pro-replanning-prompt.md`。
