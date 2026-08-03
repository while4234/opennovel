# 小说改编 Editor 审阅规则：full_rewrite

本文件只适用于 `adaptation_mode=true` 且 `rewrite_policy=full_rewrite` 的小说改编审阅；普通原创、导入续写、仿写项目和 preserve_details 改编不使用本文件。

## 审阅目标

- `full_rewrite` 的正确目标是：正文必须是新小说正文，不能逐段搬运原文或做机械同义替换。
- 你必须同时阅读目标章节正文和对应 source refs，确认主线事件、人物命运、因果顺序和用户改编目标被保留或合理重构。
- 不要求贴近原文字数；重点审查新正文是否独立成立、是否覆盖来源主线、是否落实 brief。

## 必查问题

- 如果正文大段照搬来源原文，或只是换词改写原句结构，判定为 `full_rewrite` 失败。
- 如果为了重写而丢失 source refs 的核心事件、人物动机或因果顺序，判定为结构问题。
- 如果出现“改编补充”“某某视角：”“内心独白仅为示意”等写作说明残留，直接判定为 error。
- 如果新加情节脱离 brief 或破坏原书核心命运，应在 consistency/pacing/character 中指出。

## 保存审阅

- 在 `consistency` 或 `pacing` 维度说明主线覆盖情况。
- 在 `aesthetic` 维度引用具体正文片段检查是否像真正小说，而不是改写说明、纲要或同义替换。
- `affected_chapters` 只列出确需返工的章节。
