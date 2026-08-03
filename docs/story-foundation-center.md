# 设定中心与 StoryFoundation

设定中心管理“准备写入目标故事”的规范事实。`StoryFoundation` 是前提、角色、计划关系和世界规则的唯一目标故事真相；旧的 `premise.md`、`characters.*`、`planned_relationships.*` 和 `world_rules.*` 只是同一次事务生成的兼容投影。

## 普通原创阶段

```text
共创需求
  → Character Agent 生成角色候选
  → Character Agent 独立审查
  → 用户显式确认角色卡
  → Architect 生成 premise / world rules / outline
  → 用户确认 Foundation
  → 分卷骨架 → 分卷/故事弧审查 → 详细大纲审查
  → 用户确认大纲
  → 正文
```

角色卡与 Foundation 未确认前，服务端门禁会拒绝任何分卷或章节大纲正式写入。Character Agent 是角色与计划关系的唯一写入者；Architect 只能读取已确认角色卡。确认角色候选时会同步生成兼容 `CoreCastContract`，供旧项目和既有门禁继续工作；新共创不再要求或生成 `<cast>`。

## 改编阶段

```text
来源分析
  → SourceFoundation（永久只读）
  → Character Agent 基于有界来源证据完成保留/合并/排除/原创映射
  → 独立审查 + 用户显式确认角色卡
  → target StoryFoundation 生成/审查/确认
  → skeleton/proposal → source-fidelity 与 target-consistency 审查
  → AdaptationPlan 与大纲确认
  → 正文
```

`SourceFoundation` 是来源证据，不是目标设定编辑器。设定中心只能修改 target `StoryFoundation`；任何携带 `source_foundation`、`source`、`mode` 或客户端 impact 的写请求都会被拒绝。Character Agent 只读取按章节切片、摘要与签名组成的有界证据包，不回填或改写 SourceFoundation。改编的 target Foundation 未确认前，不能生成 skeleton/proposal。

从小说仓库加载来源时，服务端会验证 SourceFoundation 的版本、来源签名、报告签名、prompt 版本、批处理配置和章节覆盖。缺少版本化绑定的旧资料包会自动进入增量升级：仍然有效的逐章报告按内容签名复用，只重新生成过期的 Foundation、dossier 或其批次，并在成功后同步回小说仓库。原文分析区在已完成状态下提供“增量重新分析并升级”，用于主动复查当前来源包；它不会无条件重复调用模型。

共创区的“重新生成共创简报”只根据当前改编方向重建 briefing，不重新分析原文章节或 SourceFoundation。该操作要求已有改编共创会话、输入了新的整理要求、当前没有运行任务，并且所有前置决策都已处理；按钮悬停提示会说明具体禁用原因。

## 范围和关系语义

- premise：目标故事的总前提。
- characters：用稳定 ID 标识的目标角色及其动机、冲突、声音、tier、阵营等。
- planned relationships：写作前计划，边 ID、source、target 都引用稳定角色 ID；方向是 `directed`、`bidirectional` 或 `undirected`。
- world rules：目标世界硬规则和软规则；修改 hard rule 会保守扩大重建范围。

计划关系不是正文推进中记录的 `runtime relationship_state`。关系图谱和关系列表都只读写 `StoryFoundation.relationships`，不会读取或写入 runtime API。

## 修改、preview、apply 和 retry

1. 所有表单、图谱 connect 和删除边都只修改浏览器中的统一 Foundation draft，并进入 dirty。
2. “预览差异与影响”把完整 candidate、预期 revision 和预期 audit signature 发给服务端。
3. 服务端规范化差异、验证依赖证据、计算影响范围并持久化签名 preview。
4. “应用”只发送 `preview_id + idempotency_key`。客户端不能提供 impact，也没有直接正式写接口。
5. 发布后如果 host/规划启动失败，“重试”从持久化安全边界继续同一个 RevisionSession，不重复增加 Foundation revision。

当 base 已变化时，页面进入 stale 并保留用户草稿。用户先加载最新基线，再显式重新 preview。局部重建只接受当前、签名一致的 dependency manifest；证据缺失、矛盾、过期、删除核心角色或修改 hard rule 时扩大到全书。

正文文件或持久化 draft 已出现后，Foundation 进入只读；图谱不可用也不会解除该门禁。

## 关系图谱

图谱使用 `@xyflow/react`，依赖按 lockfile 安装并依据 MIT 许可证分发，完整声明见 [第三方许可证](../THIRD_PARTY_LICENSES.md)。

- node ID = `Character.ID`，edge ID = `CharacterRelationship.ID`。
- 边标签同时显示类型、方向和状态；虚线等形状与文字保证信息不只依赖颜色。
- 支持角色搜索、importance/tier、阵营、关系类型/状态、一跳聚焦、孤立角色开关、图例、fitView、Controls、Background，以及桌面 MiniMap。
- 超过 80 个角色时默认只显示最高重要级别；筛选或显式“显示全部”可展开。映射、筛选、布局和 React Flow 数据都被 memoize。
- 移动端默认关系列表，可显式打开图谱。加载或渲染失败时错误边界回退到列表，draft 不丢失。
- 缺失端点的关系不会生成伪节点，而是被过滤并给出数据警告。

布局不属于 Foundation 内容。它只保存在浏览器 localStorage，key namespace 为
`foundation-graph-layout:<project-id>:<audit-signature-digest>`，payload 只含布局版本、相同不可逆 namespace、保存时间和 `{node-id: {x,y}}` 坐标。不会保存 premise、角色姓名/人设、关系内容、世界规则、来源设定或任何 token/cookie/session/credential；布局变化不触发 dirty、不会使 preview stale，也不会改变 audit signature。项目或 audit signature 变化后不会复用旧布局。

## 故障排查

- “当前状态不允许编辑”：检查是否已有 active revision、正文/draft，或项目已完成。
- `foundation_stale`：保留草稿，加载新基线后重新 preview。
- `foundation_source_stale`：改编来源证据变化；停止 retry，重新完成来源/目标审查。
- `foundation_recovery_failed`：不要重复创建新修订；修复持久化 recovery 问题后使用 retry。
- 图谱空白但列表有数据：检查筛选、一跳聚焦和“显示孤立角色”；图谱错误时直接使用列表，不影响 preview/apply。
- “缺少端点”：先修复关系的稳定角色 ID，再 preview。
