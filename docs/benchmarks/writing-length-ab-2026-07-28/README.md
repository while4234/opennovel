# 正文字数与模型推理等级对比

- 套件：`writing-length-ab-v1`
- 裁判：`codex-login/gpt-5.6-sol@xhigh`
- 样本：3 套原创脱敏语料 × 3 个显式字数档 × 3 个候选
- 评分：内容盲评单列；综合分先按硬指标 30% + 内容 70%，再应用最低字数硬门槛；最高字数为软目标

| 排名 | 候选 | 达标率 | 内容盲评 | 字数门槛综合分 | 平均输出字符 |
|---:|---|---:|---:|---:|---:|
| 1 | `grok-oauth/grok-4.5@high` | 44% | 68.9 | 59.1 | 3384 |
| 2 | `grok-oauth/grok-4.5@xhigh` | 33% | 71.4 | 55.1 | 3742 |
| 3 | `deepseek-infa/deepseek-v4-pro` | 78% | 38.0 | 50.2 | 4919 |

## 分字数档

| 候选 | 目标字数 | 达标 | 平均输出字符 | 内容盲评 | 门槛综合分 |
|---|---|---:|---:|---:|---:|
| `grok-oauth/grok-4.5@high` | 2600–3200 | 2/3 | 2562 | 72.3 | 70.3 |
| `grok-oauth/grok-4.5@high` | 4000–4500 | 1/3 | 3508 | 72.0 | 54.8 |
| `grok-oauth/grok-4.5@high` | 5000–5500 | 1/3 | 4081 | 62.3 | 52.1 |
| `grok-oauth/grok-4.5@xhigh` | 2600–3200 | 1/3 | 2336 | 70.0 | 55.7 |
| `grok-oauth/grok-4.5@xhigh` | 4000–4500 | 1/3 | 3783 | 76.3 | 57.2 |
| `grok-oauth/grok-4.5@xhigh` | 5000–5500 | 1/3 | 5107 | 68.0 | 52.4 |
| `deepseek-infa/deepseek-v4-pro` | 2600–3200 | 2/3 | 3084 | 36.7 | 46.4 |
| `deepseek-infa/deepseek-v4-pro` | 4000–4500 | 2/3 | 4381 | 24.7 | 37.3 |
| `deepseek-infa/deepseek-v4-pro` | 5000–5500 | 3/3 | 7292 | 52.7 | 66.9 |

## 逐题结果

| 候选 | 语料 | 目标字数 | 实际字符 | 达标 | 内容盲评 | 门槛综合分 |
|---|---|---|---:|---|---:|---:|
| `grok-oauth/grok-4.5@high` | harbor_signal | 2600–3200 | 2666 | 是 | 82.0 | 87.4 |
| `grok-oauth/grok-4.5@xhigh` | harbor_signal | 2600–3200 | 2286 | 否 | 63.0 | 44.0 |
| `deepseek-infa/deepseek-v4-pro` | harbor_signal | 2600–3200 | 4130 | 是 | 55.0 | 68.5 |
| `grok-oauth/grok-4.5@high` | glass_greenhouse | 2600–3200 | 2698 | 是 | 70.0 | 79.0 |
| `grok-oauth/grok-4.5@xhigh` | glass_greenhouse | 2600–3200 | 1966 | 否 | 68.0 | 37.8 |
| `deepseek-infa/deepseek-v4-pro` | glass_greenhouse | 2600–3200 | 4861 | 是 | 51.0 | 65.7 |
| `grok-oauth/grok-4.5@high` | winter_archive | 2600–3200 | 2321 | 否 | 65.0 | 44.6 |
| `grok-oauth/grok-4.5@xhigh` | winter_archive | 2600–3200 | 2757 | 是 | 79.0 | 85.3 |
| `deepseek-infa/deepseek-v4-pro` | winter_archive | 2600–3200 | 262 | 否 | 4.0 | 5.0 |
| `grok-oauth/grok-4.5@high` | harbor_signal | 4000–4500 | 4471 | 是 | 84.0 | 88.8 |
| `grok-oauth/grok-4.5@xhigh` | harbor_signal | 4000–4500 | 4061 | 是 | 72.0 | 80.4 |
| `deepseek-infa/deepseek-v4-pro` | harbor_signal | 4000–4500 | 0 | 否 | 0.0 | 0.0 |
| `grok-oauth/grok-4.5@high` | glass_greenhouse | 4000–4500 | 3263 | 否 | 70.0 | 40.8 |
| `grok-oauth/grok-4.5@xhigh` | glass_greenhouse | 4000–4500 | 3743 | 否 | 83.0 | 46.8 |
| `deepseek-infa/deepseek-v4-pro` | glass_greenhouse | 4000–4500 | 6259 | 是 | 56.0 | 69.2 |
| `grok-oauth/grok-4.5@high` | winter_archive | 4000–4500 | 2791 | 否 | 62.0 | 34.9 |
| `grok-oauth/grok-4.5@xhigh` | winter_archive | 4000–4500 | 3546 | 否 | 74.0 | 44.3 |
| `deepseek-infa/deepseek-v4-pro` | winter_archive | 4000–4500 | 6883 | 是 | 18.0 | 42.6 |
| `grok-oauth/grok-4.5@high` | harbor_signal | 5000–5500 | 5299 | 是 | 81.0 | 86.7 |
| `grok-oauth/grok-4.5@xhigh` | harbor_signal | 5000–5500 | 7992 | 是 | 77.0 | 83.9 |
| `deepseek-infa/deepseek-v4-pro` | harbor_signal | 5000–5500 | 8298 | 是 | 54.0 | 67.8 |
| `grok-oauth/grok-4.5@high` | glass_greenhouse | 5000–5500 | 3599 | 否 | 57.0 | 36.0 |
| `grok-oauth/grok-4.5@xhigh` | glass_greenhouse | 5000–5500 | 3031 | 否 | 53.0 | 30.3 |
| `deepseek-infa/deepseek-v4-pro` | glass_greenhouse | 5000–5500 | 6899 | 是 | 46.0 | 62.2 |
| `grok-oauth/grok-4.5@high` | winter_archive | 5000–5500 | 3345 | 否 | 49.0 | 33.5 |
| `grok-oauth/grok-4.5@xhigh` | winter_archive | 5000–5500 | 4297 | 否 | 74.0 | 43.0 |
| `deepseek-infa/deepseek-v4-pro` | winter_archive | 5000–5500 | 6680 | 是 | 58.0 | 70.6 |

## 统计与结论

- `high - xhigh` 内容均分差：-2.6
- `high - xhigh` 门槛综合分差：4.0
- 配对 bootstrap 95% 区间：[-11.9, 20.4]
- 字数门槛：最低字数是硬门槛：低于下限时综合分上限为 50×完成比例；最高字数仅为软目标，超出不触发门槛扣分，冗长或灌水由内容盲评的信息密度维度扣分。

grok-oauth/grok-4.5@high 在字数硬门槛后的综合排名第一。grok-oauth/grok-4.5@high：门槛综合分 59.1、达标率 44%、内容 68.9；grok-oauth/grok-4.5@xhigh：门槛综合分 55.1、达标率 33%、内容 71.4；deepseek-infa/deepseek-v4-pro：门槛综合分 50.2、达标率 78%、内容 38.0。

本报告不包含小说正文、完整模型响应或凭证；原始记录仅保存在本地 `.ainovel/benchmarks/writing-length-ab-v1/`。
