# 稿件真实项目克隆验收

## 安全协议

操作者必须明确填写一个 normal 和一个 adaptation 项目 ID。工具不会扫描后自动选择“第一个”。`validation_root` 必须是 source 之外的绝对独立目录。clone 使用 staging、拒绝 symlink/reparse/junction 和任何 hardlink 身份、受控 JSON rebasing 和原子 rename。复制规则是程序拥有 schema 的精确 allowlist：正式 output 工件、明确的 adaptation source/blob 和 export 工件；任意 `uploads`/`profiles`/`simulate` 用户文件默认拒绝，而不是依赖 credential 文件名黑名单。

复制前后对 source 的非敏感整树聚合 SHA-256；报告只保存匿名 ID、counts、bytes、hash、status，不保存标题、人物、正文、摘录或 source 绝对路径。默认准备报告后删除 clone；只有显式 `keep_clones` 才保留用于后续人工场景，完成后必须清理。

## 准备命令

复制 `scripts/e2e/manuscript-clone-validation.example.json` 到仓库外的私有位置，填写显式选择，然后运行：

```powershell
powershell -NoProfile -File .\scripts\e2e\manuscript-clone-validation.ps1 -Config C:\private\validation.json
```

这一步只证明 clone 隔离和 source checksum，不代表 15 项场景通过。生成报告会把全部场景标记为 pending，直到操作者在 clone 中逐项记录 precondition、action、expected、actual、signature、source before/after hash 和匿名 evidence。

## 15 项场景

writing 查看已完成正文、插章、新弧、新卷、单章提纲局部重写、正文打磨、分卷变更后的卷/全书审核、完本追加、candidate 失败不丢 current、重启继续 revision、normal source firewall、adaptation coverage/target-source identity/contract/prose audit、三类 resume 人工节点阻断、一句话扩写全流程、长上下文分批与预算。

任何 source hash 变化、预算超限、缺少签名、只写“手工通过”或未执行真实浏览器/模型步骤，都必须判失败而不是补写通过结论。
