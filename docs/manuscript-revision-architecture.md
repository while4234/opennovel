# 稿件修订生产架构

## 发布根信任部署

正式运行不会接受项目内容或普通环境变量提供的发布根，也不会在首次签名时静默创建新信任域。发布安装程序必须以管理员身份执行一次 `ainovel-cli authority init`：Linux/macOS 使用 `/var/lib/ainovel/publication-authority-v1`（service account 独占的可写运行状态）和 `/var/lib/ainovel/publication-authority-installation-v1`（root 拥有、父目录不可由 service account 删除或 rename 的 installation anchor）；Windows 使用 `C:\ProgramData\AINovel` 下同名两个目录，parent/root/installation 对象必须由 LocalSystem/Builtin Administrators 拥有并使用受保护 DACL，service account 仅在 root 子工件上继承修改权，不能删除或 re-ACL root/installation。运行时、`expansion-auditor` 和 `manuscript-completion-auditor` 只按这些固定路径发现状态；普通 service account 不能 bootstrap、删除或重建 anchor。pin 缺失、root 被整体替换或 epoch/generation/revocation 回退都会 fail closed。

容器为 UID/GID 65532 固定 `HOME=/home/ainovel`，普通 provider 配置从独立可写卷 `/home/ainovel/.ainovel/config.json` 发现，绝不写入 authority base。容器另使用持久卷 `/var/lib/ainovel`：先由 root bootstrap job 在同一 volume 创建管理员拥有的 installation anchor，再将仅 `publication-authority-v1` 交给 UID/GID 65532，随后才启动默认非 root runtime；卷丢失或被替换必须 fail closed，重建容器不得静默创建新 anchor。轮换、撤销、导入均属于管理员维护窗口，普通 runtime 只读取受控 pin/checkpoint 和使用可写运行状态。备份必须同时保存两个固定目录及其 owner/mode/ACL；恢复时先停止服务、恢复管理员 ownership/DACL，再运行 auditor。一般应用配置文件不包含 authority 备份，也不得删除 pin/checkpoint 强行重建。导出 bundle 仍使用 `ainovel-cli authority export <absolute-bundle> <32-byte-key-file>`，旧 bundle 不能恢复已撤销根。

## 权威数据

- current publication、stable structure identity、revision journal、签名审核工件是权威数据。
- candidate content、display number、content index、Web cache 都是投影。
- numeric chapter API 只解释当前显示顺序；adaptation source chapter number 永不参与 target 排序或重排。

## 恢复与锁序

```text
adaptation outer command
  -> normal revision publication
  -> manuscript revision publication
  -> structure migration
  -> batch/session reconciliation
  -> current read snapshot
  -> resume eligibility
```

实现中的总锁序为 `revision transaction -> migration -> artifact IO`。prepared/formal_applied journal 确定性回滚；completed journal 确定性完成 runtime CAS。恢复失败时 `ManuscriptRecoveryState` 只暴露 owner 分类，不暴露路径或正文；`RequireManuscriptWriteReady` 阻止所有正文与 expansion 写入。

## 生命周期

稿件修订与扩写写命令携带 revision/expected revision/idempotency key。相同 key 与相同 payload 重放同一结果；不同 payload 返回 `idempotency_conflict`。正文候选必须通过独立签名审核和人工 final approval 才能发布。完结作品接受新结构后，会在同一结构迁移/适配修订事务中保存 completion revalidation checkpoint；checkpoint 绑定 accepted revision/version、前后 stable order 和正式 structure signature。重启后保持人工节点。normal 项目由独立 `manuscript-completion-auditor` 进程重新读取当前每章 prose/summary、弧/卷摘要和全书结构，实际执行 chapter/arc/volume/book 规则并用进程私有 Ed25519 身份签署 receipt；产品进程只保存并验证 public trust，不能把旧 review 包装成新 receipt。审查进程缺失、失败或 receipt 未签名时 checkpoint 保持 pending。adaptation 项目必须实际重跑 checkpoint 之后的 completion semantic audit，并把该 run 的 input/report、当前 structure、当前每章 prose/summary 与 accepted revision/version 一起写入独立 receipt。任一审后漂移都会使 receipt 失效。两类项目只有通过 receipt 回读验证后才回到 complete。`ReopenedFromComplete` 仍保留为兼容投影，不是完成证据。

manuscript/expansion 写接口使用统一 error envelope，分类包括 `revision_conflict`、`preview_stale`、`idempotency_conflict`、`active_revision`、`human_confirmation_required`、`batch_failed` 和 `publication_recovery_required`；内部生成错误类别只放在 `details.error_class`，不替代稳定 API code。

## 恢复/回滚风险

不要删除 journal 来“解锁”。先保存只读诊断，再让 canonical recovery 重试。content index 可安全删除并重建；current、revision journal、content-addressed blobs 不可当缓存清理。旧项目迁移先 dry-run 取得 SHA，apply 必须提交同一 expected SHA；导入只写同卷 staging，安装前使用长度前缀 canonical 编码重新读取 staging 中每个允许文件的 normalized path/type/mode/size/content SHA 并核对 aggregate，再原子 rename。ProjectStore/Server 启动恢复 v2 journal、清理不完整 staging；若恢复失败，project discovery/open/create/clone/migrate/write 全部以稳定 `startup_recovery_required` fail closed。rollback 仅接受 v2 marker 和同一 SHA，旧 v1 不自动升级、安装或删除。
