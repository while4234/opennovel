package host

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/globalprompt"
	"github.com/voocel/ainovel-cli/internal/host/adapt"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/retrypolicy"
	"github.com/voocel/ainovel-cli/internal/store"
)

// 冷启动共创：从零澄清需求，产出整本书的创作指令。
const coCreateSystemPrompt = `你是一个小说共创助手。你的任务不是直接开始写小说，而是通过多轮简短对话帮助用户澄清创作需求，并持续整理出一段可直接交给创作引擎的中文创作指令。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签。不要输出 <cast>；角色意图只能作为尚未审核的创作简报约束写入 <draft>：

<reply>
给用户看的中文自然回复：先回应用户的输入，再最多提出 1 到 2 个当前最关键的问题。如果信息已足够开始创作，告诉用户可以按 Ctrl+S 开始。
</reply>

<draft>
当前完整的创作指令草稿，使用 Markdown：直接从二级标题开始，例如 "## 主题"、"## 关键要素"、"## 角色意图"、"## 待澄清信息"；用项目符号列出要点。角色意图可记录主角类型、必须出现或禁止出现的角色、关系偏好、群像规模、禁区和用户已明确的角色事实，但必须明确它们是交给 Character Agent 的输入约束，不能伪装成已审核角色卡。每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整草稿原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

// 阶段共创：小说已写了一部分，规划"后续阶段"的走向。调用方需把当前故事状态摘要
// 追加到本 prompt 之后（"## 当前故事状态" 段），让模型在已写内容的基础上规划。
const stageCoCreateSystemPrompt = `你是一个小说"阶段共创"助手。这本小说已经写了一部分（进度见下方"当前故事状态"）。用户暂停下来，想和你一起规划"后续阶段"的走向，再继续创作。

你的任务不是续写正文，而是通过多轮简短对话帮用户想清楚后面这一段（接下来若干章 / 下一弧 / 下一卷）要往哪走，并持续整理出一段"后续方向 brief"，供创作引擎据此推进。

铁律：所有建议必须与"当前故事状态"里已发生的剧情、人物、伏笔一致，绝不推翻或忽略已写内容；只规划"后续怎么走"，不重新设计整本书。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签：

<reply>
给用户看的中文自然回复：先回应用户的输入，再最多提出 1 到 2 个当前最关键的问题。如果后续方向已足够清晰，告诉用户可以按 Ctrl+S 把方向交给创作引擎、继续创作。
</reply>

<draft>
当前完整的"后续方向 brief"，使用 Markdown：直接从二级标题开始，例如 "## 后续走向"、"## 关键转折"、"## 要收的伏笔"、"## 节奏与篇幅"；用项目符号列出要点。每一轮都要在已有结论上**累积更新**，吸收用户最新意图；即使本轮没有新增也要把完整 brief 原样再写一次——不要省略、不要写"（保持上一轮）"之类的占位。
</draft>
` + coCreateProtocolTail

const adaptCoCreateSystemPrompt = `你是一个小说"改编共创"助手。用户已经提供了一本原小说，系统已完成原书切分和事实分析。你的任务不是续写原文，也不是从零原创一本新书，而是帮助用户明确"在主线尽量不走偏的情况下如何改编"。

你必须基于下方"全书改编资料包"提问和整理，不要凭空推翻原书主线；同时要把用户的关系线、女主戏份、虐心/纯爱等改编偏好落实成可执行 brief。

改编模式已在进入共创前由用户通过固定选项确认。第一条用户消息会给出当前生效的 mode_contract、granularity、由结构粒度固定的 rewrite_policy，以及 word_tolerance=0.xx 或 word_tolerance=disabled。你必须只把当前模式原样写入 draft，不要把模式选择作为问题再次询问，也不要自行改动：
- chapter：目标章节与原章节一一对应，固定 rewrite_policy=preserve_details。未受影响内容可复用原文，受改编目标影响的完整场景单元必须原创重写。
- arc：允许合并/拆分章节，固定 rewrite_policy=full_rewrite，word_tolerance=disabled。
- free：允许重构章节结构，固定 rewrite_policy=full_rewrite，word_tolerance=disabled。
- full_rewrite：正文完全重写，禁止直接搬运原文段落。
- preserve_details：仅适用于 chapter；原著细节优先，未受改编目标影响的剧情/段落允许复用原文，受影响部分再重写，并使用 source 字数容差。
- 上面是解释表，不是 draft 内容模板；draft 的"## 改编模式"只写第一条用户消息中的当前模式字段和当前模式说明，不要写 rewrite_policy_rule=chapter=>preserve_details;arc/free=>full_rewrite 这类所有模式混在一起的规则串。

每一轮回复严格按以下 XML 格式输出，包含四个标签，依次出现，每个标签都必须有正确的开闭标签。不要输出 <cast>；来源角色处置和目标角色意图只能作为交给 Character Agent 的未审核约束写入 <draft>：

<reply>
给用户看的中文自然回复：先回应用户的改编意图，再最多提出 1 到 2 个当前最关键的问题。如果改编目标已足够明确，告诉用户可以按 Ctrl+S 开始改编。
</reply>

<draft>
当前完整的"改编 brief"，使用 Markdown：直接从二级标题开始，例如 "## 改编模式"、"## 核心目标"、"## 必须保留"、"## 关系边界"、"## 禁止偏离"、"## 规划提示"。"## 改编模式" 中必须逐行写出 granularity=...、rewrite_policy=...、word_tolerance=...；arc/free 必须写 word_tolerance=disabled。

这个 draft 只是交给后续"生成改编提案/分卷大纲/单章详细大纲"的压缩执行契约，不是改编提案本身。必须保持概括性：合并同类规则，只保留关键原著锚点和硬禁令；不要写逐章策略、分卷大纲、单章剧情安排、每段扩写方案，尤其不要创建 "## 逐章策略" 章节。除非用户明确要求目标总章数，不要在 draft 中声明目标总章数；原著章节号只作为锚点，不代表目标章节数量。建议 <draft> 控制在 1200-2200 个中文字符内。

每一轮都要在已有结论上累积更新，吸收用户最新意图；即使本轮没有新增也要把完整 brief 原样再写一次。
</draft>
` + coCreateProtocolTail

// coCreateProtocolTail 是两种共创模式共用的输出协议尾部（<ready> / <suggestions> + 输出规范）。
// 两模式只在开场语境与 <draft> 语义上不同，协议完全一致。
const coCreateProtocolTail = `
<ready>true|false</ready>

<suggestions>
1-3 条"用户接下来可能想说的话"，每行一条以 "- " 开头。这是用户卡壳时的引导，
按数字键填入输入框，用户可再编辑后发送。

要求：
- 站在用户口吻，像用户对你说的话，不要写成助手反问。
- 每条不超过 25 字，多样化句式，避免千篇一律。
- 给倾向 / 选择 / 补充意图，不要一句话替用户写完整设定。
</suggestions>

输出规范：
- 必须使用本提示在上文定义的 XML 标签并保持顺序，每个标签都必须完整开闭。
- 标签名只能小写英文，不要改写成 <REPLY> / <REWRITE> / <回复> 等任何变体。
- 标签外不要添加任何说明、思考或代码围栏。
- <draft> 内允许多行 Markdown，直接换行书写，不需要任何转义。
- <ready> 只写 true 或 false，不要写 true|false。只要当前 <draft> 已经可以直接交给创作引擎执行，或你没有必须继续追问的关键问题，就必须填 true；只有还缺少会阻塞执行的核心信息时才填 false。
- <ready>true</ready> 时 <suggestions> 可以为空（保留空标签 <suggestions></suggestions> 即可）。`

const coCreateCastJSONFieldContract = `JSON 字段必须严格使用以下白名单，不得添加 age、appearance、background 等未列出的字段：
- 顶层：version, mode, draft_revision, draft_hash, source_signature, adaptation_intent_hash, members, planned_relationships, source_dispositions。
- version 必须是 JSON 数字 1；mode 只能是字符串 normal（普通原创）或 adaptation（改编），禁止使用 original、creative、adapt 等别名；draft_revision 必须是 JSON 整数。
- member：character, importance, origin, mainline_function, source_character_ids, inclusion_rationale, no_core_relationships。
- character：id, name, aliases, role, gender, description, arc, traits, tier, faction, goal, motivation, conflict, voice, constraints, notes。gender 必须为 male、female、nonbinary 或 unspecified；年龄、外貌、经历等信息必须写入 description 或 notes，不得自创字段。
- aliases、traits、constraints、source_character_ids、tags、target_character_ids 都必须是 JSON 字符串数组，哪怕只有一项也不得写成单个字符串；no_core_relationships 必须是 JSON 布尔值。
- planned_relationship：id, source_character_id, target_character_id, type, label, direction, status, description, since, tags, constraints。
- source_disposition：source_character_id, action, target_character_ids, rationale。
所有字符串字段必须使用 JSON 字符串，不得使用 null；没有内容的可选字段应省略或使用空字符串/空数组。
关系 type 只能是 ally/rival/family/romantic/mentor/professional/other；direction 只能是 directed/bidirectional/undirected；status 只能是 planned/active/strained/broken/resolved。`

func coCreateSystemPromptWithSimulation(st *store.Store, mode string) string {
	return appendSimulationCoCreatePrompt(coCreateSystemPrompt, st, mode)
}

func appendSimulationCoCreatePrompt(base string, st *store.Store, mode string) string {
	if mode != bootstrap.SimulationModeReinforced || st == nil || st.Simulation == nil {
		return base
	}
	contract, profile, err := st.EnsureSimulationContract(mode)
	if err != nil {
		return base + "\n\n## 仿写方向状态\n- effective_mode=normal\n- status=inactive\n- reason=profile_or_contract_invalid\n"
	}
	payload, err := json.MarshalIndent(simulationCoCreatePayloadFromContract(contract, profile), "", "  ")
	if err != nil {
		return base
	}
	return base + "\n\n---\n## 仿写方向（结构化契约候选）\n" +
		"以下 JSON 只含 portable v2 抽象 feature，且由与正式创作相同的 policy/contract 派生。status 不是 active 时不得声称强化仿写已生效。\n\n" +
		"```json\n" + string(payload) + "\n```\n\n" +
		"规则：\n" +
		"- <draft> 保留 `## 仿写方向` 作为用户可读摘要，但只能概括上述 feature ID，不得成为独立事实源。\n" +
		"- 用户要求、creative brief、已确认 foundation、章节合同和当前 POV 始终优先；冲突 feature 必须排除或降级。\n" +
		"- 禁止复制来源句子、人物、地名、专有设定或固定桥段；不得索取 raw、source_reports、本地路径、安全索引或标志短语。\n"
}

type simulationCoCreatePromptPayload struct {
	EffectiveMode string                      `json:"effective_mode"`
	Status        string                      `json:"status"`
	Reasons       []string                    `json:"reasons,omitempty"`
	Revision      int64                       `json:"contract_revision,omitempty"`
	ProfileDigest string                      `json:"profile_digest,omitempty"`
	Must          []simulationCoCreateFeature `json:"must,omitempty"`
	Should        []simulationCoCreateFeature `json:"should,omitempty"`
	Avoid         []simulationCoCreateFeature `json:"avoid,omitempty"`
	Safety        string                      `json:"safety_boundary"`
}

type simulationCoCreateFeature struct {
	ID             string   `json:"id"`
	Dimension      string   `json:"dimension"`
	Statement      string   `json:"statement"`
	Scopes         []string `json:"scopes,omitempty"`
	Classification string   `json:"classification"`
}

func simulationCoCreatePayloadFromContract(contract *domain.SimulationContract, profile *domain.SimulationProfileV2) simulationCoCreatePromptPayload {
	payload := simulationCoCreatePromptPayload{
		EffectiveMode: domain.SimulationModeNormal,
		Status:        domain.SimulationContractInactive,
		Reasons:       []string{"contract_missing"},
		Safety:        "portable_features_only",
	}
	if contract == nil {
		return payload
	}
	payload.EffectiveMode = contract.EffectiveMode
	payload.Status = contract.Status
	payload.Reasons = append([]string(nil), contract.Reasons...)
	payload.Revision = contract.Revision
	payload.ProfileDigest = contract.ProfileDigest
	if profile == nil || contract.Status == domain.SimulationContractInactive {
		return payload
	}
	view := contract.View(domain.SimulationRoleArchitect, "planning")
	if view == nil {
		payload.Reasons = append(payload.Reasons, "planning_view_unavailable")
		return payload
	}
	features := make(map[string]domain.SimulationFeature, len(profile.Features))
	for _, feature := range profile.Features {
		features[feature.ID] = feature
	}
	payload.Must = resolveSimulationCoCreateFeatures(view.Must, features)
	payload.Should = resolveSimulationCoCreateFeatures(view.Should, features)
	payload.Avoid = resolveSimulationCoCreateFeatures(view.Avoid, features)
	return payload
}

func resolveSimulationCoCreateFeatures(ids []string, features map[string]domain.SimulationFeature) []simulationCoCreateFeature {
	resolved := make([]simulationCoCreateFeature, 0, len(ids))
	for _, id := range ids {
		feature, ok := features[id]
		if !ok || strings.HasPrefix(feature.Dimension, "lexicon.signature_phrases") ||
			strings.HasPrefix(feature.Dimension, "lexicon.common_words") ||
			strings.HasPrefix(feature.Dimension, "lexicon.scene_words") {
			continue
		}
		resolved = append(resolved, simulationCoCreateFeature{
			ID: feature.ID, Dimension: feature.Dimension, Statement: feature.Statement,
			Scopes: append([]string(nil), feature.Scopes...), Classification: feature.Classification,
		})
	}
	return resolved
}

// CoCreateProgressKind 标识流式回调的内容类型。
const (
	CoCreateProgressThinking = "thinking"
	CoCreateProgressReply    = "reply"
)

const (
	coCreateMaxAttempts              = retrypolicy.MaxAttempts
	coCreateMaxStructureRepairs      = 2
	coCreateMaxTokens                = 2048
	coCreateSuggestionJudgeMaxTokens = 256
	coCreateModelRole                = "architect"
)

var coCreateRetrySleep = sleepBeforeCoCreateRetry

type coCreateModelIdentity struct {
	Provider string
	Model    string
}

func newCoCreateModelIdentity(model agentcore.ChatModel) coCreateModelIdentity {
	var identity coCreateModelIdentity
	if model == nil {
		return identity
	}
	if provider, ok := model.(interface{ ProviderName() string }); ok {
		identity.Provider = strings.TrimSpace(provider.ProviderName())
	}
	identity.Model = strings.TrimSpace(bootstrap.ModelName(model))
	return identity
}

func (i coCreateModelIdentity) label() string {
	switch {
	case i.Provider != "" && i.Model != "":
		return i.Provider + "/" + i.Model
	case i.Model != "":
		return i.Model
	case i.Provider != "":
		return i.Provider
	default:
		return ""
	}
}

func (i coCreateModelIdentity) wrapError(err error) error {
	if err == nil {
		return nil
	}
	if label := i.label(); label != "" {
		return coCreateSelectedModelError{label: label, err: err}
	}
	return err
}

type coCreateSelectedModelError struct {
	label string
	err   error
}

func (e coCreateSelectedModelError) Error() string {
	if e.err == nil {
		return "selected model " + e.label + ": unknown error"
	}
	return "selected model " + e.label + ": " + retrypolicy.SanitizeProviderError(e.err)
}

func (e coCreateSelectedModelError) Unwrap() error {
	return e.err
}

// 四段式 XML 标签输出。XML 风格比方括号 marker 更鲁棒——Claude/GPT 训练数据里
// 大量 <thinking>...</thinking> 这类格式，模型几乎不会把 <reply> 改写成 <REWRITE>
// 或其他变体；闭合标签也让流式中段截断更精确（不依赖找下一个 marker 来断尾）。
const (
	tagReply       = "reply"
	tagDraft       = "draft"
	tagCast        = "cast"
	tagReady       = "ready"
	tagSuggestions = "suggestions"
)

func coCreateStream(ctx context.Context, models *bootstrap.ModelSet, sessions *store.SessionStore, timeout time.Duration, maxTokens int, sysPrompt string, history []CoCreateMessage, onProgress func(kind, text string), diagnosticStores ...*store.Store) (reply CoCreateReply, err error) {
	if len(history) == 0 {
		return CoCreateReply{}, fmt.Errorf("cocreate history is empty")
	}
	if timeout <= 0 {
		timeout = time.Duration(bootstrap.DefaultCoCreateTimeoutSeconds) * time.Second
	}
	if maxTokens <= 0 {
		maxTokens = coCreateMaxTokens
	}

	model := models.ForStageWithFailover(bootstrap.StageCoCreate, nil)
	modelIdentity := newCoCreateModelIdentity(model)
	compiledSystem := globalprompt.Apply(sysPrompt)
	msgs := compileCoCreateMessages(compiledSystem, history)

	var raw, thinking strings.Builder
	var attempts int
	var structureRepairs int
	var retryErrors []string
	var stopReason agentcore.StopReason
	var diagnosticStore *store.Store
	if len(diagnosticStores) > 0 {
		diagnosticStore = diagnosticStores[0]
	}
	// 排查 "cocreate empty response" 等偶发问题需要看到模型实际返回什么。
	// 每轮全程落盘到 <output>/meta/sessions/cocreate.jsonl，与正式创作的 session 日志同位。
	start := time.Now()
	defer func() {
		if sessions == nil {
			return
		}
		_ = sessions.LogCoCreate(coCreateLogEntry{
			Time:             time.Now(),
			DurationMS:       time.Since(start).Milliseconds(),
			TimeoutSeconds:   int(timeout.Seconds()),
			MaxTokens:        maxTokens,
			ModelRole:        coCreateModelRole,
			SelectedProvider: modelIdentity.Provider,
			SelectedModel:    modelIdentity.Model,
			InputHistory:     coCreateLogHistoryMetadata(history),
			Attempts:         attempts,
			RetryErrors:      retryErrors,
			RawResponse:      coCreateLogTextMetadata("response", raw.String()),
			RawLen:           len([]rune(raw.String())),
			Thinking:         coCreateLogTextMetadata("thinking", thinking.String()),
			ParsedReply:      coCreateLogTextMetadata("reply", reply.Message),
			ParsedDraft:      coCreateLogTextMetadata("draft", reply.Prompt),
			ParsedReady:      reply.Ready,
			ParsedSugs:       reply.Suggestions,
			StopReason:       string(stopReason),
			Error:            errString(err),
		})
	}()

	var streamCh <-chan agentcore.StreamEvent
	var streamed, done bool
	var attemptCtx context.Context
	var cancelAttempt context.CancelFunc
	cancelCurrentAttempt := func() {
		if cancelAttempt != nil {
			cancelAttempt()
			cancelAttempt = nil
		}
	}
	defer cancelCurrentAttempt()

retry:
	cancelCurrentAttempt()
	attempts++
	raw.Reset()
	thinking.Reset()
	stopReason = ""
	streamed = false
	done = false
	diagnosticPayload, _ := json.Marshal(msgs[1:])
	recorder, budgetErr := modeldiag.Begin(modeldiag.Request{Store: diagnosticStore, Task: "cocreate_stream", Batch: attempts, System: compiledSystem, User: diagnosticPayload, InputLimitBytes: manuscriptCompiledRequestBudgetBytes, OutputLimitTokens: maxTokens, SelectorCounts: map[string]int{"messages": len(msgs)}})
	if budgetErr != nil {
		return CoCreateReply{}, budgetErr
	}
	var responseUsage *agentcore.Usage

	attemptCtx, cancelAttempt = context.WithTimeout(ctx, timeout)
	defer cancelAttempt()
	streamCh, err = model.GenerateStream(attemptCtx, msgs, nil, agentcore.WithMaxTokens(maxTokens))
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		if timeoutErr := coCreateAttemptTimeoutError(attemptCtx, timeout); timeoutErr != nil {
			cancelCurrentAttempt()
			return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(timeoutErr))
		}
		cancelCurrentAttempt()
		if ok, sleepErr := prepareCoCreateRetry(ctx, err, attempts, onProgress, &retryErrors); sleepErr != nil {
			return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
		} else if ok {
			goto retry
		}
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(err))
	}

	for ev := range streamCh {
		switch ev.Type {
		case agentcore.StreamEventThinkingDelta:
			thinking.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressThinking, thinking.String())
			}
		case agentcore.StreamEventTextDelta:
			streamed = true
			raw.WriteString(ev.Delta)
			if onProgress != nil {
				onProgress(CoCreateProgressReply, extractReplyPreview(raw.String()))
			}
		case agentcore.StreamEventDone:
			done = true
			stopReason = ev.StopReason
			if stopReason == "" {
				stopReason = ev.Message.StopReason
			}
			if !streamed {
				raw.WriteString(ev.Message.TextContent())
			}
			responseUsage = ev.Message.Usage
		case agentcore.StreamEventError:
			if ev.Err != nil {
				_ = recorder.Finish(modeldiag.StatusProviderError, raw.String(), responseUsage)
				if timeoutErr := coCreateAttemptTimeoutError(attemptCtx, timeout); timeoutErr != nil {
					cancelCurrentAttempt()
					return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(timeoutErr))
				}
				cancelCurrentAttempt()
				if ok, sleepErr := prepareCoCreateRetry(ctx, ev.Err, attempts, onProgress, &retryErrors); sleepErr != nil {
					return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
				} else if ok {
					goto retry
				}
				return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(ev.Err))
			}
			streamErr := fmt.Errorf("cocreate generate failed: %w", agentcore.ErrProviderNetwork)
			_ = recorder.Finish(modeldiag.StatusProviderError, raw.String(), responseUsage)
			cancelCurrentAttempt()
			if ok, sleepErr := prepareCoCreateRetry(ctx, streamErr, attempts, onProgress, &retryErrors); sleepErr != nil {
				return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
			} else if ok {
				goto retry
			}
			return CoCreateReply{}, modelIdentity.wrapError(streamErr)
		}
	}
	timeoutErr := coCreateAttemptTimeoutError(attemptCtx, timeout)
	cancelCurrentAttempt()
	if !done {
		_ = recorder.Finish(modeldiag.StatusProviderError, raw.String(), responseUsage)
		if timeoutErr != nil {
			return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(timeoutErr))
		}
		streamErr := fmt.Errorf("cocreate stream closed before done: %w", agentcore.ErrProviderNetwork)
		if ok, sleepErr := prepareCoCreateRetry(ctx, streamErr, attempts, onProgress, &retryErrors); sleepErr != nil {
			return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(sleepErr))
		} else if ok {
			goto retry
		}
		return CoCreateReply{}, fmt.Errorf("cocreate generate: %w", modelIdentity.wrapError(streamErr))
	}
	if stopReason == agentcore.StopReasonLength {
		_ = recorder.Finish(modeldiag.StatusTruncated, raw.String(), responseUsage)
		truncationErr := fmt.Errorf("cocreate response truncated: stop_reason=%s", stopReason)
		if prepareCoCreateTruncationRepair(ctx, truncationErr, raw.String(), coCreatePromptRequiresCast(sysPrompt), attempts, &structureRepairs, &retryErrors, &msgs, onProgress) {
			goto retry
		}
		return CoCreateReply{}, truncationErr
	}

	// Channel fallback：思考型模型（R1/GLM-Z1/QwQ 等）偶发把完整答案写进
	// reasoning_content 后没切回 final answer 通道，导致 raw 为空但 thinking 含
	// 完整四段。实测见 meta/sessions/cocreate.jsonl —— 直接拿 thinking 当 raw 解析，
	// 协议层已有降级处理（无 [REPLY] 标记时整段当 reply），救场后 UI 体验无差别。
	rawText := raw.String()
	if strings.TrimSpace(rawText) == "" {
		if t := strings.TrimSpace(thinking.String()); t != "" {
			rawText = t
		}
	}
	requireCast := coCreatePromptRequiresCast(sysPrompt)
	if err := rejectIncompleteCoCreateXML(rawText, requireCast); err != nil {
		_ = recorder.Finish(modeldiag.StatusInvalidSchema, rawText, responseUsage)
		if prepareCoCreateStructureRepair(ctx, err, rawText, requireCast, attempts, &structureRepairs, &retryErrors, &msgs, onProgress) {
			goto retry
		}
		return CoCreateReply{}, err
	}
	reply, err = parseCoCreateResponseForProtocol(rawText, requireCast)
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusDecodeError, rawText, responseUsage)
		if prepareCoCreateStructureRepair(ctx, err, rawText, requireCast, attempts, &structureRepairs, &retryErrors, &msgs, onProgress) {
			goto retry
		}
		return reply, err
	}
	if err == nil && len(reply.Suggestions) == 0 {
		reply.Suggestions = judgeCoCreateSuggestions(ctx, model, reply, diagnosticStore)
	}
	if diagnosticErr := recorder.Finish(modeldiag.StatusCompleted, rawText, responseUsage); diagnosticErr != nil {
		return CoCreateReply{}, diagnosticErr
	}
	return reply, err
}

func coCreateAttemptTimeoutError(ctx context.Context, timeout time.Duration) error {
	if ctx == nil || !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return fmt.Errorf(
		"co-create model call timed out after %s; increase cocreate_timeout_seconds before retrying: %w",
		timeout,
		context.DeadlineExceeded,
	)
}

// compileCoCreateMessages canonicalizes only the transient model view on every
// turn. The durable checkpoint keeps the complete conversation. Every user
// turn and visible assistant reply remains present, while superseded copies of
// cumulative drafts and separately persisted legacy casts are not resent.
func compileCoCreateMessages(compiledSystem string, history []CoCreateMessage) []agentcore.Message {
	return coCreateAgentMessages(compiledSystem, compactCoCreateHistory(history))
}

func coCreateAgentMessages(compiledSystem string, history []CoCreateMessage) []agentcore.Message {
	messages := []agentcore.Message{agentcore.SystemMsg(compiledSystem)}
	for _, item := range history {
		content := strings.TrimSpace(item.Content)
		if content == "" {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(item.Role)) {
		case "assistant":
			messages = append(messages, assistantMsg(content))
		default:
			messages = append(messages, agentcore.UserMsg(content))
		}
	}
	return messages
}

func coCreateCompiledRequestBytes(compiledSystem string, messages []agentcore.Message) int {
	if len(messages) <= 1 {
		return len(compiledSystem)
	}
	payload, err := json.Marshal(messages[1:])
	if err != nil {
		return len(compiledSystem)
	}
	return len(compiledSystem) + len(payload)
}

func compactCoCreateHistory(history []CoCreateMessage) []CoCreateMessage {
	latestDraftIndex := latestCoCreateDraftIndex(history)
	if latestDraftIndex < 0 {
		return append([]CoCreateMessage(nil), history...)
	}

	compacted := make([]CoCreateMessage, 0, len(history))
	for index, message := range history {
		content := strings.TrimSpace(message.Content)
		if content == "" {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") {
			content = compactCoCreateAssistantContext(content, index == latestDraftIndex)
			if content == "" {
				continue
			}
		}
		compacted = append(compacted, CoCreateMessage{Role: message.Role, Content: content})
	}
	return compacted
}

func latestCoCreateDraftIndex(history []CoCreateMessage) int {
	for index := len(history) - 1; index >= 0; index-- {
		message := history[index]
		if strings.EqualFold(strings.TrimSpace(message.Role), "assistant") &&
			strings.TrimSpace(extractTagContent(message.Content, tagDraft)) != "" {
			return index
		}
	}
	return -1
}

func compactCoCreateAssistantContext(content string, includeDraft bool) string {
	reply := strings.TrimSpace(extractTagContent(content, tagReply))
	if !includeDraft {
		if reply == "" {
			return strings.TrimSpace(content)
		}
		return "<reply>" + reply + "</reply>"
	}
	draft := strings.TrimSpace(extractTagContent(content, tagDraft))
	if draft == "" {
		return ""
	}
	ready := strings.ToLower(strings.TrimSpace(extractTagContent(content, tagReady)))
	if ready != "true" {
		ready = "false"
	}
	return strings.Join([]string{
		"<reply>" + reply + "</reply>",
		"<draft>",
		draft,
		"</draft>",
		"<ready>" + ready + "</ready>",
		"<suggestions></suggestions>",
	}, "\n")
}

func coCreatePromptRequiresCast(prompt string) bool {
	return strings.Contains(prompt, "\n<cast>\n")
}

func judgeCoCreateSuggestions(ctx context.Context, model agentcore.ChatModel, reply CoCreateReply, diagnosticStores ...*store.Store) []string {
	if model == nil || strings.TrimSpace(reply.Message) == "" {
		return nil
	}
	input := buildCoCreateSuggestionJudgeInput(reply.Message)
	var diagnosticStore *store.Store
	if len(diagnosticStores) > 0 {
		diagnosticStore = diagnosticStores[0]
	}
	recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: diagnosticStore, Task: "cocreate_suggestion_judge", System: coCreateSuggestionJudgePrompt, User: []byte(input), InputLimitBytes: manuscriptCompiledRequestBudgetBytes, OutputLimitTokens: coCreateSuggestionJudgeMaxTokens})
	if beginErr != nil {
		return nil
	}
	resp, err := model.Generate(ctx, []agentcore.Message{
		agentcore.SystemMsg(coCreateSuggestionJudgePrompt),
		agentcore.UserMsg(input),
	}, nil, agentcore.WithMaxTokens(coCreateSuggestionJudgeMaxTokens), agentcore.WithJSONMode())
	if err != nil {
		_ = recorder.Finish(modeldiag.StatusProviderError, "", nil)
		return nil
	}
	if resp == nil || strings.TrimSpace(resp.Message.TextContent()) == "" {
		_ = recorder.Finish(modeldiag.StatusEmptyResponse, "", nil)
		return nil
	}
	output := resp.Message.TextContent()
	suggestions := parseSuggestionJudgeResponse(output)
	_ = recorder.Finish(modeldiag.StatusCompleted, output, resp.Message.Usage)
	return suggestions
}

const coCreateSuggestionJudgePrompt = `你是小说共创 UI 的建议按钮判定器。你的任务是判断助手回复里是否真的包含适合显示为“用户下一句可以点击发送”的建议。

只输出 JSON，不要解释：
{"suggestions":["..."]}

严格规则：
- 只在助手明确给出用户可选择的下一步、倾向或补充意图时返回建议。
- 建议必须改写成用户口吻，像用户会对助手说的话；不要写成标题、名词短语或助手指令。
- 每条不超过 25 个中文字符，最多 3 条。
- 如果回复只是总结、规划说明、确认、泛泛询问“是否符合预期/是否需要调整”，返回空数组。
- 不要从剧情规划正文、卷标题、章节标题、主题名里硬抽按钮。`

func buildCoCreateSuggestionJudgeInput(reply string) string {
	return "助手回复：\n" + strings.TrimSpace(reply)
}

func prepareCoCreateRetry(ctx context.Context, err error, attempt int, onProgress func(kind, text string), retryErrors *[]string) (bool, error) {
	if !shouldRetryCoCreate(ctx, err, attempt) {
		return false, nil
	}
	if retryErrors != nil {
		*retryErrors = append(*retryErrors, err.Error())
	}
	clearCoCreateProgress(onProgress)
	if err := waitBeforeCoCreateRetry(ctx, attempt); err != nil {
		return false, err
	}
	return true, nil
}

func prepareCoCreateStructureRepair(
	ctx context.Context,
	err error,
	rawText string,
	requireCast bool,
	attempt int,
	structureRepairs *int,
	retryErrors *[]string,
	msgs *[]agentcore.Message,
	onProgress func(kind, text string),
) bool {
	return prepareCoCreateProtocolRepair(
		ctx, err, rawText, attempt, structureRepairs, retryErrors, msgs, onProgress,
		coCreateStructureRepairInstruction(err, requireCast),
	)
}

func prepareCoCreateTruncationRepair(
	ctx context.Context,
	err error,
	rawText string,
	requireCast bool,
	attempt int,
	structureRepairs *int,
	retryErrors *[]string,
	msgs *[]agentcore.Message,
	onProgress func(kind, text string),
) bool {
	return prepareCoCreateProtocolRepair(
		ctx, err, rawText, attempt, structureRepairs, retryErrors, msgs, onProgress,
		coCreateTruncationRepairInstruction(requireCast),
	)
}

func prepareCoCreateProtocolRepair(
	ctx context.Context,
	err error,
	rawText string,
	attempt int,
	structureRepairs *int,
	retryErrors *[]string,
	msgs *[]agentcore.Message,
	onProgress func(kind, text string),
	instruction string,
) bool {
	if ctx.Err() != nil || err == nil || structureRepairs == nil || msgs == nil ||
		attempt >= coCreateMaxAttempts || *structureRepairs >= coCreateMaxStructureRepairs {
		return false
	}
	*structureRepairs++
	if retryErrors != nil {
		*retryErrors = append(*retryErrors, err.Error())
	}
	clearCoCreateProgress(onProgress)
	*msgs = append(*msgs,
		assistantMsg(clipCoCreatePromptText(rawText, 16000)),
		agentcore.UserMsg(instruction),
	)
	return true
}

func coCreateStructureRepairInstruction(err error, requireCast bool) string {
	protocol := "请重新输出完整的 <reply><draft><ready><suggestions> 四段协议；不要只输出修补片段。"
	if requireCast {
		protocol = "请重新输出完整的 <reply><draft><cast><ready><suggestions> 五段协议；不要只输出修补后的 JSON。\n" + coCreateCastJSONFieldContract
	}
	return "上一条响应未通过机器协议校验：" + clipCoCreatePromptText(err.Error(), 600) + "\n" +
		protocol + "\n保留已经形成的创作结论，只修复协议和字段；标签外不要输出任何内容。"
}

func coCreateTruncationRepairInstruction(requireCast bool) string {
	protocol := "请重新输出完整的 <reply><draft><ready><suggestions> 四段协议。"
	if requireCast {
		protocol = "请重新输出完整的 <reply><draft><cast><ready><suggestions> 五段协议。\n" + coCreateCastJSONFieldContract
	}
	return "上一条响应因超过输出上限而被截断。现在进入紧凑修复批次：\n" + protocol +
		"\n保留已经形成的全部创作结论，但压缩表达；reply 最多 120 字，draft 只保留冻结结论和未决项，角色字段每项用一个短句，避免解释、重复和铺陈。标签外不要输出任何内容。"
}

func shouldRetryCoCreate(ctx context.Context, err error, attempt int) bool {
	if attempt >= coCreateMaxAttempts {
		return false
	}
	if ctx.Err() != nil {
		return false
	}
	return agentcore.IsFailoverEligible(err) || retrypolicy.IsProviderGatewayError(err)
}

func clearCoCreateProgress(onProgress func(kind, text string)) {
	if onProgress == nil {
		return
	}
	onProgress(CoCreateProgressThinking, "")
	onProgress(CoCreateProgressReply, "")
}

func waitBeforeCoCreateRetry(ctx context.Context, attempt int) error {
	return coCreateRetrySleep(ctx, coCreateRetryDelay(attempt))
}

func coCreateRetryDelay(attempt int) time.Duration {
	return retrypolicy.Delay(attempt)
}

func sleepBeforeCoCreateRetry(ctx context.Context, delay time.Duration) error {
	return retrypolicy.Wait(ctx, delay)
}

func adaptSystemPrompt(st *store.Store) string {
	if snapshot := adaptationBriefingSnapshot(st); strings.TrimSpace(snapshot) != "" {
		return adaptCoCreateSystemPrompt + "\n\n## Co-create briefing\n\n" + snapshot
	}
	return adaptCoCreateSystemPrompt + "\n\n## All-book adaptation dossier\n\n" + adaptationDossierSnapshot(st)
}

func adaptationBriefingSnapshot(st *store.Store) string {
	if st == nil || st.Adaptation == nil {
		return ""
	}
	manifest, manifestErr := st.Adaptation.LoadSourceManifest()
	dossier, dossierErr := st.Adaptation.LoadCoCreateDossier()
	intent, intentErr := st.Adaptation.LoadCoCreateIntent()
	briefing, briefingErr := st.Adaptation.LoadCoCreateBriefing()
	if manifestErr != nil || dossierErr != nil || intentErr != nil || briefingErr != nil || manifest == nil || dossier == nil || intent == nil || briefing == nil {
		return ""
	}
	if !store.CoCreateBriefingMatches(*briefing, *manifest, *dossier, adapt.CoCreateBriefingPromptVersion, adapt.CoCreateDossierPromptVersion, intent.IntentHash) {
		return ""
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "- source_chapters: %d\n", manifest.ChapterCount)
	fmt.Fprintf(&sb, "- dossier_batches: %d\n", len(dossier.Batches))
	fmt.Fprintf(&sb, "- trigger: %s\n", briefing.TriggerReason)
	if strings.TrimSpace(intent.RawRequest) != "" {
		fmt.Fprintf(&sb, "- user_intent: %s\n", clipCoCreatePromptText(intent.RawRequest, 800))
	}
	writeDossierStrings(&sb, "### Inferred Intent Goals", intent.Goals, 24)
	writeDossierStrings(&sb, "### Relationship Rules", intent.RelationshipRules, 24)
	writeDossierStrings(&sb, "### Preserve Rules", intent.PreserveRules, 24)
	writeDossierStrings(&sb, "### Confirmed Source Facts", briefing.ConfirmedFacts, 24)
	writeBriefingPromptRisks(&sb, "### Intent-Relevant Risks", briefing.IntentRelevantRisks, 16)
	writeDossierStrings(&sb, "### Adaptation Suggestions", briefing.AdaptationSuggestions, 16)
	return sb.String()
}

func adaptationDossierSnapshot(st *store.Store) string {
	if st == nil || st.Adaptation == nil {
		return "尚未加载全书改编资料包。"
	}
	manifest, manifestErr := st.Adaptation.LoadSourceManifest()
	dossier, dossierErr := st.Adaptation.LoadCoCreateDossier()
	if manifestErr != nil || dossierErr != nil {
		return "全书改编资料包读取失败，请提醒用户重新点击原文分析。"
	}
	if manifest == nil {
		return "尚未加载原书快照。"
	}
	if dossier == nil || !store.CoCreateDossierMatchesManifest(*dossier, *manifest, adapt.CoCreateDossierPromptVersion, adapt.CoCreateDossierBatchSize, adapt.CoCreateDossierBatchRuneLimit) {
		return "全书改编资料包缺失或已过期，请提醒用户重新点击原文分析后再共创。"
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "- 来源：%s\n", manifest.SourcePath)
	fmt.Fprintf(&sb, "- 原文章节数：%d\n", manifest.ChapterCount)
	if dossier.BatchRuneLimit > 0 {
		fmt.Fprintf(&sb, "- 资料包批次：%d 批，每批最多 %d 章，过长时约 %d 字符拆批\n", len(dossier.Batches), dossier.BatchSize, dossier.BatchRuneLimit)
	} else {
		fmt.Fprintf(&sb, "- 资料包批次：%d 批，每批约 %d 章\n", len(dossier.Batches), dossier.BatchSize)
	}
	if strings.TrimSpace(dossier.Overview) != "" {
		fmt.Fprintf(&sb, "- 覆盖说明：%s\n", dossier.Overview)
	}
	writeDossierStrings(&sb, "### 全书主线与因果锚点", dossier.Mainline, 80)
	writeDossierStrings(&sb, "### 剧情线程", dossier.PlotThreads, 80)
	writeDossierStrings(&sb, "### 人物弧光", dossier.CharacterArcs, 80)
	writeDossierStrings(&sb, "### 世界与连续性约束", dossier.WorldConstraints, 80)
	writeDossierSignals(&sb, "### 关系线信号", dossier.RelationshipMap, 80)
	writeDossierSignals(&sb, "### 女主相关信号", dossier.HeroineSignals, 80)
	writeDossierRisks(&sb, "### 女配暧昧/后宫风险", dossier.AmbiguityRisks, 80)
	writeDossierSignals(&sb, "### 情侣/暧昧进展节点", dossier.CoupleMilestones, 80)
	return sb.String()
}

func writeBriefingPromptRisks(sb *strings.Builder, title string, values []domain.AdaptationBriefingRisk, max int) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(title)
	sb.WriteString("\n")
	for i, value := range values {
		if max > 0 && i >= max {
			fmt.Fprintf(sb, "- %d additional risks are stored in the briefing for later planning.\n", len(values)-max)
			break
		}
		fmt.Fprintf(sb, "- %s%s: %s", dossierChapterLabel(value.Chapters), dossierCharactersLabel(value.Characters), value.Risk)
		if strings.TrimSpace(value.Evidence) != "" {
			fmt.Fprintf(sb, " (evidence: %s)", value.Evidence)
		}
		if strings.TrimSpace(value.Suggestion) != "" {
			fmt.Fprintf(sb, " (suggestion: %s)", value.Suggestion)
		}
		sb.WriteString("\n")
	}
}

func clipCoCreatePromptText(text string, maxRunes int) string {
	text = strings.TrimSpace(text)
	if maxRunes <= 0 {
		return text
	}
	runes := []rune(text)
	if len(runes) <= maxRunes {
		return text
	}
	return string(runes[:maxRunes]) + "..."
}

func writeDossierStrings(sb *strings.Builder, title string, values []string, max int) {
	values = trimDossierStrings(values, max)
	if len(values) == 0 {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(title)
	sb.WriteString("\n")
	for _, value := range values {
		fmt.Fprintf(sb, "- %s\n", value)
	}
}

func writeDossierSignals(sb *strings.Builder, title string, values []domain.AdaptationRelationshipSignal, max int) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(title)
	sb.WriteString("\n")
	for i, value := range values {
		if max > 0 && i >= max {
			fmt.Fprintf(sb, "- 另有 %d 条同类信号已存入资料包，后续规划阶段可继续读取。\n", len(values)-max)
			break
		}
		fmt.Fprintf(sb, "- %s%s：%s", dossierChapterLabel(value.Chapters), dossierCharactersLabel(value.Characters), value.Summary)
		if strings.TrimSpace(value.Evidence) != "" {
			fmt.Fprintf(sb, "（证据：%s）", value.Evidence)
		}
		sb.WriteString("\n")
	}
}

func writeDossierRisks(sb *strings.Builder, title string, values []domain.AdaptationRelationshipRisk, max int) {
	if len(values) == 0 {
		return
	}
	sb.WriteString("\n")
	sb.WriteString(title)
	sb.WriteString("\n")
	for i, value := range values {
		if max > 0 && i >= max {
			fmt.Fprintf(sb, "- 另有 %d 条风险已存入资料包，后续规划阶段可继续读取。\n", len(values)-max)
			break
		}
		fmt.Fprintf(sb, "- %s%s：%s", dossierChapterLabel(value.Chapters), dossierCharactersLabel(value.Characters), value.Risk)
		if strings.TrimSpace(value.Evidence) != "" {
			fmt.Fprintf(sb, "（证据：%s）", value.Evidence)
		}
		sb.WriteString("\n")
	}
}

func trimDossierStrings(values []string, max int) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out
}

func dossierChapterLabel(chapters []int) string {
	if len(chapters) == 0 {
		return ""
	}
	if len(chapters) == 1 {
		return fmt.Sprintf("第 %d 章", chapters[0])
	}
	return fmt.Sprintf("第 %d-%d 章", chapters[0], chapters[len(chapters)-1])
}

func dossierCharactersLabel(characters []string) string {
	characters = trimDossierStrings(characters, 6)
	if len(characters) == 0 {
		return ""
	}
	if label := strings.Join(characters, "/"); label != "" {
		return " " + label
	}
	return ""
}

// coCreateLogEntry 是写入 meta/sessions/cocreate.jsonl 的一行结构。
// 字段命名贴近 jsonl 直查习惯（snake_case），方便 jq 过滤。
type coCreateLogEntry struct {
	Time             time.Time         `json:"time"`
	DurationMS       int64             `json:"duration_ms"`
	TimeoutSeconds   int               `json:"timeout_seconds,omitempty"`
	MaxTokens        int               `json:"max_tokens,omitempty"`
	ModelRole        string            `json:"model_role,omitempty"`
	SelectedProvider string            `json:"selected_provider,omitempty"`
	SelectedModel    string            `json:"selected_model,omitempty"`
	InputHistory     []CoCreateMessage `json:"input_history"`
	Attempts         int               `json:"attempts,omitempty"`
	RetryErrors      []string          `json:"retry_errors,omitempty"`
	RawResponse      string            `json:"raw_response"`
	RawLen           int               `json:"raw_len"`
	Thinking         string            `json:"thinking,omitempty"`
	ParsedReply      string            `json:"parsed_reply"`
	ParsedDraft      string            `json:"parsed_draft"`
	ParsedReady      bool              `json:"parsed_ready"`
	ParsedSugs       []string          `json:"parsed_sugs,omitempty"`
	StopReason       string            `json:"stop_reason,omitempty"`
	Error            string            `json:"error,omitempty"`
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func coCreateLogHistoryMetadata(history []CoCreateMessage) []CoCreateMessage {
	out := make([]CoCreateMessage, 0, len(history))
	for _, message := range history {
		out = append(out, CoCreateMessage{Role: strings.TrimSpace(message.Role), Content: coCreateLogTextMetadata("message", message.Content)})
	}
	return out
}

func coCreateLogTextMetadata(kind, text string) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return fmt.Sprintf("[%s redacted; runes=%d]", kind, len([]rune(text)))
}

func assistantMsg(text string) agentcore.Message {
	return agentcore.Message{
		Role:      agentcore.RoleAssistant,
		Content:   []agentcore.ContentBlock{agentcore.TextBlock(text)},
		Timestamp: time.Now(),
	}
}

// parseCoCreateResponse 解析 XML 标签输出。模型若没遵守协议（直接说自然语言），
// 整段作为 reply 显示，draft 留空让 session 保留上一轮。
func parseCoCreateResponse(raw string) (CoCreateReply, error) {
	return parseCoCreateResponseForProtocol(raw, false)
}

// LegacyCoCreateCast parses only a complete legacy five-section response. It
// exists for checkpoint migration; the returned cast remains an unreviewed
// Character Agent input seed.
func LegacyCoCreateCast(raw string) *domain.CoreCastContract {
	reply, err := parseCoCreateResponseForProtocol(raw, false)
	if err != nil || reply.CoreCast == nil {
		return nil
	}
	value := *reply.CoreCast
	return &value
}

const coCreateCastMaxBytes = 128 * 1024

func parseCoCreateResponseForProtocol(raw string, requireCast bool) (CoCreateReply, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return CoCreateReply{}, fmt.Errorf("cocreate empty response")
	}

	tags := []string{tagReply, tagDraft, tagReady, tagSuggestions}
	hasLegacyCast := !requireCast && strings.Contains(raw, "<"+tagCast+">")
	if requireCast || hasLegacyCast {
		tags = []string{tagReply, tagDraft, tagCast, tagReady, tagSuggestions}
	}
	sections, err := strictCoCreateSections(raw, tags)
	if err != nil {
		return CoCreateReply{}, err
	}
	readyText := strings.ToLower(strings.TrimSpace(sections[tagReady]))
	if readyText != "true" && readyText != "false" {
		return CoCreateReply{}, fmt.Errorf("cocreate response invalid: <ready> must be true or false")
	}
	parsed := CoCreateReply{
		Message:     sections[tagReply],
		Prompt:      sections[tagDraft],
		Ready:       readyText == "true",
		Suggestions: parseSuggestions(sections[tagSuggestions]),
		Raw:         raw,
	}
	if !requireCast && !hasLegacyCast {
		return parsed, nil
	}
	castRaw := sections[tagCast]
	if castRaw == "" {
		return parsed, fmt.Errorf("cocreate cast missing")
	}
	if len([]byte(castRaw)) > coCreateCastMaxBytes {
		return parsed, fmt.Errorf("cocreate cast exceeds %d bytes", coCreateCastMaxBytes)
	}
	decoder := json.NewDecoder(strings.NewReader(castRaw))
	decoder.DisallowUnknownFields()
	var cast domain.CoreCastContract
	if err := decoder.Decode(&cast); err != nil {
		return parsed, fmt.Errorf("cocreate cast invalid json: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return parsed, fmt.Errorf("cocreate cast contains trailing json content")
	}
	cast.Mode = normalizeGeneratedCoreCastMode(cast.Mode)
	normalized, err := domain.NormalizeCoreCastContract(cast)
	if err != nil {
		return parsed, fmt.Errorf("cocreate cast invalid contract: %w", err)
	}
	parsed.CoreCast = &normalized
	return parsed, nil
}

func normalizeGeneratedCoreCastMode(mode domain.CoreCastMode) domain.CoreCastMode {
	switch strings.ToLower(strings.TrimSpace(string(mode))) {
	case "original", "creative":
		return domain.CoreCastModeNormal
	case "adapt":
		return domain.CoreCastModeAdaptation
	default:
		return mode
	}
}

func rejectIncompleteCoCreateXML(raw string, requireCast ...bool) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	tags := []string{tagReply, tagDraft, tagReady, tagSuggestions}
	if (len(requireCast) > 0 && requireCast[0]) ||
		(len(requireCast) == 0 || !requireCast[0]) && strings.Contains(raw, "<"+tagCast+">") {
		tags = []string{tagReply, tagDraft, tagCast, tagReady, tagSuggestions}
	}
	_, err := strictCoCreateSections(raw, tags)
	return err
}

func strictCoCreateSections(raw string, tags []string) (map[string]string, error) {
	remaining := strings.TrimSpace(raw)
	sections := make(map[string]string, len(tags))
	allTags := []string{tagReply, tagDraft, tagCast, tagReady, tagSuggestions}
	for _, tag := range tags {
		open := "<" + tag + ">"
		closeTag := "</" + tag + ">"
		if !strings.HasPrefix(remaining, open) {
			return nil, fmt.Errorf("cocreate response invalid: expected %s in protocol order", open)
		}
		remaining = remaining[len(open):]
		closeIndex := strings.Index(remaining, closeTag)
		if closeIndex < 0 {
			return nil, fmt.Errorf("cocreate response incomplete: missing %s", closeTag)
		}
		content := remaining[:closeIndex]
		for _, nested := range allTags {
			if strings.Contains(content, "<"+nested+">") || strings.Contains(content, "</"+nested+">") {
				return nil, fmt.Errorf("cocreate response invalid: nested or duplicate <%s> tag", nested)
			}
		}
		sections[tag] = strings.TrimSpace(content)
		remaining = strings.TrimSpace(remaining[closeIndex+len(closeTag):])
	}
	if remaining != "" {
		return nil, fmt.Errorf("cocreate response invalid: content outside protocol sections")
	}
	return sections, nil
}

// splitCoCreateMarkers 按四个 XML 标签切分文本。
// 标签可能缺失（流式中段或模型遗漏），缺失部分对应字段为空 / false / nil。
// 缺失闭标签时，extractTagContent 会取到字符串末尾，仍尽力解析。
func splitCoCreateMarkers(s string) (reply, draft string, ready bool, suggestions []string) {
	reply = extractTagContent(s, tagReply)
	draft = extractTagContent(s, tagDraft)
	readyStr := strings.ToLower(extractTagContent(s, tagReady))
	ready = readyStr == "true" || readyStr == "yes"
	suggestions = parseSuggestions(extractTagContent(s, tagSuggestions))
	return
}

// extractTagContent 从 s 中抠出 <tag>...</tag> 之间的文本。
// 三种偶发故障场景兜底，避免直接走降级丢字段：
//  1. 有开无闭（流式中段）→ 切到下一个已知开标签前
//  2. 无开有闭（模型 typo，如 <suggestions> 写成 <uggestions>）→ 从最近一个已知
//     完整闭合标签的结束位置开始，到 </tag> 之前
//  3. reply 完全无开标签（模型直接以自然语言开篇，末尾贴 </reply>）→ 从开头到 </reply>
func extractTagContent(s, tag string) string {
	open := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	oIdx := strings.Index(s, open)
	if oIdx >= 0 {
		rest := s[oIdx+len(open):]
		if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
			return strings.TrimSpace(rest[:cIdx])
		}
		// 有开无闭 → 切到下一个已知开标签前
		for _, other := range []string{"<reply>", "<draft>", "<cast>", "<ready>", "<suggestions>"} {
			if other == open {
				continue
			}
			if idx := strings.Index(rest, other); idx >= 0 {
				rest = rest[:idx]
			}
		}
		return strings.TrimSpace(rest)
	}

	// 无开有闭 → 从最近一个已知完整闭合标签的结束位置开始，到 </tag>。
	if cIdx := strings.Index(s, closeTag); cIdx >= 0 {
		prefix := s[:cIdx]
		start := 0
		for _, t := range []string{"</reply>", "</draft>", "</ready>", "</suggestions>"} {
			if t == closeTag {
				continue
			}
			if i := strings.LastIndex(prefix, t); i >= 0 {
				if end := i + len(t); end > start {
					start = end
				}
			}
		}
		return strings.TrimSpace(prefix[start:])
	}
	return ""
}

// parseSuggestions 把 <suggestions> 段每行抠出来，去掉 "- " / "* " / "1. " 等列表前缀。
// 最多保留 3 条；空行、过短（<2 字）、整行像 XML 标签的（typo 开标签兜底残留，
// 例如 <uggestions>）忽略。
func parseSuggestions(text string) []string {
	if text == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// 整行像 XML 标签 → 跳过（防 typo 开标签污染）
		if strings.HasPrefix(line, "<") && strings.HasSuffix(line, ">") {
			continue
		}
		// 剥列表前缀
		switch {
		case strings.HasPrefix(line, "- "):
			line = strings.TrimSpace(line[2:])
		case strings.HasPrefix(line, "* "):
			line = strings.TrimSpace(line[2:])
		case isOrderedSuggestion(line):
			line = stripOrderedPrefix(line)
		}
		if len([]rune(line)) < 2 {
			continue
		}
		out = append(out, line)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

type coCreateSuggestionJudgeResponse struct {
	Suggestions []string `json:"suggestions"`
}

func parseSuggestionJudgeResponse(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	raw = extractJSONObject(raw)
	if raw == "" {
		return nil
	}
	var response coCreateSuggestionJudgeResponse
	if err := json.Unmarshal([]byte(raw), &response); err != nil {
		return nil
	}
	return normalizeCoCreateSuggestionTexts(response.Suggestions, 25)
}

func normalizeCoCreateSuggestionTexts(values []string, maxRunes int) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		text := cleanCoCreateSuggestionText(value)
		runes := len([]rune(text))
		if runes < 2 || (maxRunes > 0 && runes > maxRunes) || seen[text] {
			continue
		}
		seen[text] = true
		out = append(out, text)
		if len(out) >= 3 {
			break
		}
	}
	return out
}

func cleanCoCreateSuggestionText(text string) string {
	text = strings.TrimSpace(text)
	text = strings.Trim(text, "`")
	switch {
	case strings.HasPrefix(text, "- "):
		text = strings.TrimSpace(text[2:])
	case strings.HasPrefix(text, "* "):
		text = strings.TrimSpace(text[2:])
	case isOrderedSuggestion(text):
		text = stripOrderedPrefix(text)
	}
	text = strings.TrimSpace(text)
	text = strings.Trim(text, " \t\r\n:：,，、。；;？?")
	return text
}

func extractJSONObject(text string) string {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		lines := strings.Split(text, "\n")
		if len(lines) >= 2 {
			if strings.HasPrefix(strings.TrimSpace(lines[0]), "```") {
				lines = lines[1:]
			}
			if len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "```") {
				lines = lines[:len(lines)-1]
			}
			text = strings.TrimSpace(strings.Join(lines, "\n"))
		}
	}
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end < start {
		return ""
	}
	return strings.TrimSpace(text[start : end+1])
}

// isOrderedSuggestion 判断行首是否形如 "1. " / "12. "（数字+点+空格）。
func isOrderedSuggestion(line string) bool {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	return i > 0 && i+1 < len(line) && line[i] == '.' && line[i+1] == ' '
}

func stripOrderedPrefix(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) {
		return line
	}
	return strings.TrimSpace(line[i+2:])
}

// extractReplyPreview 流式预览：raw 还在生长时给 UI 一段可显示的文本。
// 找到 <reply> 之后的内容，切到 </reply> 或下一个开标签 <draft> 之前。
// 模型半遵守（漏 <reply> 开标签）时，开头到 </reply> 或 <draft> 都算 reply。
func extractReplyPreview(raw string) string {
	trimmed := strings.TrimSpace(raw)
	open := "<" + tagReply + ">"
	closeTag := "</" + tagReply + ">"
	draftOpen := "<" + tagDraft + ">"

	rest := trimmed
	if rIdx := strings.Index(trimmed, open); rIdx >= 0 {
		rest = trimmed[rIdx+len(open):]
	}
	if cIdx := strings.Index(rest, closeTag); cIdx >= 0 {
		return strings.TrimSpace(rest[:cIdx])
	}
	if dIdx := strings.Index(rest, draftOpen); dIdx >= 0 {
		rest = rest[:dIdx]
	}
	return strings.TrimSpace(rest)
}
