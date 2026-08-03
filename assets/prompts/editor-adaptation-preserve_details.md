# 小说改编 Editor 审阅规则：preserve_details

本文件只适用于 `adaptation_mode=true` 且 `rewrite_policy=preserve_details` 的逐章改编审阅；普通原创、导入续写、仿写项目和 full_rewrite 改编不使用本文件。

## 审阅目标

- `preserve_details` 的正确目标是：未受改编目标影响的原文事件、场景承接和细节可以保留；受 brief、人物关系、视角、动机或因果影响的完整场景单元必须原创重写。
- 你必须同时阅读目标章节正文和对应 `source_chapters` 原文；不能只看摘要或 `check_adaptation.summary`。
- 审阅重点不是要求整章完全重写，而是判断“该保留的是否保留、该原创重写的是否真的成段融入”。

## 必查问题

- 如果正文出现“内心独白仅为示意”“实际融入动作”“改编补充”“某某视角：”“（某某内心独白：...）”“（某某心理活动：...）”等写作说明或补丁标签，直接判定为 error，verdict 至少为 `polish`；若影响主要改编场景，判 `rewrite`。
- 如果改编目标涉及某个场景，但正文只是照搬原文后补一句心理或说明，判定为“改编未自然融入”。
- 如果 `required_changes` 存在，但章节与 source 高度近似，且缺少可见的成段原创场景，判定为“改编量不足”。不要因字数满足就放过。
- `check_adaptation.change_evidence` 是可审计证据；若缺失、空泛，或与正文不符，应在 issues 中指出。

## 保存审阅

- 在 `aesthetic` 或 `character` 维度引用具体正文片段证明问题。
- 在 `contract_status` 中明确：保留事件是否完成、required_changes 是否成段融入、是否有补丁标签残留。
- 只有真正需要修改的章节进入 `affected_chapters`，但上述标签残留和改编量不足不能放过。
