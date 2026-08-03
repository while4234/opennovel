# 全阶段模型质量 Benchmark

- 套件：`stage-quality-v1`
- 裁判：`codex-login/gpt-5.6-sol@xhigh`
- 评分：硬指标 30% + 匿名盲评 70%

阶段输出预算与生产路径对齐：共创 4096 tokens、资料分析 6144 tokens、正文 7000 tokens；其他阶段使用各自测试契约中的固定预算。正文最低 2600 字是硬门槛，低于下限时综合分封顶在不及格区；3200 字是软目标，超过上限不直接扣分，冗长与灌水由内容盲评扣分。模型若在预算内结束但未满足篇幅或协议，仍按实际结果评分。

| 排名 | 候选 | 总分 | 非创作 | 正文 | Writer 工具编排* | 阶段胜场 |
|---:|---|---:|---:|---:|---:|---:|
| 1 | `grok-oauth/grok-4.5@xhigh` | 90.9 | 91.0 | 90.4 | 96.0 | 2 |
| 2 | `grok-oauth/grok-4.5@high` | 90.7 | 93.0 | 74.8 | 94.9 | 6 |
| 3 | `deepseek-infa/deepseek-v4-pro` | 75.8 | 80.2 | 45.2 | 93.2 | 0 |

\* Writer 工具编排是额外诊断，不计入 8 阶段主榜。

## 分阶段结果

| 阶段 | 候选 | 硬指标 | 盲评 | 最终分 | 标准差 |
|---|---|---:|---:|---:|---:|
| co_create | `grok-oauth/grok-4.5@xhigh` | 100.0 | 93.0 | 95.1 | 2.8 |
| source_analysis | `grok-oauth/grok-4.5@xhigh` | 82.9 | 91.0 | 88.6 | 0.8 |
| skeleton | `grok-oauth/grok-4.5@xhigh` | 100.0 | 86.0 | 90.2 | 8.0 |
| detail_outline | `grok-oauth/grok-4.5@xhigh` | 100.0 | 86.0 | 90.2 | 1.9 |
| writing | `grok-oauth/grok-4.5@xhigh` | 100.0 | 86.3 | 90.4 | 1.1 |
| review | `grok-oauth/grok-4.5@xhigh` | 82.5 | 98.3 | 93.6 | 0.4 |
| character_analysis | `grok-oauth/grok-4.5@xhigh` | 100.0 | 95.0 | 96.5 | 0.7 |
| character_review | `grok-oauth/grok-4.5@xhigh` | 100.0 | 75.3 | 82.7 | 14.5 |
| co_create | `grok-oauth/grok-4.5@high` | 100.0 | 94.7 | 96.3 | 1.5 |
| source_analysis | `grok-oauth/grok-4.5@high` | 82.9 | 92.7 | 89.8 | 2.9 |
| skeleton | `grok-oauth/grok-4.5@high` | 100.0 | 86.7 | 90.7 | 5.4 |
| detail_outline | `grok-oauth/grok-4.5@high` | 100.0 | 89.0 | 92.3 | 1.2 |
| writing | `grok-oauth/grok-4.5@high` | 95.0 | 80.7 | 74.8 | 23.5 |
| review | `grok-oauth/grok-4.5@high` | 82.5 | 98.7 | 93.9 | 0.4 |
| character_analysis | `grok-oauth/grok-4.5@high` | 100.0 | 94.3 | 96.0 | 1.1 |
| character_review | `grok-oauth/grok-4.5@high` | 100.0 | 88.7 | 92.1 | 5.3 |
| co_create | `deepseek-infa/deepseek-v4-pro` | 100.0 | 78.7 | 85.1 | 3.3 |
| source_analysis | `deepseek-infa/deepseek-v4-pro` | 82.0 | 90.0 | 87.6 | 3.5 |
| skeleton | `deepseek-infa/deepseek-v4-pro` | 100.0 | 62.7 | 73.9 | 11.7 |
| detail_outline | `deepseek-infa/deepseek-v4-pro` | 100.0 | 60.0 | 72.0 | 5.5 |
| writing | `deepseek-infa/deepseek-v4-pro` | 85.0 | 54.0 | 45.2 | 25.5 |
| review | `deepseek-infa/deepseek-v4-pro` | 82.5 | 93.3 | 90.1 | 2.8 |
| character_analysis | `deepseek-infa/deepseek-v4-pro` | 100.0 | 75.3 | 82.7 | 4.6 |
| character_review | `deepseek-infa/deepseek-v4-pro` | 100.0 | 57.3 | 70.1 | 14.0 |

## Writer 确定性检查工具编排诊断

| 候选 | 诊断题 | 硬指标 | 盲评 | 最终分 |
|---|---|---:|---:|---:|
| `grok-oauth/grok-4.5@xhigh` | writer_tooling__consistency_exact_evidence | 96.1 | 92.0 | 93.2 |
| `grok-oauth/grok-4.5@xhigh` | writer_tooling__consistency_missing_scene | 90.0 | 100.0 | 97.0 |
| `grok-oauth/grok-4.5@xhigh` | writer_tooling__de_ai_batch_repair | 100.0 | 97.0 | 97.9 |
| `grok-oauth/grok-4.5@high` | writer_tooling__consistency_exact_evidence | 96.1 | 92.0 | 93.2 |
| `grok-oauth/grok-4.5@high` | writer_tooling__consistency_missing_scene | 90.0 | 100.0 | 97.0 |
| `grok-oauth/grok-4.5@high` | writer_tooling__de_ai_batch_repair | 100.0 | 92.0 | 94.4 |
| `deepseek-infa/deepseek-v4-pro` | writer_tooling__consistency_exact_evidence | 100.0 | 92.0 | 94.4 |
| `deepseek-infa/deepseek-v4-pro` | writer_tooling__consistency_missing_scene | 90.0 | 91.0 | 90.7 |
| `deepseek-infa/deepseek-v4-pro` | writer_tooling__de_ai_batch_repair | 100.0 | 92.0 | 94.4 |

## 配对比较

| 左候选 | 右候选 | 左减右逐题均差 | 95% bootstrap 区间 | 判定 |
|---|---|---:|---:|---|
| `deepseek-infa/deepseek-v4-pro` | `grok-oauth/grok-4.5@high` | -14.9 | [-22.1, -8.0] | 优势明确 |
| `deepseek-infa/deepseek-v4-pro` | `grok-oauth/grok-4.5@xhigh` | -15.1 | [-22.2, -9.0] | 优势明确 |
| `grok-oauth/grok-4.5@high` | `grok-oauth/grok-4.5@xhigh` | -0.2 | [-4.6, 3.1] | 优势较弱 |

## 结论

grok-oauth/grok-4.5@xhigh 排名第一（至少一组优势较弱）。grok-oauth/grok-4.5@xhigh：总分 90.9、非创作 91.0、正文 90.4、Writer 工具 96.0；grok-oauth/grok-4.5@high：总分 90.7、非创作 93.0、正文 74.8、Writer 工具 94.9；deepseek-infa/deepseek-v4-pro：总分 75.8、非创作 80.2、正文 45.2、Writer 工具 93.2。

阶段路由、长正文压力测试与 Writer 检查拆分建议见 [RECOMMENDATION.md](RECOMMENDATION.md)。

原始提示、模型响应和裁判响应仅保存在本地 `.ainovel/benchmarks/stage-quality-v1`，本报告不包含正文或凭证。
