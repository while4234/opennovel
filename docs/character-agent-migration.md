# Character Agent 流程与迁移

Character Agent 是普通原创和小说改编中角色卡、计划关系与来源角色映射的唯一内容写入者。Architect 只消费用户已确认的规范角色卡，不能新增、删除、改名或重塑角色，也不能通过 `save_foundation(type=characters|planned_relationships)` 绕过角色审查。

## 新流程

```text
共创四段协议（requirements / constraints / decisions / confirmation）
  → Character analyze：读取有界上下文，写一个候选
  → Character review：独立 run，写一个审查结论
  → 用户确认：CAS + 签名 + idempotency key
  → 原子发布 StoryFoundation characters/relationships
  → 同步投影兼容 CoreCastContract
  → Architect 生成 premise / world rules / outline
```

改编模式额外读取不可变的 SourceFoundation、来源摘要、主要角色清单和来源签名。每个主要来源角色必须有 keep / merge / exclude 等显式处置；目标原创角色必须标记为 original addition。候选或来源签名变化会让审查与确认变为 stale，必须重新审查。

## 旧/新数据迁移矩阵

迁移采用“读取时解释、确认时发布”，不会因为打开页面或读取状态而改写正式文件。

| 项目状态 | 读取/恢复行为 | 下一次受控写入 |
|---|---|---|
| 新普通项目，无角色 | 显示空 workspace，可启动 analyze | 完成 analyze → review → confirm 后发布 |
| 新改编项目，无 CoreCast | 从 adaptation gate、SourceFoundation 与 intent 推断改编模式 | 角色确认时绑定 source/intent/draft 签名并发布 |
| 只有旧 `characters.*` / Foundation 角色 | 原样可读；惰性计算完整度，不提升 revision | 缺失的富角色字段标记待补全、待审查 |
| 旧五段共创 checkpoint 含 `<cast>` | 严格兼容解析并可恢复旧 CoreCast 编辑状态 | 新一轮模型回复/repair 只使用四段协议，不再要求 `<cast>` |
| 已确认旧 CoreCast | 继续作为兼容 seed 与门禁证据 | Character 候选确认后由新投影替换 |
| 已有 outline、尚未写正文 | 现有工件可读取；角色/Foundation 变更仍受 revision gate 管理 | 新规划写入前完成必要的角色与 Foundation 审查 |
| 已有章节、快照或 runtime 关系 | 正文、快照与 runtime 状态保持可读且不被静态角色迁移覆盖 | 静态角色变更走 revision/impact 流程，不能直接污染动态状态 |
| interrupted Character run | 启动时恢复为 interrupted，保留 run/candidate/error 证据 | 使用 retry 新幂等键继续；不重复发布 revision |
| stale candidate / review | 保留用户候选并显示 stale 原因 | 基于最新签名重新 analyze/review/confirm |

## 上下文、性能与安全边界

- `character_context` 只返回当前任务所需的角色、关系、来源证据与签名；Character Agent 每个 run 必须先读取一次，并且只能调用一个匹配模式的 save 工具。
- Writer 使用稳定角色 ID 选择当前章节 workset；完整卡、压缩卡、快照和冲突诊断共同受 16 KiB workset 预算约束。成熟项目 planning source context 受 42 KiB 上限约束，不重复发送 `skeleton_arcs`。
- workspace 候选限制为 512 KiB，用户指令限制为 4 KiB。错误信息会清理 provider token 等敏感片段；日志、模型用量 ledger 与浏览器布局不保存 prompt、正文或凭证。
- SourceFoundation 永久只读。Character 候选、review、confirmation、CoreCast 投影和 Foundation 发布都使用 revision/signature/CAS/idempotency 保护，过期输入 fail closed。

## 模型配置

`roles.character` 是分析与审查的共同回退。`roles["stage:character_analysis"]` 和 `roles["stage:character_review"]` 可以分别覆盖 provider、model、fallbacks 与 reasoning_effort；stage 未配置时依次回退到 `character` 和全局默认。全局 prompt 与全局规则会注入 Character Agent，模式专用 task 仍保持最窄工具权限。

## 发布核对

自动化必须覆盖无预置 CoreCast 的普通/改编角色链路、legacy checkpoint 解析、旧 sparse card 惰性完整度、stale/retry/reload、SourceFoundation 不变、稳定 ID workset、上下文预算和模型 failover。外部真实模型 smoke test 必须由发布者显式选择 provider/model 并记录结果；没有凭证或未执行时必须标为“未验证”，不能写成通过。

本迁移为仓库内独立实现，没有复制 InkOS 源码或资产；现有第三方许可证集合未因本迁移新增依赖。
