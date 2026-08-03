# OpenNovel

Web-only AI 长篇小说创作工作台。Coordinator 驱动 Architect / Writer / Editor 三个子代理完成整本书的创作，Host 负责启动、恢复和观察。普通共创、小说改编与小说续写都提供可视化进度和质量确认节点；项目可选择自动通过常规审核。

## 特性

- **多智能体协作** — Coordinator 在一次长循环中调度 Architect / Writer / Editor 三个子代理，自主决策创作流程
- **LLM 驱动长循环** — 一次 Prompt 写完整本书，Host 不介入调度。越简单越稳定，拒绝复杂编排
- **Step 级断点恢复** — 每个工具执行成功后写入 checkpoint，崩溃后精确到 plan/draft/check/commit 步骤级恢复
- **卷弧双层滚动规划** — 长篇不再一次性规划全部章节。初始只规划前 2 卷弧骨架 + 第 1 弧详细章节，后续弧/卷在写作推进到时再由 Architect 展开，每次展开都参考前文摘要和角色状态，远期规划不空洞
- **相关章节智能推荐** — 每章写作时从伏笔、角色出场、状态变化、关系四个维度自动推荐相关历史章节，配合下一章预告，确保 500+ 章长篇的连续性
- **自适应上下文策略** — 根据总章节数自动切换全量 / 滑窗 / 分层摘要，支持 500+ 章长篇
- **七维质量评审** — Editor 从设定一致性、角色行为、节奏、叙事连贯、伏笔、钩子、审美品质七个维度评审，审美维度细分描写质感/叙事手法/对话区分度/用词质量/情感打动力五项，每项必须引用原文举证
- **用户实时干预** — 写作过程中随时在输入框注入修改意见（无需暂停），系统自动评估影响范围并重写受影响章节
- **抗网络波动重试** — 共创、源书分析、规则归一化和子代理调用统一采用 7 次有间隔重试；Web 会显示断线、重试和可恢复状态
- **唯一 Web 工作台** — 普通共创、小说改编和小说续写统一展示步骤进度、确认节点、失败恢复、模型用量与缓存效果
- **多 LLM 支持** — OpenRouter / Anthropic / Gemini / OpenAI 等等随意切换

## 架构

核心设计：**LLM 驱动，Host 服务**。Coordinator 在一次 Run 中自主决策整本书的创作流程，Host 只做启动、恢复和事件观察。

```
┌─────────────────────────────────────────────────┐
│                Host（薄外壳）                     │
│           启动 / 恢复 / 观察 / 干预注入            │
└──────────────────────┬──────────────────────────┘
                       │ 一次 Prompt
┌──────────────────────▼──────────────────────────┐
│              Coordinator（LLM 长循环）            │
│    读 novel_context → 调子代理 → 读结果 → 继续     │
└────┬──────────┬──────────┬──────────────────────┘
     │          │          │
 ┌───▼────┐ ┌───▼───┐ ┌────▼────┐
 │Architect│ │Writer │ │ Editor  │
 └───┬────┘ └───┬───┘ └────┬────┘
     └──────────┼──────────┘
                │ 工具调用（IO + checkpoint）
┌───────────────▼─────────────────────────────────┐
│                   Store                         │
│  Progress / Checkpoint / Outline / Drafts / ... │
└─────────────────────────────────────────────────┘
```

- **Host** — 启动 Coordinator、崩溃恢复、事件投影给 Web。不做任何调度决策
- **Coordinator** — 唯一的决策者，在一次 Run 里驱动规划→写作→评审→总结的完整流程
- **SubAgents** — Character / Architect / Writer / Editor 各自独立 context，通过 Store 中的工件协作
- **Tools** — 原子 IO + checkpoint 写入，只返事实 JSON，不夹带指令

### 智能体职责

| 智能体 | 职责 | 工具 |
|--------|------|------|
| **Coordinator** | 调度全局，处理评审裁定和用户干预 | `subagent` `novel_context` |
| **Character** | 独占生成、补全与独立审查规范角色卡；确认前只写候选区 | `character_context` `save_character_candidate` `save_character_review` |
| **Architect** | 生成前提、世界规则和大纲；只消费已确认角色卡 | `novel_context` `save_foundation` |
| **Writer** | 自主完成一章的构思、写作、自审和提交 | `novel_context` `read_chapter` `plan_chapter` `draft_chapter` `check_consistency` `check_adaptation` `check_de_ai` `check_simulation` `commit_chapter` |
| **Editor** | 阅读原文，从结构和审美两个层面审阅 | `novel_context` `read_chapter` `save_review` `save_arc_summary` `save_volume_summary` |

### 写作流程

```
用户需求 → Character 分析/独立审查 → 用户确认角色卡 → Architect 规划 → Writer 逐章写作 → Editor 弧级评审
                                                                   ↑                   │
                                                                   ├── 重写/打磨 ◄──────┘
                                                                   │
                                                            Architect 展开下一弧/卷
                                                           （参考前文摘要+角色快照）
```

Writer 按固定顺序完成每章（写作内容完全自主，工具调用顺序严格）：

1. `novel_context` — 加载上下文（前情摘要、伏笔、角色状态、风格规则、相关章节推荐）
2. `read_chapter` — 回读前文找回语气和节奏
3. `plan_chapter` — 构思本章目标、冲突、情绪弧线
4. `draft_chapter` — 写入整章正文
5. `check_consistency` — 对照状态数据检查一致性（必须在 draft 之后）
6. `check_adaptation` — 小说改编模式专用，对照原文和改编契约检查草稿
7. `commit_chapter` — 提交终稿，返回事实字段（`arc_end_reached` / `next_chapter` 等），下一步由 Reminder 驱动

### 状态迁移规则

系统内部把运行状态拆成两层：

- **Phase** — 大阶段，表示作品目前处于设定期、写作期还是已完成
- **Flow** — 当前活跃流程，表示系统此刻是在正常写作、审阅、重写、打磨还是处理用户干预

#### Phase

`Phase` 采用“只前进不回退”的规则：

```text
init -> premise -> outline -> writing -> complete
  \-------> outline ------^
  \--------------> writing
```

含义：

- `init` — 任务已创建，尚未形成稳定设定
- `premise` — 已保存故事前提
- `outline` — 已保存大纲，可以进入正式写作
- `writing` — 已进入章节创作期
- `complete` — 全书流程结束

规则说明：

- 允许同态更新，例如 `writing -> writing`
- 允许前进，例如 `outline -> writing`
- 不允许回退，例如 `writing -> premise`、`complete -> writing`

#### Flow

`Flow` 只描述写作期内的活跃流程，允许在几个工作流之间切换：

```text
writing   -> reviewing / rewriting / polishing / steering / writing
reviewing -> writing / rewriting / polishing / steering / reviewing
rewriting -> writing / steering / rewriting
polishing -> writing / steering / polishing
steering  -> writing / reviewing / rewriting / polishing / steering
```

含义：

- `writing` — 正常推进下一章
- `reviewing` — Editor 正在评审
- `rewriting` — 处理必须重写的章节
- `polishing` — 处理只需打磨的章节
- `steering` — 正在评估并处理用户干预

规则说明：

- 允许 `writing -> reviewing`，例如章节提交后触发评审
- 允许 `reviewing -> rewriting/polishing/writing`，由评审结果决定
- 允许 `steering -> writing/reviewing/rewriting/polishing`，由干预影响范围决定
- 不允许明显反常的跳转，例如 `rewriting -> reviewing`

这些规则现在由代码中的轻量校验统一约束，避免状态回退或跳到不合理的流程分支。

### 长篇滚动规划

传统方案一次规划所有章节，300+ 章时大纲空洞、节奏像赶进度。本系统采用**指南针 + 视野滚动规划**，模拟网文作者的真实创作流程：

```
初始规划                     弧结束时                      卷结束时
┌────────────────────┐    ┌─────────────────────┐    ┌─────────────────────┐
│ 终局方向（指南针）    │    │ Editor 弧级评审      │    │ Editor 卷级评审       │
│ 起步 2 卷，后续按需   │    │ 弧摘要 + 角色快照     │    │ 卷摘要               │
│ 第1弧详细章节        │ →  │ Architect 展开下一弧  │ →  │ Architect 自主创建   │
│ 角色 + 世界观        │    │ Writer 继续写作      │    │ 下一卷 + 更新指南针    │
└────────────────────┘    └─────────────────────┘    └─────────────────────┘
```

- **指南针（Compass）** — 终局方向 + 活跃长线 + 规模估计，每次卷边界由 Architect 更新，故事方向可随创作演化
- **按需生成** — 当前卷写完后，Architect 根据已写内容自主创建下一卷。初始规划生成 2 卷作为起步，后续卷按需生成
- **骨架弧** — 只有 goal + 预估章数，到达时再展开详细章节
- **渐进细化** — 每次展开都参考前文摘要、角色快照、风格规则，越往后写越精确
- **通用节奏模板** — 成长突破弧 / 竞技对抗弧 / 探索发现弧 / 恩怨冲突弧 / 日常过渡弧，每种弧型有参考密度和适用题材映射

### 长篇上下文管理

500+ 章小说采用三级摘要 + 四级压缩管线 + 智能推荐：

```
卷（Volume）→ 卷摘要
└── 弧（Arc）→ 弧摘要 + 角色快照 + 风格规则
    └── 章（Chapter）→ 章摘要（滑窗最近3章）
```

- **分层摘要** — 近处用章摘要，中距离用弧摘要，远处用卷摘要，层层压缩不丢信息
- **相关章节推荐** — 每章写作时从伏笔、角色出场、状态变化、关系四个维度反查历史章节，推荐 Writer 按需回读
- **下一章预告** — 加载下一章大纲，帮 Writer 设计章末钩子和伏笔衔接
- **弧边界检测** — 自动识别弧/卷结束，触发评审、摘要生成和下一弧/卷展开

#### 上下文压缩管线

当对话超出模型上下文窗口时，按代价从低到高逐级压缩：

```
ToolResultMicrocompact → LightTrim → StoreSummaryCompact → FullSummary
     清理旧工具结果        截断长文本      store 零 LLM 压缩      LLM 摘要兜底
```

- **StoreSummaryCompact** — Writer 专用，用 store 中已有的章节摘要、角色快照、伏笔台账直接替换旧消息，零 LLM 开销
- **FullSummary 小说定制** — Writer 使用面向叙事连续性的摘要提示词，明确要求保留角色状态、伏笔线索、审稿待修项、风格锚点
- **压缩后恢复包** — FullSummary 后自动注入当前章节计划、大纲和角色快照，防止 Writer 压缩后"失忆"
- **熔断器** — 压缩连续失败时自动跳过并显式告警，采用半开模式，下轮自动重试
- **CJK Token 估算** — 中文 `runes × 1.5`，不会因为 `bytes/4` 低估而导致压缩触发滞后
- **Web 健康度可视化** — 上下文占用绿(<70%)→黄(70-85%)→红(>85%)实时展示

## 快速开始

```bash
# 一键安装（macOS / Linux，无需 Go）
curl -fsSL https://raw.githubusercontent.com/while4234/opennovel/main/scripts/install.sh | sh

# 安装指定版本
curl -fsSL https://raw.githubusercontent.com/while4234/opennovel/main/scripts/install.sh | sh -s -- v1.2.3

# 或通过 Go 安装
go install github.com/while4234/opennovel/cmd/opennovel@latest

# 查看版本 / 更新到最新版本
opennovel --version
opennovel update

# 默认启动本地 Web，并自动打开浏览器
opennovel
```

> Windows 或手动安装：前往 [Releases](https://github.com/while4234/opennovel/releases/latest) 下载对应平台的包。

### Web UI（唯一交互界面）

直接运行 `opennovel` 会在 `http://127.0.0.1:9898` 启动本地 Web 并自动打开浏览器。发布包里的 Go 主程序已经嵌入 Web 前端，并与独立的 `expansion-auditor`、`manuscript-completion-auditor`（Windows 使用同名 `.exe`）一起安装在同一目录；两类审核私钥只存在于各自独立组件。缺少或无法启动对应组件时，相关扩写或完本复审保持 pending 并 fail closed，不会静默跳过审核；其他不依赖该审核器的项目功能仍可使用。

`opennovel web` 是服务器部署入口，默认不会打开浏览器；只有显式传入 `--open` 才会打开。

```bash
opennovel web
opennovel web --open
opennovel web --host 0.0.0.0 --port 9898
opennovel web --runtime-root D:\OpenNovel\novels-preview
```

仓库开发环境下，重启 Web 只使用根目录的一键脚本：

```powershell
.\restart-web.cmd
.\restart-web.cmd -Port 9898
.\restart-web.cmd -Port 9898 -RuntimeRoot "$env:USERPROFILE\.opennovel\novels-preview"
.\restart-web.cmd -Port 9898 -StopPorts 9898,9901
```

这个脚本会按相对路径定位仓库，先构建 `internal/entry/web/ui`，再构建 Go 二进制到临时文件；构建成功后才停止旧端口、覆盖 `opennovel.exe`、启动 Web 并检查 `/api/runtime` 和项目列表。运行时根目录解析顺序是：`-RuntimeRoot`、`OPENNOVEL_WEB_RUNTIME_ROOT`、`OPENNOVEL_RUNTIME_ROOT`、已存在的 `~/.opennovel/novels-preview`、最后退回 CLI 默认配置。后续开发重启请统一使用这个脚本，快速复用现有构建时可加 `-NoBuild`。

Windows 本地 Go 构建和测试统一使用项目入口，所有构建缓存、模块缓存和临时文件都会写入 D 盘仓库下的 `.cache/`，不再占用 `AppData\Local\go-build`。`test` 命令结束后会自动删除本次 Go 构建/测试缓存和临时文件：

```powershell
.\configure-go-cache.cmd
.\go-project.cmd test ./...
.\go-project.cmd build ./cmd/opennovel
.\clean-project-cache.cmd
```

`configure-go-cache.cmd` 会把 Go 的持久缓存位置保存到当前用户的 Go 配置，确保直接调用 `go` 时构建缓存仍落在 D 盘；日常仍应优先使用 `go-project.cmd`，因为它还会隔离测试进程的 `TEMP/TMP` 并自动清理。需要同时清除迁移前遗留的 C 盘缓存时，运行 `powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\scripts\clean-project-cache.ps1 -IncludeLegacyCDriveCache`。

Windows 用户可以从快捷方式或任意命令窗口直接运行 `opennovel`，然后在浏览器里创建、打开和切换多本小说。

如果还没有可用模型配置，Web 会先显示设置向导：选择服务商、填写模型和凭证、测试连接、保存。测试接口不会落盘或回显密钥；验证并保存成功后才开放项目创作。

```text
GET  /api/setup
POST /api/setup/test
POST /api/setup/complete
```

Web UI 把每本小说保存为运行时项目，默认运行时根目录是：

- Windows：`%USERPROFILE%\.opennovel\novels-preview`
- macOS / Linux：`~/.opennovel/novels-preview`

运行时根目录覆盖优先级从高到低是：

1. `opennovel web --runtime-root <path>`
2. 环境变量 `OPENNOVEL_RUNTIME_ROOT`
3. 配置文件字段 `runtime_root`（配置文件自身仍按 `~/.opennovel/config.json` → `./.opennovel/config.json` → `--config` 合并）
4. 默认 `~/.opennovel/novels-preview`

Web 运行时根目录必须在仓库外；如果指到仓库目录或其子目录，启动会拒绝并提示换一个路径。每本小说位于 `<runtime-root>/projects/<project-id>/`，其输出仍在该项目自己的 `output/` 下。

### Docker

Docker 镜像默认以 Web-only 模式监听 `0.0.0.0:9898`，适合部署在服务器或 NAS。配置和作品目录建议挂载到宿主机：

```bash
mkdir -p config workspace authority

# Web 服务
docker run --rm -p 9898:9898 \
  -v "$PWD/config:/home/opennovel/.opennovel" \
  -v "$PWD/workspace:/workspace" \
  -v "$PWD/authority:/var/lib/opennovel" \
  ghcr.io/while4234/opennovel:latest

# 严格非交互的 headless 自动化
docker run --rm \
  -v "$PWD/config:/home/opennovel/.opennovel" \
  -v "$PWD/workspace:/workspace" \
  -v "$PWD/authority:/var/lib/opennovel" \
  -v "$PWD/answers.json:/workspace/answers.json:ro" \
  ghcr.io/while4234/opennovel:latest \
  --headless --prompt "写一本东方玄幻长篇，主角从边陲小城起步" \
  --answers-file /workspace/answers.json
```

镜像固定以 UID/GID `65532` 运行，并设置 `HOME=/home/opennovel`；因此普通 provider 配置必须挂载到 `/home/opennovel/.opennovel`，代码会从该 HOME 下的 `config.json` 读取。这个可写配置卷与 root 管理的 `/var/lib/opennovel` authority 卷严格分离：后者丢失、未 bootstrap 或被普通 runtime 重建时，发布与完稿审核会 fail closed。

也可以用 Compose：

```bash
docker compose up -d
docker compose run --rm opennovel --headless --prompt "写一本悬疑短篇" --answers-file /workspace/answers.json
```

Web 工作台提供三种创作入口：

- `普通共创`：从创意输入、篇幅与结构、澄清决策、设定/规划审核进入正式创作
- `小说改编`：上传原小说，完成原文分析、改编契约、提案审核、创作和质量审计
- `小说续写`：建立原作基线，依次审核续写 Draft、提案/分卷和章节细纲，再从下一章继续

三种模式最终都会收敛为同一套创作引擎。

### Headless 自动化

`--headless` 只用于严格非交互自动化，不会在终端逐题询问。可能需要用户确认的任务必须提供 `--answers-file <json|->`；答案不足时程序输出结构化错误并退出，便于 CI 或调度器处理。

```json
{
  "answers": {
    "问题或标题": "答案"
  },
  "notes": {
    "问题或标题": "可选补充"
  }
}
```

```bash
opennovel --headless --prompt-file prompt.md --answers-file answers.json
opennovel --headless --prompt "写一本悬疑短篇" --answers-file - < answers.json
```

`--prompt-file -` 与 `--answers-file -` 不能同时读取标准输入。

### 管理多本小说

Web UI 使用运行时根目录管理多本小说：运行 `opennovel` 后，在浏览器里创建和切换项目即可，不需要为每本小说切换工作目录。项目数据落在 `<runtime-root>/projects/<project-id>/`，适合桌面用户和需要同时管理多本书的场景。headless 自动化仍以当前工作目录作为单个任务的作品目录。

### 配置文件

首次运行不要求预先生成模型配置。Web 设置向导会先测试临时配置，验证通过后再原子保存到 `~/.opennovel/config.json` 并初始化运行时。后续可以在 Web 的模型设置中调整，也可以直接编辑配置文件。

也可以手动创建配置文件，参考仓库根目录的 `config.example.jsonc`。

初始化完成后可以在 Web 的模型设置中为已有 provider 增加模型，或新增 OpenAI/Anthropic/Gemini/Grok 协议的服务商配置。保存成功后会写回全局 `~/.opennovel/config.json`。

Grok 账号登录会打开 xAI 授权链接；如果本机 loopback 回调不可用，可以把浏览器回调 URL 或 `?code=...&state=...` 查询串粘贴回 Web 向导。access/refresh token 只保存在本机 `~/.opennovel/auth/grok.json`，`config.json` 只记录 `type:"grok"`、`auth:"grok_oauth"` 和 `account_id`。

```jsonc
{
  "provider": "openrouter",
  "model": "google/gemini-2.5-flash",
  "reasoning_effort": "medium",
  "providers": {
    "openrouter": {
      "api_key": "sk-or-v1-xxx",
      "base_url": "https://openrouter.ai/api/v1",
      "models": ["google/gemini-2.5-flash", "google/gemini-2.5-pro"],
      "extra": {
        "user_agent": "my-client/1.0",
        "headers": { "X-Custom-Client": "my-client" }
      }
    }
  },
  "simulation_mode": "normal",
  "style": "default"
}
```

#### 配置文件查找顺序（后者覆盖前者）

1. `~/.opennovel/config.json` — 全局配置
2. `./.opennovel/config.json` — 项目级覆盖（可选）
3. `--config path/to/config.json` — 命令行指定

> 项目级 `.opennovel/` 是全局 `~/.opennovel/` 的镜像：同样的结构、只是根目录从家目录换成当前项目。配置放 `./.opennovel/config.json`，写作规则放 `./.opennovel/rules/*.md`（详见下文「去 AI 味与自定义规则」）。该目录含密钥，已默认加入 `.gitignore`。

覆盖规则说明：

- 标量字段按后者覆盖前者，例如 `provider`、`model`、`reasoning_effort`、`style`
- `providers` 和 `roles` 按 key 合并，同名项内部按字段覆盖
- 未填写的字段会继承上层配置，例如项目级配置只写 `base_url` 时会保留全局配置中的 `api_key`
- 当前不支持用空字符串显式清空上层已有值；如需清空，请直接编辑更高优先级的配置文件

> ⚠️ `provider`（以及 `roles.*.provider`）的值是 `providers` 里的 **key 名**——一根指针，不是协议名。项目级若把 `provider` 切到一个全局 `providers` 里不存在的账号，必须在项目级同时补上该账号的凭证（`api_key` / `base_url`），否则启动会报“未配置凭证”。

`runtime_root` 只影响 Web UI 项目存储位置；headless 的作品目录仍由当前启动目录决定。Web UI 中 `--runtime-root` 和 `OPENNOVEL_RUNTIME_ROOT` 的优先级高于配置文件字段。

`providers.<name>.models` 为可选字段，用于声明该 provider 在 Web 模型设置中的候选模型列表；如果未配置，系统会回退为当前配置文件里已经出现过的该 provider 模型。

`reasoning_effort` 为默认推理强度，可选值为 `off` / `low` / `medium` / `high` / `xhigh` / `max`；省略或空字符串表示沿用模型/provider 默认。`roles.<role>.reasoning_effort` 可按角色覆盖，未配置时继承顶层 `reasoning_effort`。在 Web 模型设置中切换 provider、model 或推理强度后，会写回全局配置 `~/.opennovel/config.json`。

`providers.<name>.api` 仅对 `type: "openai"` 或内置 `openai` 生效，用于选择 OpenAI 协议 endpoint：`chat`（默认，`/v1/chat/completions`）或 `responses`（`/v1/responses`）。Codex 类代理通常需要配置为 `responses`。

`simulation_mode` 控制仿写画像的使用强度，可选值为 `normal` / `reinforced`；省略或留空时默认是 `normal`，新项目也默认使用 `normal`。

Web 右侧的“定时”页签可以配置多个每日恢复时间。服务按 `resume_schedule.timezone` 的墙钟时间扫描全部项目；项目级“设定”可单独关闭。定时恢复只继续已经启动但被中断的生成流程，等待分卷、提案、共创建议或续写审核时不会自动推进。程序当天晚于配置时间启动或从休眠恢复时，遗漏的时间点会合并补跑一次。

`providers.<name>.auth: "grok_oauth"` 表示使用 Grok 账号登录 token；该 provider 必须是 `type: "grok"`，可省略 `api_key`，`base_url` 留空时默认使用 `https://api.x.ai/v1`。

`providers.<name>.auth: "codex"` 表示复用本机 Codex 登录凭证，不使用 OpenAI API key；该 provider 必须是 `type: "openai"` 且 `api: "responses"`，默认读取 `CODEX_AUTH_FILE`、`CODEX_HOME/auth.json`、项目 `.codex/auth.json` 或 `~/.codex/auth.json`。

`providers.<name>.extra` 为 provider 级配置，会传给底层 HTTP 客户端，适合配置 `user_agent`、`headers`、`anthropic_beta` 等代理识别字段；`providers.<name>.extra_body` 才是请求体扩展参数，两者不要混用。

## 模型用量与 Prompt Cache

Web 的“高级工具 → 模型与缓存”按项目或全局展示调用量、输入/输出 token、非缓存输入、cache read/write、费用、节省、延迟、失败率、重试率与 usage 覆盖率。逐调用 ledger 只保存模型身份、阶段、计数、费用和延迟等元数据，不保存 prompt、模型回复、小说正文或推理内容；明细保留 90 天，之后压缩为日聚合。

- 不支持 Prompt Cache 的模型显示“不支持/N/A”。
- provider 未返回 usage 时显示“不完整”，不会伪装成 0% 命中。
- 高置信度要求 usage 覆盖率 ≥95% 且同组调用 ≥30；中置信度要求覆盖率 ≥80% 且调用 ≥10。低于门槛只展示事实，不生成建议。
- 优化建议只针对 Prompt Cache；系统不会缓存生成结果，也不会在后台自动切换模型。应用模型路由建议必须由用户确认，并校验当前配置 revision。

观测 API：

```text
GET  /api/observability/usage
GET  /api/observability/recommendations
POST /api/projects/{id}/models/apply-recommendation
```

`/api/observability/usage` 支持 `project_id`、`group_by`、`from`、`to` 查询参数；`group_by` 可按模型、角色、流程或阶段聚合。旧累计用量仅作为 `legacy aggregate` 展示，时间和覆盖率标记为未知，不会伪造逐调用历史。

## 诊断报告

在 Web 的“高级工具 → 诊断”中可以分析当前小说的 output 产物，得到可执行的发现和改进建议。

诊断覆盖四个维度：

- **流程** — 改写循环卡顿、未消费的转向指令、阶段/流程状态异常、章节跳号
- **质量** — 评审维度持续低分、合同履约率、改写率、章节字数异常
- **规划** — 伏笔停滞、指南针过时、大纲耗尽、摘要缺失
- **上下文** — 角色消失、时间线缺口、关系数据停滞

每条发现包含：问题描述、数据证据、改进建议（指向具体的 prompt/flow/config）。

诊断同时会写出一份**已脱敏**的 `meta/diag-export.md`（移除小说正文，仅保留工具调用、错误串、重复次数等行为骨架）。遇到死循环 / 中断类问题，把它贴到 GitHub issue 即可，方便维护者在拿不到本地数据的情况下定位。

## 仿写画像

在 Web 的“作品工具 → 仿写画像”上传 `.txt`、`.md` 或 `.markdown` 参考文章并点击“分析”。系统会用 architect 模型分析语料，并写入当前项目：

```text
output/novel/meta/simulation_profile.json        # v2 portable 画像
output/novel/meta/simulation_evidence.local.json # 本项目专用逐篇证据、来源信息与 safety index
output/novel/meta/simulation_checks/NNN.json     # 绑定当前草稿/画像/契约/checker 的章节检查报告
```

`simulation_profile.json` 默认使用 `simulation_profile.v2`，只保存脱敏 feature、support/coverage/confidence、阶段/冲突、能力/健康状态和分析签名，不包含绝对语料目录、逐篇报告、专名或标志短语，可安全用于画像库和跨项目导入。`simulation_evidence.local.json` 保留当前分析流程仍需的来源路径、结构化逐篇报告和本地 safety index，只在本项目内读取，不进入画像库或 Agent 上下文。画像库从项目保存或自动同步画像时，会在独立的本地语料归档目录中同时保存原语料副本和完整性清单；portable JSON 本身仍不嵌入原文。旧 `simulation_profile.v1` 仍可直接加载；首次成功写入时会确定性投影为 v2，并把旧 reports 分流到 local evidence。缺少分析签名的旧画像明确标记为 `legacy` / `analysis_signature_unknown`，不会冒充 fresh。

再次分析时，会同时比较 `relative_path + sha256` 和 source-analysis signature。内容、source prompt/schema、窗口算法或分析模型变化时只重分析受影响文章；只有 merge/reducer/selection signature 变化时复用有效 reports，只做重合成。新增、修改和删除都会更新 corpus digest；删除来源会立即使旧画像和 checkpoint 失效，空语料目录会清除画像、本地证据和 checkpoint。相同 reports 的输入顺序不影响 feature ID、support、coverage、classification 或 evidence refs。

Runner 对超过 15,000 rune 的单文件使用确定性的头/中/尾三窗口，并在 report 中记录实际 coverage 和 health；非正文、疑似二创、低覆盖及局部报告不会被当作全局稳定证据。可复制专名、罕见片段和标志短语只进入本地 safety index，供后续安全扫描使用，不会成为 Writer guidance。

右侧栏也可以直接按文件名搜索语料。搜索只展示前 5 个 TXT 结果；点击“下载”后，后端通过 BaiduPCS-Go 下载、再次校验文件类型，并加入当前项目的 `simulate/` 目录。长文本沿用上传流程自动按章节拆分，随后可以直接点击“分析”。大力盘没有 TXT 结果时，Windows 版本会在后台以无界面 Edge/Chrome 尝试百度智能体兜底；不会打开或抢占用户正在使用的浏览器，兜底失败时明确显示“没有找到 TXT 文件”。

私有发行版内置加密的搜索/下载凭据和固定版本的 BaiduPCS-Go 安装信息。首次使用会在运行时目录自动下载并校验 BaiduPCS-Go，另一台 Windows 电脑拉取并构建本仓库后不需要复制浏览器 profile、Cookie 或手工配置下载工具。内置密文与解密材料同仓库分发，只用于私有仓库的免配置部署，并不构成对仓库读取者的安全隔离；仓库访问权限必须保持私有。

也可以在同一面板导入之前生成的画像，避免重复分析同一批文章。导入接受本功能生成的 `simulation_profile.v2` portable JSON，并在兼容窗口内继续接受 v1；v1 导入画像在进入画像库前会自动脱敏投影为 v2。两个 portable-only 画像仅在分析签名兼容时按稳定 feature identity 和带命名空间的 provenance 合并；签名不兼容，或当前项目仍绑定 local evidence 时会明确拒绝，不再做字符串拼接。只导入可信来源的画像文件；导入内容会成为后续 Agent 的上下文参考。

正式创作不再让各 Agent 自行解释整份画像。系统在 `meta/simulation_contract.json` 持久化一份带 digest/revision 的 `SimulationContract`，只引用稳定 feature ID，并绑定 profile digest、仿写 mode、creative brief 和 Foundation revision。任一绑定变化都会生成新 revision；stale/invalid/missing 画像会明确返回 inactive/degraded reason，不会冒充强化已生效。Coordinator 只读取 health/status；Architect、Writer、Editor 分别读取 planning、chapter、review 角色视图，且共享同一 contract revision。

### 普通仿写与强化仿写

仿写模式有两档：`normal`（普通仿写）和 `reinforced`（强化仿写）。默认始终是 `normal`；即使项目已经加载仿写画像，也不会自动切到强化仿写。

`normal` 的正式角色视图只选择少量跨来源稳定、高置信度且阶段适用的 feature；除安全/avoid 外均为 advisory `should`，主观风格偏离不会阻塞创作。普通共创和阶段共创不会主动把画像注入对话提示。

`reinforced` 需要在 Web 右侧栏开启：`设定 -> 仿写画像 -> 仿写模式`。开启后，冷启动共创、阶段共创和正式写作使用同一结构化 contract：预算和维度覆盖显著高于 normal，只有稳定、高覆盖、高置信度且可客观验收的结构/钩子/节奏 feature 可成为 `must`；模糊审美仍是 `should`。用户要求、creative brief、Foundation、改编/章节合同和当前 POV 始终优先。

无论 `normal` 还是 `reinforced`，Agent 上下文只解析 contract 引用的 portable 抽象 feature，不注入 `source_reports`、raw source、本地路径、safety index、来源专名或 signature phrase 库；也不会复制源文句子、人物、地名、专有设定或固定桥段。

Writer 在最终正文修改结束后、`commit_chapter` 前必须运行 `check_simulation`。本地 scanner 会在进程内做 Unicode/空白/标点规范化，结合跨来源 rarity 检查长连续片段、罕见 n-gram、来源特有专名/术语、标志短语和高辨识度短语组合；常用短表达会通过 allowlist 和跨来源支持数降权。报告只展示当前草稿中的可修改片段与脱敏 source reference，不回显来源原句、绝对路径、完整专名库，也不把索引发送给模型。草稿、profile digest、contract revision/mode、安全索引或 checker 配置变化后，旧报告立即 stale，commit gate 要求重跑。

`normal` 只因确定性 copy/safety 风险阻塞；主观 should 偏离仅交给 Editor 建议。`reinforced` 还会阻塞缺失的 measurable must，但只接受章节计划/细纲中的结构化证据，不用脆弱关键词给风格“像不像”打硬分。缺少本地 safety index 的 portable-only/legacy 项目会明确返回 `partial/unavailable`，不会虚假声称通过完整来源扫描，也不会仅因能力缺失无条件禁止提交。该检查是工程风险控制，不构成法律结论。

Web 画像面板只读取后端 canonical diagnostics summary，不下载完整画像或本地证据。面板会区分 `fresh`、`stale`、`portable_only`、`legacy`、`invalid`，显示报告覆盖、分析/合成签名、本地安全能力、特征分类、selected/effective mode、contract revision/绑定/排除原因，以及当前章节检查的 `pass` / `partial` / `not_run` / `stale` / `fail` / `error`。Architect、Writer、Editor 的 normal/reinforced 注入预览来自同一 mode policy 和 contract compiler；selected reinforced 但契约缺失、过期或 inactive 时，UI 不会宣称强化已落实。

重新扫描、仅重合成和全量重分析是三个不同操作。重新扫描让后端根据内容与签名选择最小工作量；仅重合成只在所有本地逐篇报告仍有效时可用；全量重分析只在项目仍有本地语料时可用。按钮可用性和禁用原因来自后端 summary，前端不自行推断。

画像库中的画像文件始终是 `simulation_profile.v2` portable JSON。通过“保存当前画像到库”或分析完成后的自动同步写入时，系统还会把当前项目 `simulate/` 中的原语料复制到 `simulation_corpus_library/<画像名>/sources/`，并写入绑定画像 digest、文件名、大小和 SHA-256 的 `bundle.json`；加载该条目时会校验完整性并把语料恢复到目标项目，因此流程或画像版本升级后可以重新分析。直接上传的单个 JSON 无法携带原语料，会继续作为兼容的“仅画像”条目显示。v1 上传仍会先迁移、脱敏并标记 migrated/legacy 能力；portable JSON 不写入 `source_dir`、`source_reports`、raw 文本或 safety index。

Web UI 的上传入口会把仿写语料保存到当前项目的 `simulate/` 目录，把导入的画像 JSON 保存到当前项目的 `profiles/imported/` 目录；小说改编上传的原文保存在当前项目的 `uploads/adaptation/` 目录。画像库的语料归档只存在本机 runtime，不会写入仓库；加载到已有不同语料的项目会拒绝覆盖，避免静默丢失文件。点击“分析”后，语料内容会按所选模型/provider 的正常调用路径发送给模型生成画像；不要上传或归档没有授权处理的私人文本。

### 离线仿写回归基线

运行 `.\scripts\run-simulation-e2e.ps1` 可使用仓库原创合成 fixture
离线验证 analyze → profile → contract/context → check → commit gate 全链路。
命令同时输出人类可读摘要和 `.cache/simulation-e2e/report.json`，失败时返回
非零退出码；不需要网络、真实模型、浏览器账号或用户目录。指标边界、
payload 预算、fixture/golden 更新规则及可选 provider eval 的授权要求见
[`docs/simulation-e2e.md`](docs/simulation-e2e.md)。

## 导入

在 Web 的“续写”面板上传已有小说，可以按章切分，再用 LLM 反推出前提 / 角色 / 世界观 / 分层大纲 / 指南针并逐章落盘。原文会作为已完成正文建立续写基线；导入完成后不会自动开写，需依次确认 Draft、提案/分卷以及章节细纲，审核通过后才从下一章继续。流程状态会持久化，刷新页面后仍可查看、重试或继续。

**章节切分规则**：自动识别这些标题格式（行首，可带 `#`/`##` Markdown 前缀、`【】`/`〖〗` 包裹、`【书名】第N章` 前缀、全角空格，兼容 GBK/BOM 编码）：

- 中文编号：`第一章` `第3回` `第十话` `第二卷` `第五节` `第二幕`、独立 `卷一`，数字支持大写（`第壹章`），可带副标题（`第三章：决战` `【女神攻略】第九章银屏女神`）
- 卷章同行：`卷一 白蛇妖仙 第一章 医大女鬼` `卷二 蛇指影魔 引子` `卷十三 逐愿尸奴 尾声`
- 旧式中文编号：`十一章 难以接受` `廿一章 旗袍研究`
- 中文特殊单元：`序章` `楔子` `引子` `前言` `尾声` `终章` `后记` `番外` `外传`
- 英文：`Chapter 1` `Chapter II`、`Prologue` `Epilogue`，可带副标题（`Chapter 1: The Beginning`）

纯卷名后紧跟章节标题时只作为卷边界，不会单独生成空章；常见资料类标题如 `预告`、`灵异档案`、`编者语` 只作为切分边界丢弃，不进入导入/改编章节。若源站文本把章节标题粘在上一句末尾（如 `……。”第十三章 最后清算`），会在标题位于行尾时自动拆开。

若提示**"未识别到任何章节"**，请确认文件确为分章小说文本（章节标题独占一行、位于行首）。

> 导入是确定性回放，不经过 Coordinator；原文会逐字落盘为已完成章节，因此适合"续写同一本书"。如果只想借鉴设定做全新创作，请用普通方式起一本新书、在需求里描述想要的风格设定。

## 小说改编

`小说改编` 不等同于“续写导入”：它会把原文章节保存到 `meta/adaptation/source_chapters/` 作为对照快照，生成原书分析和改编计划，但不会把原文章节写成最终正文。Writer 每章需要先读取对应 `source` 章节，再写新的改编正文，并在提交前通过 `check_consistency` 和 `check_adaptation`。

Web：打开“小说改编”，上传原小说并等待分析完成，再选择 `chapter` / `arc` / `free`。系统会按结构固定改写策略：`chapter => preserve_details`，`arc/free => full_rewrite`。模式确定后进入改编契约与提案审核，确认具体改编目标后开始创作。

如果模型流式回复遇到临时 EOF、网络断开或服务端短暂抖动，系统会按统一退避策略最多尝试 7 次；重试仍失败时，Web 会保留当前步骤、错误原因与可恢复操作。浏览器刷新或事件流重连不会取消后台任务。

Headless：

```bash
opennovel --headless --adapt ./source.txt --prompt-file adapt.md --answers-file answers.json
```

改编 brief 可以写明关系线目标，例如"主线不要走偏，强化女主互动，弱化另一个女主与男主的感情戏，改成单女主纯爱"。系统支持 `chapter` / `arc` / `free` 三档粒度，默认 `chapter`；改写策略由粒度固定：`chapter` 使用 `preserve_details` 且字数容差为 ±15%，`arc/free` 使用 `full_rewrite`，不启用硬字数容差，只约束主线稳定、source coverage 和禁止搬运原文。若命令行在 `arc/free` 下传入 `--adapt-word-tolerance`，该值会被忽略并显示为 `disabled`。这些模式在改编共创前通过固定选项确认，不再由 AI 追问。

## 导出

Web UI 的“作品工具 → 小说导出”可以把已完成章节合并为 TXT 或 EPUB。文件名不带 `.txt` / `.epub` 时会按所选格式自动补齐；手填后缀与所选格式冲突时会提示错误。导出是只读操作，写作中途也可以随时获取“现阶段成品”，不会影响 Coordinator 运行。

- **TXT** — `《书名》` → 卷分隔 → 章节正文（长篇分层模式自动加卷分隔）。两类内部数据**不进导出**：premise（创作蓝图，含目标读者 / 写作禁区等后台信息，写给作者与引擎看的）、弧分隔（读者视角下弧是过细的内部结构）。导出器统一生成"第 N 章 标题"，正文里 writer 自带的重复标题（`# 第N章…` 或 `# 章节名`）会被剥掉。
- **EPUB** — EPUB 3 标准容器，含封面页、目录、按章拆分的 XHTML，标识符基于内容稳定派生（重导出同一本书阅读器识别为更新版本）。不带封面图。

范围内未完成的章节会跳过并显示在结果里，不算错误。

#### 按角色使用不同模型

通过 `roles` 字段为不同智能体分配不同的模型，未配置的角色使用默认模型：

```jsonc
{
  "provider": "openrouter",
  "model": "google/gemini-2.5-flash",
  "reasoning_effort": "medium",
  "providers": {
    "openrouter": { "api_key": "sk-or-v1-xxx", "base_url": "https://openrouter.ai/api/v1" },
    "anthropic": { "api_key": "sk-ant-xxx" }
  },
  "roles": {
    "character": { "provider": "openrouter", "model": "google/gemini-2.5-pro", "reasoning_effort": "high" },
    "stage:character_review": { "provider": "anthropic", "model": "claude-sonnet-4", "reasoning_effort": "high" },
    "writer": { "provider": "anthropic", "model": "claude-sonnet-4", "reasoning_effort": "high" },
    "architect": { "provider": "openrouter", "model": "google/gemini-2.5-pro", "reasoning_effort": "low" }
  }
}
```

可配置的角色：`coordinator` / `character` / `architect` / `writer` / `editor`。Character Agent 还可分别覆盖 `stage:character_analysis` 与 `stage:character_review`；未配置 stage 时回退到 `character`，再回退到全局默认。三处都支持 `provider`、`model`、`fallbacks` 与 `reasoning_effort`，全局 prompt 规则也会注入 Character Agent。

#### 自定义代理

选择任意 Provider 后填写代理地址即可，或使用 Custom Proxy 并指定 API 协议类型。自定义代理的 `api_key` 可选；如果你的代理不需要认证，可以省略：

```jsonc
{
  "provider": "my-proxy",
  "model": "gpt-4o",
  "providers": {
    "my-proxy": {
      "type": "openai",
      "base_url": "https://proxy.example.com/v1",
      "extra": {
        "user_agent": "my-client/1.0",
        "headers": { "X-Custom-Client": "my-client" }
      }
    }
  }
}
```

支持的 Provider：`openrouter` / `anthropic` / `gemini` / `openai` / `deepseek` / `qwen` / `glm` / `grok` / `ollama` / `bedrock` 及任意自定义代理。

如果代理是 Anthropic 协议，并限制只能由 Claude Code 客户端访问，`type` 应设为 `anthropic`，`anthropic_beta` 放在 `extra` 顶层，Stainless 等 HTTP 头放在 `extra.headers` 中：

```jsonc
{
  "provider": "claude-code-proxy",
  "model": "claude-sonnet-4-6",
  "providers": {
    "claude-code-proxy": {
      "type": "anthropic",
      "api_key": "sk-xxx",
      "base_url": "https://proxy.example.com",
      "extra": {
        "user_agent": "claude-code/2.1.183",
        "anthropic_beta": "claude-code-20250219",
        "headers": {
          "X-Stainless-Lang": "js",
          "X-Stainless-Package-Version": "0.94.0",
          "X-Stainless-Runtime": "node"
        }
      }
    }
  }
}
```

如果要直接复用 Codex 登录凭证，先确保本机已完成 `codex login`，然后使用 `auth: "codex"`：

```jsonc
{
  "provider": "codex-login",
  "model": "gpt-5.5",
  "providers": {
    "codex-login": {
      "type": "openai",
      "auth": "codex",
      "api": "responses",
      "base_url": "https://chatgpt.com/backend-api/codex",
      "auth_file": "",
      "models": ["gpt-5.5"]
    }
  }
}
```

如果代理是 OpenAI/NewAPI 协议，并限制只能由 Codex 客户端访问，`type` 应设为 `openai`，用 `extra.user_agent` 覆盖默认 `litellm-go/0.1`，并在 `extra.headers` 里透传 Codex 识别头。示例里的 `Session_id` 和 `X-Codex-Turn-Metadata` 应换成稳定的随机值；它们同时兼容 New API 的 Codex 透传模板和 sub2api 的 `x-codex-*` 指纹检查：

```jsonc
{
  "provider": "codex-proxy",
  "model": "gpt-5.4",
  "providers": {
    "codex-proxy": {
      "type": "openai",
      "api_key": "sk-xxx",
      "base_url": "https://proxy.example.com/v1",
      "models": ["gpt-5.4", "gpt-5.4-mini", "MiniMax-M3"],
      "api": "responses",
      "extra": {
        "user_agent": "example-model-client/1.0",
        "headers": {
          "Originator": "example-model-client",
          "Session_id": "replace-with-random-session-id",
          "X-Codex-Turn-Metadata": "replace-with-random-turn-metadata"
        }
      }
    }
  }
}
```

关于 `api_key`：

- `openrouter` / `anthropic` / `gemini` / `openai` / `deepseek` / `qwen` / `glm` / `grok` 这类托管接口通常需要填写 `api_key`
- `ollama` 和 `bedrock` 允许不填 `api_key`；Bedrock 需在 `extra` 中配置 `region`、`access_key_id`、`secret_access_key`（可选 `session_token`）
- 显式指定了 `type` 的自定义代理允许不填 `api_key`

例如本地 `ollama` 配置：

```jsonc
{
  "provider": "ollama",
  "model": "qwen3:latest",
  "providers": {
    "ollama": {
      "base_url": "http://localhost:11434/v1"
    }
  }
}
```

### 写作风格

通过配置文件的 `style` 字段切换：

- `default` — 通用风格
- `suspense` — 悬疑推理
- `fantasy` — 奇幻仙侠
- `romance` — 言情

### 去 AI 味与自定义规则

内置一份去 AI 味基线（出厂默认）：机械黑名单（套句 / 疲劳词，代码内置 `rules.SystemDefaults()`，commit 时确定性检查）+ 语义判据 `assets/references/anti-ai-tone.md`（注入 writer / editor 规避与举证）。

想叠加自己的偏好**无需改源码**：在 `~/.opennovel/rules/` 目录（全局，放任意 `.md`，按文件名字典序合并）或 `./.opennovel/rules/` 目录（本书，同样放任意 `.md`，与全局同形态）里，**用大白话写偏好即可**（如「主角别写成圣母」「多用身体感知」「每章 3000 字左右」「不要出现『某种程度上』」）——零格式、零 YAML。系统会用模型把这些自然语言要求归一化成本书规则快照（字数范围 / 禁用词 / 疲劳词阈值等结构化约束 + 风格偏好），写作时自动遵循、提交时自动机械自检；常见 AI 套句与疲劳词的机械基线已内置，不写也能用，就近覆盖、与内置基线叠加生效。

## 输出结构

所有创作数据（章节、大纲、角色、进度等）保存在output目录中。中断后重新运行会自动从上次进度续写。删除output目录将重新开始创作。

```
output/{novel_name}/
├── chapters/           # 终稿（Markdown）
│   ├── 01.md
│   └── ...
├── summaries/          # 章节摘要（JSON）
├── drafts/             # 章节草稿
├── reviews/            # 评审报告
├── meta/
│   ├── premise.md      # 故事前提
│   ├── outline.json    # 扁平章节大纲（仅含已展开的章节）
│   ├── layered_outline.json # 分层大纲（当前卷 + 预览卷，长篇模式）
│   ├── compass.json   # 终局方向指南针（长篇模式）
│   ├── characters.json # 角色档案
│   ├── world_rules.json# 世界规则
│   ├── progress.json   # 进度状态
│   ├── timeline.json   # 时间线
│   ├── foreshadow.json # 伏笔台账
│   ├── state_changes.json # 角色状态变化记录
│   ├── style_rules.json# 写作风格规则（弧边界时提炼）
│   ├── snapshots/      # 角色状态快照（长篇）
│   ├── checkpoints.jsonl # Step 级 checkpoint（每个工具成功后追加）
│   ├── characters.md   # 角色档案（可读版）
│   └── world_rules.md  # 世界规则（可读版）
```

## 断点恢复

写一部长篇小说可能需要数小时甚至数天，中途崩溃、断网、Ctrl+C 都是常见情况。系统在**同一目录再次运行时自动恢复**，无需手动操作。

### 恢复场景

| 中断时机 | 恢复行为 |
|---|---|
| 规划阶段（正在构建世界观/大纲） | 检查已保存的设定，自动补全缺失项 |
| 某章正在写作（有草稿未提交） | 从该章续写，读取已有草稿继续 |
| 审阅进行中 | 重新触发 Editor 评审 |
| 重写/打磨队列未清空 | 继续处理待重写的章节 |
| 弧/卷展开中断（评审完但下一弧未展开） | 自动检测骨架弧/卷，触发 Architect 展开 |
| 用户干预未完成 | 重新注入上次的干预指令 |
| 正常写作中断 | 从下一章继续 |

### 工作原理

所有创作产物持久化在 `output/` 目录。每个工具执行成功后写入 checkpoint (`meta/checkpoints.jsonl`)。重启时：

1. 读取 `progress.json` + 最近 checkpoint + 待处理信号
2. 精确到 step 级生成恢复指令（如"第 7 章 draft 已落盘，请继续 check_consistency"）
3. 一次 `Prompt` 启动 Coordinator，进入长循环继续创作

> 文件写入使用 temp + fsync + rename 原子操作，即使在写入过程中断电也不会损坏已有数据。

## 实时干预（Steer）

创作过程中可以随时通过输入框注入修改意见，**不需要暂停或重启**。

### Web 工作台

创作启动后，在当前流程的干预输入框填写修改意见：

```
❯ 把感情线提前到第4章，增加男女主的对手戏
```

提交后系统自动：
1. 记录干预指令到 `run.json`（崩溃恢复用）
2. 注入到正在运行的 Coordinator
3. Coordinator 评估影响范围，决定是修改设定、重写已有章节，还是在后续章节调整

### 干预示例

| 干预指令 | 系统可能的响应 |
|---|---|
| "主角改成女性" | 修改角色设定，评估已写章节是否需要重写 |
| "把感情线提前到第4章" | 调整大纲，可能重写第4章及后续 |
| "加入一个反派角色" | 更新角色档案和世界规则，在后续章节引入 |
| "节奏太慢了，加快推进" | 调整后续章节的大纲密度 |

## 设计理念

> **把复杂度从代码搬到模型里。** 代码越少，能坏的地方越少。决策权交给更擅长做决策的角色。

### LLM 驱动，越简单越稳定

- **决策权归 LLM** — 流程决策全部由 Coordinator 自主判断，Host 不介入。工具失败时返回结构化错误，由 LLM 自行决定重试或调整策略
- **工具只返事实** — 原子 IO + checkpoint 写入，返回值是 JSON 事实字段（`final_verdict` / `pending_rewrites` / `arc_end_reached`），不夹带任何指令字符串
- **Reminder 驱动每轮** — Host 在每轮 LLM 调用前读事实层，运行纯函数 generator 生成 `<system-reminder>` 注入，指令不进持久历史、每轮从事实重算
- **StopGuard 物理守门** — `Phase ≠ Complete` 时 Coordinator 物理上不可 `end_turn`，连续阻拦超限才升级终止
- **拒绝复杂业务编排** — 没有把 Agent 决策拆成 task queue 或 policy engine；每日恢复调度器只负责在安全状态门禁后唤醒既有流程，Coordinator 的一次 Run 仍是唯一创作控制流
- **模型越强收益越大** — 架构把决策权留在 prompt 和工具语义里，模型升级后直接吃到收益，Host 一行不用改

### 可自动闭环

默认在创作契约、设定/提案和大纲处等待确认；开启项目“自动通过”后，可从一句话输入持续生成完整小说。质量检查失败、模型异常和规则冲突仍会暂停：

```
“写一部悬疑小说” → 构建世界观 → 设计角色 → 规划大纲
                → 逐章写作 → 质量评审 → 自动重写
                → 弧级摘要 → 角色快照 → 完整成书
```

- **Coordinator 自主调度** — 在一次长循环里读事实层 + Reminder 决定下一步，无需 Host 干预
- **Writer 自主创作** — 每章独立完成 plan → draft → check → commit 的完整闭环
- **Editor 自主评审** — 跨章节分析结构问题，输出裁定及影响范围
- **Architect 自主构建** — 从一句话需求推导出完整设定，弧/卷边界时自主展开后续规划
- **自动伏笔管理** — 埋设、推进、回收全程由 Agent 自行追踪
- **自动节奏调控** — 追踪叙事线和钩子类型历史，避免连续章节结构雷同

### 事实与指令解耦

工具只返事实，指令由 Reminder 每轮从事实层重算：

- `commit_chapter` / `save_review` 返回结构化事实（`final_verdict` / `pending_rewrites` / `arc_end_reached` / `next_chapter`），不夹带任何 `[系统]` 字符串
- `internal/host/reminder/` 下的纯函数 generator 读 `Progress` + `Outline`，每轮 pre-turn 生成 `<system-reminder>`：`flow`（当前该做什么 / 弧末刹车）/ `queue_guard`（队列未清禁止新章）/ `book_complete`（全书完成才放行）。物理兜底由 `StopGuard` 在 `phase≠Complete` 时拒绝 `end_turn` 承担
- Reminder 只存活一轮，不进历史、不参与压缩；规则有单元测试，退化可被回归捕获

这样指令不会被链式调用吞掉，也不会在工具产物里漂移。改 bug 只需加一个 generator + 一个测试。

## 技术栈

- **Go 1.25** — 主语言
- **[agentcore](https://github.com/voocel/agentcore)** — 极简 Agent 内核（tool-calling + streaming）
- **[litellm](https://github.com/voocel/litellm)** — 统一 LLM 接口适配
- **React + Vite** — 唯一 Web 工作台与嵌入式前端构建

## 稿件修订与生产恢复

专业稿件工作区把 stable ID、current publication、revision journal 和签名工件作为正式真值；candidate、content index、cache 和 numeric chapter 都只是可重建投影。启动恢复未完成时仍可读取 current，但正文修订、一句话扩写、自动/定时恢复等写入口会统一返回 `publication_recovery_required`，且人工确认节点不会被 resume 越过。

- [稿件修订架构](docs/manuscript-revision-architecture.md)
- [稿件工作区用户指南](docs/manuscript-workspace-user-guide.md)
- [真实项目克隆验收指南](docs/manuscript-real-project-validation.md)

真实项目验收必须显式选择一个 normal 和一个 adaptation 项目，并只在独立 clone root 操作；脚本不会自动选择项目：

```powershell
powershell -NoProfile -File .\scripts\e2e\manuscript-clone-validation.ps1 -Config C:\private\validation.json
```

首次启动前，必须在同一个持久化 authority volume 中执行一次受管初始化；该目录是发布签名根、trust pin 与单调 checkpoint 的固定部署位置，不能用 `XDG_CONFIG_HOME`/`APPDATA` 或项目内容重定向：

```bash
docker run --rm -u 0 \
  -v "$PWD/authority:/var/lib/opennovel" alpine:3.22 \
  sh -c 'install -d -o root -g root -m 0755 /var/lib/opennovel /var/lib/opennovel/publication-authority-installation-v1 && install -d -o 65532 -g 65532 -m 0700 /var/lib/opennovel/publication-authority-v1'
docker run --rm \
  -v "$PWD/authority:/var/lib/opennovel" \
  --user 0 ghcr.io/while4234/opennovel:latest authority init
# bootstrap 完成后仅把可写 root 交给镜像运行 UID；installation sibling 保持 root-owned
docker run --rm --entrypoint sh -u 0 \
  -v "$PWD/authority:/var/lib/opennovel" alpine:3.22 \
  -c 'chown -R 65532:65532 /var/lib/opennovel/publication-authority-v1 && chmod 0700 /var/lib/opennovel/publication-authority-v1'
```

Windows 原生安装应从提升的管理员 PowerShell 运行 `scripts/install-authority-windows.ps1 -ServiceAccount <account>`；脚本在 `C:\ProgramData\OpenNovel` 建立受保护 DACL。普通服务账户只能修改 root 内的子工件并读取 installation anchor，不拥有 parent/root/installation 对象的删除、改 DACL 或改 owner 权限。

### Authority orphan maintenance

Every `NewStore` startup performs a bounded scan of the release-managed
`publications/` journal registry. Operators can also run it explicitly:

```bash
opennovel authority gc
```

The command prints anonymous counts only. Reachable projects are reconciled
under their project revision transaction lock; moved or deleted projects are
processed only after the retention period and only when the protected journal
and private registry record match exactly. Unknown schema, unsafe links,
permission drift, and record ABA fail closed without deleting evidence.

## License

MIT

本项目积极参与并认可 [linux.do 社区](https://linux.do/)。
# StoryFoundation 设定中心

Web 设定中心以 canonical `StoryFoundation` 管理原创与改编目标故事的 premise、稳定 ID 角色、计划关系和世界规则。普通与改编的新流程都由 Character Agent 生成候选、独立审查并经用户显式确认；确认时才原子发布角色卡及兼容 `CoreCastContract`，之后 Architect 只负责 premise、world rules 和 outline。旧五段共创/CoreCast checkpoint 仍可读取与恢复，但不会让新流程重新生成 `<cast>`。改编的 `SourceFoundation` 永久只读，正文开始后 target Foundation 也只读。

小说仓库加载会校验版本化 SourceFoundation 绑定，并对旧资料包自动执行增量升级；有效的逐章报告会按签名复用。原文分析区的“增量重新分析并升级”可主动复查来源资料，而共创区的“重新生成共创简报”只刷新 briefing，两者不再混用同一“重新分析”名称。

计划关系图谱基于 `@xyflow/react`，只映射 `StoryFoundation.relationships`，不读取正文 runtime `relationship_state`。图上的 connect/删除只进入本地 dirty draft，正式写入仍必须经过服务端 signed preview，再以 `preview_id + idempotency_key` apply。角色新旧流程与迁移矩阵见 [Character Agent 迁移说明](docs/character-agent-migration.md)，使用说明、恢复语义、隐私边界和故障排查见 [设定中心文档](docs/story-foundation-center.md)，发布验收见 [StoryFoundation 发布检查清单](docs/story-foundation-release-checklist.md)，依赖许可证见 [第三方许可证](THIRD_PARTY_LICENSES.md)。
