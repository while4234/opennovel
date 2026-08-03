# Prompt A/B 验证

创作提示词不能只靠读起来是否合理来判断。每次改 Writer / Architect 这类会影响正文或大纲质量的提示词，都先跑 A/B，再决定是否合入生产 prompt。

## 原则

- 同一用户需求、同一配置、同一模型、同一风格、隔离输出目录。
- Baseline 使用当前仓库内置提示词；Variant 只替换本次要验证的 prompt 文件。
- 脚本构建时固定 `GOWORK=off`，避免本地 `go.work` 把实验变体指向另一份源码；结果以 `go.mod` 依赖为准。
- 脚本不自动裁判质量，只负责可重复地对跑并暴露失败。质量裁定看实际产物。
- 失败要显式暴露，不写 mock 成功、不吞错误、不用 silent fallback。

## 运行

准备一个用户需求文件：

```bash
mkdir -p workspace/prompt-ab/cases
cat > workspace/prompt-ab/cases/xianxia.txt <<'EOF'
写一本修仙长篇，主角从边城杂役开始，核心看点是靠异常记忆能力破解宗门旧案并卷入长生局。
EOF
```

准备 Variant prompt 目录。目录里只放要替换的文件，文件名必须和 `assets/prompts/` 下的文件一致：

```bash
mkdir -p workspace/prompt-ab/variants/writer-quality
cp assets/prompts/writer.md workspace/prompt-ab/variants/writer-quality/writer.md
# 修改 workspace/prompt-ab/variants/writer-quality/writer.md
```

执行：

```bash
scripts/prompt_ab.sh \
  --config ~/.ainovel/config.json \
  --prompt-file workspace/prompt-ab/cases/xianxia.txt \
  --variant-prompts workspace/prompt-ab/variants/writer-quality \
  --max-chapters 1
```

脚本会生成两份隔离输出：

- `baseline/output/novel`
- `variant/output/novel`
- `report.md`
- `report.json`

`--max-chapters N` 只用于实验：脚本等 `chapters/NN.md` 落盘后停止该 headless 进程，方便快速比较前 N 章产物。不传该参数时会按正常 headless 流程跑到全书结束或显式错误。

## 报告

`report.md` / `report.json` 只汇总确定性运行事实：

- 完成章节数、总字数、每章字符数
- draft / commit / review 事件数
- 重复指令告警数
- 错误数
- tool call 总数与按 agent/tool 拆分的调用次数
- token 输入/输出与成本
- progress 的 phase / flow / completed_count

报告会给数值型指标显示 Variant 相对 Baseline 的 delta，但不做质量裁判，也不输出“胜者”。创作质量仍必须人工读正文判断，尤其看：

- 正文是否更好看，而不是只更短或更长
- 是否完成章节契约
- 是否少了前情复述、机械打卡和 AI 味
- 是否保持角色、伏笔、时间线连续
- 是否减少无效工具循环

## 边界

这个工具只负责实验编排和事实汇总：

- 不从用户自然语言 prompt 里猜长期规则。
- 不把临时测试 provider 写入仓库或文档。
- 不做自动评分，不用模板产物模拟成功。
- 不把某次 A/B 的结论固化成隐藏 fallback；通过的改动要回到正式 prompt 或代码配置里。

## Writer 第一轮验证

第一轮只测 Writer，不同时改 Architect / Editor / Coordinator。

推荐 Variant：

- 降低写作标准清单密度，合并重复规则。
- 保留 `plan -> draft -> check -> commit` 的硬流程。
- 把“check_consistency 通过后禁止润色”改成质量模式约束：默认不额外润色；质量实验允许最多一次整章级润色，且必须仍走 `check_consistency -> commit_chapter`。

对比重点：

- 章节是否完整覆盖 `required_beats`。
- 是否保持上一章衔接、角色状态、时间线和伏笔连续。
- 正文是否减少机械打卡感、前情复述和 AI 味句式。
- 对话是否有角色差异和行动目的。
- 章末是否自然形成追读欲，而不是硬造悬念。
- 工具链是否稳定，没有额外空转、重复规划或越过 `max turns`。

## Architect 后续验证

Architect 的模板感来自 premise 的固定标题与解析耦合，不是单纯措辞问题。优化时优先验证：

- parser 是否能容忍标题别名或缺省项。
- prompt 是否能减少强制标题数量，同时仍让 `premise_structure` 返回可用结构。
- 大纲是否保留可持续展开能力，而不是只变得文艺。

这类改动涉及解析和下游上下文注入，应单独做，不和 Writer 质量实验混在一起。
