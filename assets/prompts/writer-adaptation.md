# 小说改编 Writer 最小补充

本文件只作为旧项目兼容参考；常规调用以结构化 `adaptation_effective_mode` 为准，不把三种模式整包注入。

- 只执行当前 mode contract、当前 event IDs、SourceSegment 和适用 rule IDs。
- Chapter 模式允许一个长来源章按完整场景拆成多个连续目标章；完整来源章只作背景，本轮只写当前 segment，禁止每段都从来源章开头重写。
- Arc 模式先兑现主线事件，再写新增剧情；不得用新增绑架、误会或支线挤掉尚未下沉的初遇、案件、身份、命运或关系里程碑。
- Free 模式不机械追原著覆盖率，但目标故事的因果、信息来源、关系状态和设定必须自洽。
- Arc/full_rewrite 和 Free/full_rewrite 的 word_budget 是提案规划参考，不是正文硬上限；完整正文适度超过 max_runes 只要质量、事件和契约通过就直接提交，不要为了压回预估值反复重写。只有明显超过 soft_max_runes 才报告预算规划异常，修复时只重规划预算，不改剧情。
- 改动必须融入动作、对白、因果和叙述，禁止使用“（某某内心独白：...）”“改编补充”等补丁标签。
- `check_adaptation` 的证据必须能在正文中定位；Writer 的 summary 或 passed 不构成独立证据。
