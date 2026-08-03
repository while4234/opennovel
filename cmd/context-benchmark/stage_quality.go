package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

const qualityBenchmarkVersion = "stage-quality-v1"

//go:embed testdata/stage-quality-v1/fixtures.json
var qualityFixtureFS embed.FS

type qualityFixture struct {
	ID            string   `json:"id"`
	Title         string   `json:"title"`
	UserIntent    string   `json:"user_intent"`
	Source        string   `json:"source"`
	Premise       string   `json:"premise"`
	Characters    string   `json:"characters"`
	WorldRules    string   `json:"world_rules"`
	Outline       string   `json:"outline"`
	Draft         string   `json:"draft"`
	ExpectedFacts []string `json:"expected_facts"`
}

type qualityCase struct {
	ID              string   `json:"id"`
	Stage           string   `json:"stage"`
	FixtureID       string   `json:"fixture_id"`
	Title           string   `json:"title"`
	SystemPrompt    string   `json:"-"`
	TaskPrompt      string   `json:"-"`
	Context         string   `json:"-"`
	Rubric          []string `json:"rubric"`
	RequiredTerms   []string `json:"required_terms,omitempty"`
	Structured      bool     `json:"structured"`
	MinRunes        int      `json:"min_runes"`
	MaxRunes        int      `json:"max_runes"`
	MaxOutputTokens int      `json:"max_output_tokens"`
	Diagnostic      bool     `json:"diagnostic,omitempty"`
	EnforceLength   bool     `json:"enforce_length,omitempty"`
}

type qualityAttempt struct {
	Version         string               `json:"version"`
	CaseID          string               `json:"case_id"`
	Stage           string               `json:"stage"`
	Provider        string               `json:"provider"`
	Model           string               `json:"model"`
	ReasoningEffort string               `json:"reasoning_effort,omitempty"`
	Status          string               `json:"status"`
	StartedAt       time.Time            `json:"started_at"`
	FinishedAt      time.Time            `json:"finished_at"`
	DurationMillis  int64                `json:"duration_millis"`
	CallAttempts    int                  `json:"call_attempts"`
	Usage           *agentcore.Usage     `json:"usage,omitempty"`
	StopReason      agentcore.StopReason `json:"stop_reason,omitempty"`
	Response        string               `json:"response,omitempty"`
	Error           string               `json:"error,omitempty"`
	TransportErrors []string             `json:"transport_errors,omitempty"`
	HardScore       float64              `json:"hard_score"`
	HardIssues      []string             `json:"hard_issues,omitempty"`
}

type qualityJudgeDimension struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

type qualityJudgeScore struct {
	Candidate  string                  `json:"candidate"`
	Dimensions []qualityJudgeDimension `json:"dimensions"`
	Overall    float64                 `json:"overall"`
	Summary    string                  `json:"summary"`
}

type qualityJudgement struct {
	Scores     []qualityJudgeScore `json:"scores"`
	Preference string              `json:"preference"`
	Confidence float64             `json:"confidence"`
}

type qualityJudgeRecord struct {
	Version          string            `json:"version"`
	CaseID           string            `json:"case_id"`
	Judge            modelSpec         `json:"judge"`
	Status           string            `json:"status"`
	StartedAt        time.Time         `json:"started_at"`
	FinishedAt       time.Time         `json:"finished_at"`
	DurationMillis   int64             `json:"duration_millis"`
	CallAttempts     int               `json:"call_attempts"`
	AnonymousMapping map[string]string `json:"anonymous_mapping"`
	Responses        []string          `json:"responses,omitempty"`
	Judgement        *qualityJudgement `json:"judgement,omitempty"`
	Error            string            `json:"error,omitempty"`
}

type qualityStageSummary struct {
	Stage     string  `json:"stage"`
	Samples   int     `json:"samples"`
	HardMean  float64 `json:"hard_mean"`
	JudgeMean float64 `json:"judge_mean"`
	FinalMean float64 `json:"final_mean"`
	StdDev    float64 `json:"stddev"`
}

type qualityCandidateSummary struct {
	Candidate          string                     `json:"candidate"`
	Provider           string                     `json:"provider"`
	Model              string                     `json:"model"`
	ReasoningEffort    string                     `json:"reasoning_effort,omitempty"`
	OverallScore       float64                    `json:"overall_score"`
	NonCreativeScore   float64                    `json:"non_creative_score"`
	WritingScore       float64                    `json:"writing_score"`
	WriterToolingScore float64                    `json:"writer_tooling_score"`
	StageWins          int                        `json:"stage_wins"`
	Stages             []qualityStageSummary      `json:"stages"`
	WriterTooling      []qualityDiagnosticSummary `json:"writer_tooling"`
}

type qualityDiagnosticSummary struct {
	CaseID     string  `json:"case_id"`
	HardScore  float64 `json:"hard_score"`
	JudgeScore float64 `json:"judge_score"`
	FinalScore float64 `json:"final_score"`
}

type qualityComparison struct {
	LeftCandidate   string  `json:"left_candidate"`
	RightCandidate  string  `json:"right_candidate"`
	MeanPairedDelta float64 `json:"mean_paired_delta"`
	ConfidenceLow   float64 `json:"confidence_low"`
	ConfidenceHigh  float64 `json:"confidence_high"`
	WeakAdvantage   bool    `json:"weak_advantage"`
}

type qualitySummary struct {
	Version     string                    `json:"version"`
	GeneratedAt time.Time                 `json:"generated_at"`
	Judge       modelSpec                 `json:"judge"`
	Candidates  []qualityCandidateSummary `json:"candidates"`
	Comparison  *qualityComparison        `json:"comparison,omitempty"`
	Comparisons []qualityComparison       `json:"comparisons,omitempty"`
	Winner      string                    `json:"winner"`
	Conclusion  string                    `json:"conclusion"`
}

func runStageQuality(ctx context.Context, opts options) error {
	fixtures, err := loadQualityFixtures()
	if err != nil {
		return err
	}
	cases := buildQualityCases(fixtures)
	if err := validateQualitySuite(cases); err != nil {
		return err
	}
	cases = append(cases, buildWriterToolingCases()...)
	cfg, err := bootstrap.LoadConfig(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if opts.AllConfigured {
		opts.ModelSpecs = configuredQualityModels(cfg)
	}
	opts.ModelSpecs = resolveQualityModelSpecs(cfg, opts.ModelSpecs)
	if len(opts.ModelSpecs) == 0 {
		return errors.New("stage-quality-v1 requires at least one candidate model")
	}
	opts.JudgeSpec = resolveQualityModelSpec(cfg, opts.JudgeSpec)
	if !opts.DryRun && opts.JudgeSpec.Model == "" {
		return errors.New("stage-quality-v1 requires --judge")
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := writeQualityManifest(opts, cases); err != nil {
		return err
	}
	if opts.DryRun {
		fmt.Printf("dry-run: suite=%s ranked_cases/model=%d diagnostic_cases/model=%d models=%d candidate_calls=%d judge_calls=%d output=%s\n",
			qualityBenchmarkVersion, len(cases)-3, 3, len(opts.ModelSpecs), len(cases)*len(opts.ModelSpecs), len(cases), opts.OutputDir)
		for _, spec := range opts.ModelSpecs {
			fmt.Printf("candidate: %s\n", qualityModelLabel(spec))
		}
		if opts.JudgeSpec.Model != "" {
			fmt.Printf("judge: %s\n", qualityModelLabel(opts.JudgeSpec))
		}
		return nil
	}

	candidateModels, err := createQualityModels(cfg, opts.ModelSpecs, opts.RequestTimeout)
	if err != nil {
		return err
	}
	if err := runQualityCandidates(ctx, opts, cases, candidateModels); err != nil {
		return err
	}
	judgeModels, err := createQualityModels(cfg, []modelSpec{opts.JudgeSpec}, opts.RequestTimeout)
	if err != nil {
		return err
	}
	if err := runQualityJudges(ctx, opts, cases, judgeModels[opts.JudgeSpec.ID()]); err != nil {
		return err
	}
	summary, err := aggregateQualityResults(opts, cases)
	if err != nil {
		return err
	}
	return writeQualityReports(opts, summary)
}

func loadQualityFixtures() ([]qualityFixture, error) {
	data, err := qualityFixtureFS.ReadFile("testdata/stage-quality-v1/fixtures.json")
	if err != nil {
		return nil, fmt.Errorf("read quality fixtures: %w", err)
	}
	var fixtures []qualityFixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		return nil, fmt.Errorf("parse quality fixtures: %w", err)
	}
	if len(fixtures) != 3 {
		return nil, fmt.Errorf("quality fixture count = %d, want 3", len(fixtures))
	}
	return fixtures, nil
}

type qualityStageDefinition struct {
	stage      string
	system     string
	task       func(qualityFixture) string
	rubric     []string
	required   func(qualityFixture) []string
	structured bool
	minRunes   int
	maxRunes   int
	maxOutput  int
}

func qualityStageDefinitions() []qualityStageDefinition {
	return []qualityStageDefinition{
		{
			stage: bootstrap.StageCoCreate, structured: false, minRunes: 700, maxRunes: 2600, maxOutput: bootstrap.DefaultCoCreateMaxTokens,
			system: "你是长篇小说共创策划师。只处理用户意图，不续写正文。严格输出四个完整 XML 区块。",
			task: func(f qualityFixture) string {
				return "根据用户意图和资料形成可执行共创方案。必须依次输出 <reply>、<draft>、<ready>、<suggestions>；draft 应明确目标、人物约束、世界规则、风险和验收标准。"
			},
			rubric: []string{"意图捕捉", "方案可执行性", "约束完整性", "风险识别", "表达清晰度"},
			required: func(qualityFixture) []string {
				return []string{"<reply>", "</reply>", "<draft>", "</draft>", "<ready>", "</ready>", "<suggestions>", "</suggestions>"}
			},
		},
		{
			stage: bootstrap.StageSourceAnalysis, structured: true, minRunes: 500, maxRunes: 3200, maxOutput: 6144,
			system: "你是事实优先的小说资料分析师。不得补写资料中不存在的事件，只输出合法 JSON。",
			task: func(f qualityFixture) string {
				return `分析原始资料，只输出 {"facts":[],"characters":[],"timeline":[],"rules":[],"uncertainties":[]}。facts 必须覆盖可验证事实；推断必须放入 uncertainties。`
			},
			rubric: []string{"事实覆盖", "无幻觉", "时间线准确", "人物关系识别", "不确定性处理"},
			required: func(f qualityFixture) []string {
				return append([]string{`"facts"`, `"characters"`, `"timeline"`, `"rules"`, `"uncertainties"`}, f.ExpectedFacts...)
			},
		},
		{
			stage: bootstrap.StageSkeleton, structured: true, minRunes: 700, maxRunes: 3800, maxOutput: 4500,
			system: "你是长篇小说架构师。设计因果清晰的分弧骨架，只输出合法 JSON。",
			task: func(f qualityFixture) string {
				return `设计四弧骨架，只输出 {"volume_goal":"","central_conflict":"","character_arcs":[],"arcs":[{"index":1,"goal":"","obstacle":"","choice":"","consequence":"","payoff":"","constraints":[]}],"risks":[]}。四弧必须升级并回收前置证据。`
			},
			rubric: []string{"整体因果", "冲突升级", "人物弧", "约束服从", "伏笔回收"},
			required: func(qualityFixture) []string {
				return []string{`"volume_goal"`, `"central_conflict"`, `"character_arcs"`, `"arcs"`, `"risks"`}
			},
		},
		{
			stage: bootstrap.StageDetailOutline, structured: true, minRunes: 900, maxRunes: 4500, maxOutput: 5000,
			system: "你是逐章规划师。把骨架转为可直接写作的详细提纲，只输出合法 JSON。",
			task: func(f qualityFixture) string {
				return `为第一弧设计四章，只输出 {"arc_goal":"","chapters":[{"chapter":1,"title":"","goal":"","conflict":"","beats":[],"character_change":"","continuity_checks":[],"hook":""}],"arc_payoff":"","unresolved_threads":[]}。必须恰好四章，每章至少四个可执行节拍。`
			},
			rubric: []string{"逐章因果", "节拍可执行性", "连续性", "人物变化", "节奏与钩子"},
			required: func(qualityFixture) []string {
				return []string{`"arc_goal"`, `"chapters"`, `"beats"`, `"continuity_checks"`, `"hook"`, `"arc_payoff"`}
			},
		},
		{
			stage: bootstrap.StageWriting, structured: false, minRunes: 2600, maxRunes: 3200, maxOutput: 7000,
			system: "你是中文长篇小说作者。直接写场景正文，不解释创作过程。",
			task: func(f qualityFixture) string {
				return "续写给定开篇，形成一个 2600–3200 个中文字符的完整章节。通过行动推进矛盾，保持人物动机与世界规则，关键发现必须有可见取得过程，结尾留下可执行悬念。不要总结、提纲或元叙述。"
			},
			rubric:   []string{"场景与 prose 质量", "人物声音", "因果推进", "连续性", "结尾驱动力"},
			required: func(qualityFixture) []string { return nil },
		},
		{
			stage: bootstrap.StageReview, structured: true, minRunes: 500, maxRunes: 2800, maxOutput: 3500,
			system: "你是严格的小说审稿人。区分重大问题和可选润色，只输出合法 JSON。",
			task: func(f qualityFixture) string {
				return `审核草稿与设定的一致性，只输出 {"verdict":"accept|polish|rewrite","summary":"","issues":[{"severity":"major|minor","evidence":"","impact":"","fix":""}],"strengths":[]}。不得发明草稿中不存在的问题。`
			},
			rubric: []string{"问题发现准确性", "证据定位", "优先级判断", "修复可执行性", "摘要忠实度"},
			required: func(qualityFixture) []string {
				return []string{`"verdict"`, `"summary"`, `"issues"`, `"severity"`, `"evidence"`, `"fix"`}
			},
		},
		{
			stage: bootstrap.StageCharacterAnalysis, structured: true, minRunes: 650, maxRunes: 3200, maxOutput: 4000,
			system: "你是角色与关系分析师。所有判断必须绑定资料证据，只输出合法 JSON。",
			task: func(f qualityFixture) string {
				return `分析角色，只输出 {"characters":[{"name":"","goal":"","fear":"","contradiction":"","evidence":[],"arc_potential":""}],"relationships":[{"source":"","target":"","current_state":"","tension":"","evidence":[]}],"risks":[]}。`
			},
			rubric: []string{"证据扎根", "动机建模", "内在矛盾", "关系动力", "角色弧潜力"},
			required: func(qualityFixture) []string {
				return []string{`"characters"`, `"goal"`, `"contradiction"`, `"evidence"`, `"relationships"`, `"risks"`}
			},
		},
		{
			stage: bootstrap.StageCharacterReview, structured: true, minRunes: 500, maxRunes: 2800, maxOutput: 3500,
			system: "你是角色设定审核人。核对人物契约、关系和行为逻辑，只输出合法 JSON。",
			task: func(f qualityFixture) string {
				return `审核角色设定与现有开篇，只输出 {"verdict":"accept|revise","checks":[{"dimension":"motivation|continuity|relationship|agency","score":0,"evidence":"","issue":"","fix":""}],"false_positive_risks":[],"summary":""}。`
			},
			rubric: []string{"人物契约一致性", "行为逻辑", "关系连续性", "能动性", "误报控制"},
			required: func(qualityFixture) []string {
				return []string{`"verdict"`, `"checks"`, `"dimension"`, `"score"`, `"evidence"`, `"false_positive_risks"`}
			},
		},
	}
}

func buildQualityCases(fixtures []qualityFixture) []qualityCase {
	definitions := qualityStageDefinitions()
	result := make([]qualityCase, 0, len(fixtures)*len(definitions))
	for _, fixture := range fixtures {
		contextText := qualityFixtureContext(fixture)
		for _, definition := range definitions {
			result = append(result, qualityCase{
				ID:              fixture.ID + "__" + definition.stage,
				Stage:           definition.stage,
				FixtureID:       fixture.ID,
				Title:           fixture.Title,
				SystemPrompt:    definition.system,
				TaskPrompt:      definition.task(fixture),
				Context:         contextText,
				Rubric:          append([]string(nil), definition.rubric...),
				RequiredTerms:   definition.required(fixture),
				Structured:      definition.structured,
				MinRunes:        definition.minRunes,
				MaxRunes:        definition.maxRunes,
				MaxOutputTokens: definition.maxOutput,
				EnforceLength:   definition.stage == bootstrap.StageWriting,
			})
		}
	}
	return result
}

func buildWriterToolingCases() []qualityCase {
	return []qualityCase{
		{
			ID: "writer_tooling__consistency_exact_evidence", Stage: "writer_tooling",
			FixtureID: "tooling_consistency_exact", Title: "一致性检查：精确证据", Diagnostic: true,
			SystemPrompt: "你是 Writer。根据章节契约和已回读的当前草稿，生成下一次 check_consistency 的参数。只输出合法 JSON 参数，不解释。",
			Context: `# 章节契约
第 12 章，共 3 个计划场景：
1. 二点十七分，沈砚在值班室发现六号灯熄灭，并打印校时页。
2. 二点二十九分，周启明进入值班室，声称按计划入港；沈砚尚不知道罗岑在调查采购。
3. 沈砚把打印页夹进潮汐表，决定暂不关闭异常单。

# 当前草稿
六号灯在二点十七分灭了。沈砚没有去碰绿色的关闭按钮，先把校时页送进打印机。
二点二十九分，周启明推门进来，说海燕七号一直按计划航行。沈砚把罗岑正在调查采购的事告诉了他。
打印纸还带着热气。沈砚把它夹进潮汐表，异常单继续留在屏幕上。`,
			TaskPrompt: `输出 check_consistency 参数：
{"chapter":12,"scene_checks":[{"scene":1,"evidence":"草稿中可精确检索的原句","time_and_place_match":true,"pov_match":true,"characters_match":true,"event_order_match":true,"knowledge_match":true,"irreversible_result_match":true}],"findings":[]}
必须恰好三项 scene_checks。第二场存在 information boundary 泄漏，knowledge_match 必须为 false，并添加同场景 error finding；其他判断按证据填写。`,
			Rubric: []string{"证据逐字可检索", "场景数量与顺序", "布尔判断准确", "信息边界问题识别", "修复指令可执行"},
			RequiredTerms: []string{
				`"chapter"`, `"scene_checks"`, `"findings"`,
				"六号灯在二点十七分灭了。", "二点二十九分，周启明推门进来", "沈砚把它夹进潮汐表",
				`"knowledge_match"`, `"severity"`, `"error"`,
			},
			Structured: true, MinRunes: 500, MaxRunes: 2600, MaxOutputTokens: 3500,
		},
		{
			ID: "writer_tooling__consistency_missing_scene", Stage: "writer_tooling",
			FixtureID: "tooling_consistency_missing", Title: "一致性检查：缺失场景", Diagnostic: true,
			SystemPrompt: "你是 Writer。根据章节契约和已回读的当前草稿，生成下一次 check_consistency 的参数。只输出合法 JSON 参数，不解释。",
			Context: `# 章节契约
第 7 章，共 3 个计划场景：
1. 陆遥核对十二枚短竹筹。
2. 陆遥在车马簿中发现腊月初七四辆空车入仓，其中两辆属于陆家。
3. 陆遥前往西郊粥棚，确认无官印陈粮从初八开始出现。

# 当前草稿
陆遥把十二枚竹筹排在案上，每一枚都比旧档样筹短半寸。
车马簿翻到腊月初七，四辆空车的编号并排写着，其中两辆带着陆家的记号。
郡守派人来催账，陆遥合上簿册，决定先去问兄长。`,
			TaskPrompt: `输出 check_consistency 参数。必须恰好三项 scene_checks。计划场景完全缺失时，evidence 必须填固定值 "MISSING_FROM_DRAFT"，相关 match 字段设为 false，并添加同场景 critical 或 error finding。`,
			Rubric:     []string{"缺失场景识别", "固定证据值正确", "场景覆盖", "阻断 finding 完整", "不伪造正文证据"},
			RequiredTerms: []string{
				`"chapter"`, `"scene_checks"`, `"findings"`, "十二枚竹筹排在案上",
				"车马簿翻到腊月初七", "MISSING_FROM_DRAFT", `"scene":3`,
			},
			Structured: true, MinRunes: 450, MaxRunes: 2400, MaxOutputTokens: 3200,
		},
		{
			ID: "writer_tooling__de_ai_batch_repair", Stage: "writer_tooling",
			FixtureID: "tooling_de_ai_repair", Title: "去 AI 批量修订", Diagnostic: true,
			SystemPrompt: "你是 Writer。根据 failed check_de_ai 报告和当前草稿，生成一批 repair_de_ai_batch 参数。只输出合法 JSON 参数，不解释。",
			Context: `# 当前草稿（第 9 章）
叶澄看着屏幕，心中不由得泛起一阵复杂的情绪。她知道，这不仅仅是一次故障，更是一次对团队信任的考验。
她打开隔离配置，关闭同步，写入新的阈值。三个动作一气呵成，仿佛一把手术刀切开了迷雾。
顾临把探头放进营养液。读数停在六点一，平台仍显示六点七。

# failed check_de_ai
当前首批类别：template_reaction
examples：
1. 叶澄看着屏幕，心中不由得泛起一阵复杂的情绪。
2. 她知道，这不仅仅是一次故障，更是一次对团队信任的考验。
要求：同一类别每批 1-8 处；old_string 必须从 examples 精确复制且在草稿中唯一出现；new_string 保留叶澄面对自身审批责任和团队信任压力的事实，不做机械同义词替换。`,
			TaskPrompt: `输出 repair_de_ai_batch 参数：
{"chapter":9,"repairs":[{"old_string":"精确原文","new_string":"实质重写"}]}
只处理当前 template_reaction 类别的两个 examples，不修改比喻类别或实验数据。`,
			Rubric: []string{"old_string 精确性", "同类小批次", "事实保留", "改写自然度", "无越界修改"},
			RequiredTerms: []string{
				`"chapter"`, `"repairs"`,
				"叶澄看着屏幕，心中不由得泛起一阵复杂的情绪。",
				"她知道，这不仅仅是一次故障，更是一次对团队信任的考验。",
			},
			Structured: true, MinRunes: 180, MaxRunes: 1600, MaxOutputTokens: 2200,
		},
	}
}

func qualityFixtureContext(f qualityFixture) string {
	return strings.Join([]string{
		"# 测试作品\n" + f.Title,
		"## 用户意图\n" + f.UserIntent,
		"## 原始资料\n" + f.Source,
		"## 前提\n" + f.Premise,
		"## 角色\n" + f.Characters,
		"## 世界规则\n" + f.WorldRules,
		"## 现有骨架\n" + f.Outline,
		"## 待续写或审核草稿\n" + f.Draft,
	}, "\n\n")
}

func validateQualitySuite(cases []qualityCase) error {
	if len(cases) != len(bootstrap.KnownModelStages)*3 {
		return fmt.Errorf("stage-quality case count = %d, want %d", len(cases), len(bootstrap.KnownModelStages)*3)
	}
	counts := make(map[string]int)
	ids := make(map[string]bool)
	for _, testCase := range cases {
		if ids[testCase.ID] {
			return fmt.Errorf("duplicate quality case %q", testCase.ID)
		}
		ids[testCase.ID] = true
		counts[testCase.Stage]++
		if strings.TrimSpace(testCase.SystemPrompt) == "" || strings.TrimSpace(testCase.TaskPrompt) == "" ||
			strings.TrimSpace(testCase.Context) == "" || len(testCase.Rubric) == 0 {
			return fmt.Errorf("quality case %q is incomplete", testCase.ID)
		}
	}
	for _, stage := range bootstrap.KnownModelStages {
		if counts[stage] != 3 {
			return fmt.Errorf("quality stage %q has %d cases, want 3", stage, counts[stage])
		}
		delete(counts, stage)
	}
	if len(counts) != 0 {
		return fmt.Errorf("quality suite contains unknown stages: %v", counts)
	}
	return nil
}

func configuredQualityModels(cfg bootstrap.Config) []modelSpec {
	providers := make([]string, 0, len(cfg.Providers))
	for provider := range cfg.Providers {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	var specs []modelSpec
	for _, provider := range providers {
		for _, model := range cfg.CandidateModels(provider) {
			specs = append(specs, modelSpec{Provider: provider, Model: model})
		}
	}
	return resolveQualityModelSpecs(cfg, specs)
}

func resolveQualityModelSpecs(cfg bootstrap.Config, specs []modelSpec) []modelSpec {
	seen := make(map[string]bool)
	result := make([]modelSpec, 0, len(specs))
	for _, spec := range specs {
		spec = resolveQualityModelSpec(cfg, spec)
		if spec.Provider == "" || spec.Model == "" || seen[spec.ID()] {
			continue
		}
		seen[spec.ID()] = true
		result = append(result, spec)
	}
	return result
}

func resolveQualityModelSpec(cfg bootstrap.Config, spec modelSpec) modelSpec {
	if spec.ReasoningEffort != "" || spec.Provider == "" || spec.Model == "" {
		return spec
	}
	if provider, ok := cfg.Providers[spec.Provider]; ok {
		if effort := strings.TrimSpace(provider.ModelReasoningEfforts[spec.Model]); effort != "" {
			spec.ReasoningEffort = effort
			return spec
		}
	}
	spec.ReasoningEffort = strings.TrimSpace(cfg.ReasoningEffort)
	return spec
}

func createQualityModels(cfg bootstrap.Config, specs []modelSpec, timeout time.Duration) (map[string]agentcore.ChatModel, error) {
	models := make(map[string]agentcore.ChatModel, len(specs))
	for _, spec := range specs {
		provider, ok := cfg.Providers[spec.Provider]
		if !ok {
			return nil, fmt.Errorf("provider %q is not configured", spec.Provider)
		}
		minimumTimeout := int(timeout / time.Second)
		if provider.RequestTimeoutSeconds < minimumTimeout {
			provider.RequestTimeoutSeconds = minimumTimeout
		}
		model, err := bootstrap.NewProviderModelWithConfig(cfg, spec.Provider, spec.Model, provider)
		if err != nil {
			return nil, fmt.Errorf("create %s: %w", qualityModelLabel(spec), err)
		}
		models[spec.ID()] = model
	}
	return models, nil
}

func writeQualityManifest(opts options, cases []qualityCase) error {
	manifest := struct {
		Version     string        `json:"version"`
		CreatedAt   time.Time     `json:"created_at"`
		Candidates  []modelSpec   `json:"candidates"`
		Judge       modelSpec     `json:"judge"`
		Cases       []qualityCase `json:"cases"`
		HardWeight  float64       `json:"hard_weight"`
		JudgeWeight float64       `json:"judge_weight"`
	}{
		Version: opts.Suite, CreatedAt: time.Now(), Candidates: opts.ModelSpecs,
		Judge: opts.JudgeSpec, Cases: cases, HardWeight: 0.30, JudgeWeight: 0.70,
	}
	return writeJSONAtomic(filepath.Join(opts.OutputDir, "manifest.json"), manifest)
}

func runQualityCandidates(ctx context.Context, opts options, cases []qualityCase, models map[string]agentcore.ChatModel) error {
	errCh := make(chan error, len(opts.ModelSpecs))
	var groups sync.WaitGroup
	for _, spec := range opts.ModelSpecs {
		spec := spec
		groups.Add(1)
		go func() {
			defer groups.Done()
			if err := runQualityCandidateGroup(ctx, opts, spec, models[spec.ID()], cases); err != nil {
				errCh <- err
			}
		}()
	}
	groups.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func runQualityCandidateGroup(ctx context.Context, opts options, spec modelSpec, model agentcore.ChatModel, cases []qualityCase) error {
	groupCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan qualityCase)
	workerCount := min(opts.Concurrency, len(cases))
	if workerCount <= 0 {
		workerCount = 1
	}
	workerErrors := make(chan error, workerCount)
	var workers sync.WaitGroup
	for workerID := 0; workerID < workerCount; workerID++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for testCase := range jobs {
				if err := runQualityCandidate(groupCtx, opts, spec, model, testCase); err != nil {
					select {
					case workerErrors <- err:
					default:
					}
					cancel()
					return
				}
				if opts.Cooldown > 0 {
					if err := waitFor(groupCtx, opts.Cooldown); err != nil {
						return
					}
				}
			}
		}()
	}
	for _, testCase := range cases {
		select {
		case <-groupCtx.Done():
			break
		case jobs <- testCase:
			continue
		}
		break
	}
	close(jobs)
	workers.Wait()
	close(workerErrors)
	for err := range workerErrors {
		return err
	}
	if err := groupCtx.Err(); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

func runQualityCandidate(ctx context.Context, opts options, spec modelSpec, model agentcore.ChatModel, testCase qualityCase) error {
	path := qualityCandidatePath(opts.OutputDir, spec, testCase.ID)
	var previous qualityAttempt
	if err := readJSONFile(path, &previous); err == nil {
		if previous.Status == "success" {
			fmt.Printf("skip candidate %s %s: successful result already recorded\n", qualityModelLabel(spec), testCase.ID)
			return nil
		}
		fmt.Printf("retry candidate %s %s: previous status=%s\n", qualityModelLabel(spec), testCase.ID, previous.Status)
	} else if !errors.Is(err, os.ErrNotExist) {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || !errors.Is(pathErr.Err, os.ErrNotExist) {
			return fmt.Errorf("inspect candidate result: %w", err)
		}
	}
	messages := []agentcore.Message{
		agentcore.SystemMsg(testCase.SystemPrompt),
		agentcore.UserMsg(testCase.Context + "\n\n--- 当前任务 ---\n" + testCase.TaskPrompt),
	}
	record := qualityAttempt{
		Version: opts.Suite, CaseID: testCase.ID, Stage: testCase.Stage,
		Provider: spec.Provider, Model: spec.Model, ReasoningEffort: spec.ReasoningEffort,
		Status: "started", StartedAt: time.Now(),
	}
	record.TransportErrors = append(record.TransportErrors, previous.TransportErrors...)
	if previous.Error != "" {
		record.TransportErrors = append(record.TransportErrors, previous.Error)
	}
	if err := writeJSONAtomic(path, record); err != nil {
		return err
	}
	fmt.Printf("[candidate %s] %s stage=%s\n", qualityModelLabel(spec), testCase.ID, testCase.Stage)
	callOptions := []agentcore.CallOption{agentcore.WithMaxTokens(testCase.MaxOutputTokens)}
	if testCase.Structured {
		callOptions = append(callOptions, agentcore.WithJSONMode())
	}
	if spec.ReasoningEffort != "" {
		callOptions = append(callOptions, agentcore.WithThinking(agentcore.ThinkingLevel(spec.ReasoningEffort)))
	}
	started := time.Now()
	var callErr error
	var responseText string
	var responseUsage *agentcore.Usage
	var stopReason agentcore.StopReason
	for attempt := 1; attempt <= 3; attempt++ {
		record.CallAttempts = attempt
		callCtx, cancel := context.WithTimeout(ctx, opts.RequestTimeout)
		response, err := model.Generate(callCtx, messages, nil, callOptions...)
		cancel()
		callErr = err
		if err == nil {
			responseText = response.Message.TextContent()
			responseUsage = response.Message.Usage
			stopReason = response.Message.StopReason
			break
		}
		record.TransportErrors = append(record.TransportErrors, err.Error())
		if attempt < 3 {
			if err := waitFor(ctx, time.Duration(attempt*3)*time.Second); err != nil {
				callErr = err
				break
			}
		}
	}
	record.FinishedAt = time.Now()
	record.DurationMillis = time.Since(started).Milliseconds()
	if callErr != nil {
		record.Status = "error"
		record.Error = callErr.Error()
		record.HardScore = 0
		record.HardIssues = []string{"model_call_error"}
	} else {
		record.Status = "success"
		record.Usage = responseUsage
		record.StopReason = stopReason
		record.Response = responseText
		record.HardScore, record.HardIssues = scoreQualityHardChecks(testCase, record.Response, stopReason)
	}
	if err := writeJSONAtomic(path, record); err != nil {
		return err
	}
	fmt.Printf("[candidate %s] done %s status=%s hard=%.1f duration=%s\n",
		qualityModelLabel(spec), testCase.ID, record.Status, record.HardScore, time.Duration(record.DurationMillis)*time.Millisecond)
	if callErr != nil {
		return fmt.Errorf("candidate %s %s failed after %d attempts: %w",
			qualityModelLabel(spec), testCase.ID, record.CallAttempts, callErr)
	}
	return nil
}

func scoreQualityHardChecks(testCase qualityCase, response string, stopReason agentcore.StopReason) (float64, []string) {
	score := 0.0
	var issues []string
	trimmed := strings.TrimSpace(response)
	if testCase.Structured {
		var parsed any
		if json.Unmarshal([]byte(extractJSONObject(trimmed)), &parsed) == nil {
			score += 25
		} else {
			issues = append(issues, "invalid_json")
		}
	} else if trimmed != "" {
		score += 25
	} else {
		issues = append(issues, "empty_output")
	}
	runes := utf8.RuneCountInString(trimmed)
	if !testCase.EnforceLength && trimmed != "" {
		score += 15
	} else if testCase.EnforceLength && runes >= testCase.MinRunes {
		score += 15
	} else {
		issues = append(issues, "length_out_of_range")
	}
	if len(testCase.RequiredTerms) == 0 {
		score += 35
	} else {
		hits := 0
		for _, term := range testCase.RequiredTerms {
			if strings.Contains(trimmed, term) {
				hits++
			}
		}
		score += 35 * float64(hits) / float64(len(testCase.RequiredTerms))
		if hits != len(testCase.RequiredTerms) {
			issues = append(issues, "missing_required_content")
		}
	}
	forbidden := []string{"作为AI", "作为 AI", "无法完成", "以下是提纲"}
	forbiddenHit := false
	for _, term := range forbidden {
		if strings.Contains(trimmed, term) {
			forbiddenHit = true
			break
		}
	}
	if !forbiddenHit {
		score += 10
	} else {
		issues = append(issues, "forbidden_meta_text")
	}
	if stopReason != agentcore.StopReasonLength {
		score += 15
	} else {
		issues = append(issues, "truncated")
	}
	return roundQuality(score), issues
}

func runQualityJudges(ctx context.Context, opts options, cases []qualityCase, judge agentcore.ChatModel) error {
	workerCount := opts.Concurrency
	if workerCount > len(cases) {
		workerCount = len(cases)
	}
	jobs := make(chan qualityCase, len(cases))
	errCh := make(chan error, workerCount)
	for _, testCase := range cases {
		jobs <- testCase
	}
	close(jobs)
	var workers sync.WaitGroup
	for worker := 0; worker < workerCount; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for testCase := range jobs {
				if err := runQualityJudge(ctx, opts, testCase, judge); err != nil {
					errCh <- err
					return
				}
				if opts.Cooldown > 0 {
					if err := waitFor(ctx, opts.Cooldown); err != nil {
						errCh <- err
						return
					}
				}
			}
		}()
	}
	workers.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

func runQualityJudge(ctx context.Context, opts options, testCase qualityCase, judge agentcore.ChatModel) error {
	path := qualityJudgePath(opts.OutputDir, testCase.ID)
	var previous qualityJudgeRecord
	if err := readJSONFile(path, &previous); err == nil {
		if previous.Status == "success" && len(previous.AnonymousMapping) == len(opts.ModelSpecs) {
			fmt.Printf("skip judge %s: successful result already recorded\n", testCase.ID)
			return nil
		}
		fmt.Printf("retry judge %s: previous status=%s candidates=%d want=%d\n",
			testCase.ID, previous.Status, len(previous.AnonymousMapping), len(opts.ModelSpecs))
	} else if !errors.Is(err, os.ErrNotExist) {
		var pathErr *os.PathError
		if !errors.As(err, &pathErr) || !errors.Is(pathErr.Err, os.ErrNotExist) {
			return fmt.Errorf("inspect judge result: %w", err)
		}
	}
	attempts := make([]qualityAttempt, 0, len(opts.ModelSpecs))
	for _, spec := range opts.ModelSpecs {
		var attempt qualityAttempt
		if err := readJSONFile(qualityCandidatePath(opts.OutputDir, spec, testCase.ID), &attempt); err != nil {
			return err
		}
		attempts = append(attempts, attempt)
	}
	ordered := stableAnonymousOrder(testCase.ID, attempts)
	mapping := make(map[string]string, len(ordered))
	var candidates strings.Builder
	for index, attempt := range ordered {
		label := string(rune('A' + index))
		mapping[label] = qualityAttemptID(attempt)
		fmt.Fprintf(&candidates, "\n\n## 候选 %s\n%s", label, attempt.Response)
	}
	systemPrompt := `你是独立的中文长篇小说 benchmark 裁判。候选已经匿名化。
只根据相同输入、任务和评分维度评价，不猜测模型身份，不因文风偏好替代任务约束。
每个维度和 overall 使用 0-100 分。只输出合法 JSON，不要 Markdown。`
	userPrompt := fmt.Sprintf(`# 输入
%s

# 任务
%s

# 评分维度
%s

# 候选
%s

返回：
{"scores":[{"candidate":"A","dimensions":[{"name":"维度名","score":0}],"overall":0,"summary":"不超过80字"}],"preference":"A|B|tie","confidence":0}
scores 必须覆盖每个候选；dimensions 必须覆盖全部评分维度。`, testCase.Context, testCase.TaskPrompt, strings.Join(testCase.Rubric, "、"), candidates.String())
	record := qualityJudgeRecord{
		Version: opts.Suite, CaseID: testCase.ID, Judge: opts.JudgeSpec,
		Status: "started", StartedAt: time.Now(), AnonymousMapping: mapping,
	}
	record.Responses = append(record.Responses, previous.Responses...)
	if err := writeJSONAtomic(path, record); err != nil {
		return err
	}
	fmt.Printf("[judge %s] %s candidates=%d\n", qualityModelLabel(opts.JudgeSpec), testCase.ID, len(ordered))
	started := time.Now()
	var lastErr error
	for attempt := 1; attempt <= 2; attempt++ {
		record.CallAttempts = attempt
		prompt := userPrompt
		if attempt > 1 {
			prompt += "\n\n上一次返回无法解析。重新从原始候选评分，并严格返回上述 JSON。"
		}
		callOptions := []agentcore.CallOption{agentcore.WithMaxTokens(4000), agentcore.WithJSONMode()}
		if opts.JudgeSpec.ReasoningEffort != "" {
			callOptions = append(callOptions, agentcore.WithThinking(agentcore.ThinkingLevel(opts.JudgeSpec.ReasoningEffort)))
		}
		callCtx, cancel := context.WithTimeout(ctx, opts.RequestTimeout)
		response, err := judge.Generate(callCtx, []agentcore.Message{agentcore.SystemMsg(systemPrompt), agentcore.UserMsg(prompt)}, nil, callOptions...)
		cancel()
		if err != nil {
			lastErr = err
			continue
		}
		text := response.Message.TextContent()
		record.Responses = append(record.Responses, text)
		judgement, err := parseQualityJudgement(text, mapping, testCase.Rubric)
		if err != nil {
			lastErr = err
			continue
		}
		record.Judgement = judgement
		record.Status = "success"
		lastErr = nil
		break
	}
	record.FinishedAt = time.Now()
	record.DurationMillis = time.Since(started).Milliseconds()
	if lastErr != nil {
		record.Status = "error"
		record.Error = lastErr.Error()
	}
	if err := writeJSONAtomic(path, record); err != nil {
		return err
	}
	if record.Status != "success" {
		return fmt.Errorf("judge %s failed: %w", testCase.ID, lastErr)
	}
	fmt.Printf("[judge] done %s duration=%s\n", testCase.ID, time.Duration(record.DurationMillis)*time.Millisecond)
	return nil
}

func stableAnonymousOrder(caseID string, attempts []qualityAttempt) []qualityAttempt {
	result := append([]qualityAttempt(nil), attempts...)
	sort.SliceStable(result, func(i, j int) bool {
		left := sha256.Sum256([]byte(caseID + "\x00" + qualityAttemptID(result[i])))
		right := sha256.Sum256([]byte(caseID + "\x00" + qualityAttemptID(result[j])))
		return bytes.Compare(left[:], right[:]) < 0
	})
	return result
}

func parseQualityJudgement(text string, mapping map[string]string, rubric []string) (*qualityJudgement, error) {
	var judgement qualityJudgement
	if err := json.Unmarshal([]byte(extractJSONObject(text)), &judgement); err != nil {
		return nil, fmt.Errorf("parse judge JSON: %w", err)
	}
	if len(judgement.Scores) != len(mapping) {
		return nil, fmt.Errorf("judge score count = %d, want %d", len(judgement.Scores), len(mapping))
	}
	if judgement.Confidence > 1 && judgement.Confidence <= 100 {
		judgement.Confidence /= 100
	}
	if judgement.Confidence < 0 || judgement.Confidence > 1 {
		return nil, fmt.Errorf("judge confidence %.2f is outside 0-1", judgement.Confidence)
	}
	seen := make(map[string]bool)
	for _, score := range judgement.Scores {
		if _, ok := mapping[score.Candidate]; !ok || seen[score.Candidate] {
			return nil, fmt.Errorf("judge returned invalid candidate %q", score.Candidate)
		}
		seen[score.Candidate] = true
		if score.Overall < 0 || score.Overall > 100 {
			return nil, fmt.Errorf("judge overall %.2f is outside 0-100", score.Overall)
		}
		if len(score.Dimensions) != len(rubric) {
			return nil, fmt.Errorf("judge dimensions = %d, want %d", len(score.Dimensions), len(rubric))
		}
		for _, dimension := range score.Dimensions {
			if dimension.Score < 0 || dimension.Score > 100 {
				return nil, fmt.Errorf("judge dimension %.2f is outside 0-100", dimension.Score)
			}
			if strings.TrimSpace(dimension.Name) == "" {
				return nil, errors.New("judge dimension name is empty")
			}
		}
	}
	return &judgement, nil
}

func aggregateQualityResults(opts options, cases []qualityCase) (qualitySummary, error) {
	type scoreRow struct {
		hard  float64
		judge float64
		final float64
	}
	rows := make(map[string]map[string]scoreRow)
	for _, spec := range opts.ModelSpecs {
		rows[spec.ID()] = make(map[string]scoreRow)
	}
	for _, testCase := range cases {
		var judgeRecord qualityJudgeRecord
		if err := readJSONFile(qualityJudgePath(opts.OutputDir, testCase.ID), &judgeRecord); err != nil {
			return qualitySummary{}, err
		}
		judgeByID := make(map[string]float64)
		for _, score := range judgeRecord.Judgement.Scores {
			judgeByID[judgeRecord.AnonymousMapping[score.Candidate]] = score.Overall
		}
		for _, spec := range opts.ModelSpecs {
			var attempt qualityAttempt
			if err := readJSONFile(qualityCandidatePath(opts.OutputDir, spec, testCase.ID), &attempt); err != nil {
				return qualitySummary{}, err
			}
			hardScore, _ := scoreQualityHardChecks(testCase, attempt.Response, attempt.StopReason)
			judgeScore := judgeByID[qualityAttemptID(attempt)]
			finalScore := 0.30*hardScore + 0.70*judgeScore
			if testCase.Stage == bootstrap.StageWriting && testCase.EnforceLength {
				runes := utf8.RuneCountInString(strings.TrimSpace(attempt.Response))
				finalScore, _, _ = applyWritingLengthGate(finalScore, runes, testCase.MinRunes)
			}
			rows[spec.ID()][testCase.ID] = scoreRow{hard: hardScore, judge: judgeScore, final: roundQuality(finalScore)}
		}
	}

	summary := qualitySummary{Version: qualityBenchmarkVersion, GeneratedAt: time.Now(), Judge: opts.JudgeSpec}
	for _, spec := range opts.ModelSpecs {
		candidate := qualityCandidateSummary{
			Candidate: qualityModelLabel(spec), Provider: spec.Provider, Model: spec.Model,
			ReasoningEffort: spec.ReasoningEffort,
		}
		var stageMeans []float64
		var nonCreative []float64
		for _, stage := range bootstrap.KnownModelStages {
			var hardValues, judgeValues, finalValues []float64
			for _, testCase := range cases {
				if testCase.Stage != stage {
					continue
				}
				row := rows[spec.ID()][testCase.ID]
				hardValues = append(hardValues, row.hard)
				judgeValues = append(judgeValues, row.judge)
				finalValues = append(finalValues, row.final)
			}
			stageSummary := qualityStageSummary{
				Stage: stage, Samples: len(finalValues), HardMean: roundQuality(meanQuality(hardValues)),
				JudgeMean: roundQuality(meanQuality(judgeValues)), FinalMean: roundQuality(meanQuality(finalValues)),
				StdDev: roundQuality(stdDevQuality(finalValues)),
			}
			candidate.Stages = append(candidate.Stages, stageSummary)
			stageMeans = append(stageMeans, stageSummary.FinalMean)
			if stage == bootstrap.StageWriting {
				candidate.WritingScore = stageSummary.FinalMean
			} else {
				nonCreative = append(nonCreative, stageSummary.FinalMean)
			}
		}
		var toolingValues []float64
		for _, testCase := range cases {
			if !testCase.Diagnostic {
				continue
			}
			row := rows[spec.ID()][testCase.ID]
			candidate.WriterTooling = append(candidate.WriterTooling, qualityDiagnosticSummary{
				CaseID: testCase.ID, HardScore: row.hard, JudgeScore: row.judge, FinalScore: row.final,
			})
			toolingValues = append(toolingValues, row.final)
		}
		candidate.OverallScore = roundQuality(meanQuality(stageMeans))
		candidate.NonCreativeScore = roundQuality(meanQuality(nonCreative))
		candidate.WriterToolingScore = roundQuality(meanQuality(toolingValues))
		summary.Candidates = append(summary.Candidates, candidate)
	}
	sort.SliceStable(summary.Candidates, func(i, j int) bool {
		return summary.Candidates[i].OverallScore > summary.Candidates[j].OverallScore
	})
	if len(summary.Candidates) > 0 {
		summary.Winner = summary.Candidates[0].Candidate
	}
	if len(opts.ModelSpecs) >= 2 {
		for leftIndex := 0; leftIndex < len(opts.ModelSpecs); leftIndex++ {
			for rightIndex := leftIndex + 1; rightIndex < len(opts.ModelSpecs); rightIndex++ {
				left, right := opts.ModelSpecs[leftIndex], opts.ModelSpecs[rightIndex]
				var deltas []float64
				for _, testCase := range cases {
					if testCase.Diagnostic {
						continue
					}
					deltas = append(deltas, rows[left.ID()][testCase.ID].final-rows[right.ID()][testCase.ID].final)
				}
				low, high := bootstrapQualityCI(deltas, 10_000)
				comparison := qualityComparison{
					LeftCandidate: qualityModelLabel(left), RightCandidate: qualityModelLabel(right),
					MeanPairedDelta: roundQuality(meanQuality(deltas)), ConfidenceLow: roundQuality(low),
					ConfidenceHigh: roundQuality(high),
				}
				comparison.WeakAdvantage = math.Abs(comparison.MeanPairedDelta) < 2 ||
					(comparison.ConfidenceLow <= 0 && comparison.ConfidenceHigh >= 0)
				summary.Comparisons = append(summary.Comparisons, comparison)
			}
		}
		if len(summary.Comparisons) == 1 {
			summary.Comparison = &summary.Comparisons[0]
		}
		for index := range summary.Candidates {
			wins := 0
			for _, stage := range bootstrap.KnownModelStages {
				own := stageScore(summary.Candidates[index], stage)
				best := true
				for otherIndex := range summary.Candidates {
					if otherIndex != index && own <= stageScore(summary.Candidates[otherIndex], stage) {
						best = false
						break
					}
				}
				if best {
					wins++
				}
			}
			summary.Candidates[index].StageWins = wins
		}
	}
	if len(summary.Candidates) >= 2 {
		leader := summary.Candidates[0]
		strength := "优势明确"
		for _, comparison := range summary.Comparisons {
			if (comparison.LeftCandidate == leader.Candidate || comparison.RightCandidate == leader.Candidate) &&
				comparison.WeakAdvantage {
				strength = "至少一组优势较弱"
				break
			}
		}
		var details []string
		for _, candidate := range summary.Candidates {
			details = append(details, fmt.Sprintf("%s：总分 %.1f、非创作 %.1f、正文 %.1f、Writer 工具 %.1f",
				candidate.Candidate, candidate.OverallScore, candidate.NonCreativeScore,
				candidate.WritingScore, candidate.WriterToolingScore))
		}
		summary.Conclusion = fmt.Sprintf("%s 排名第一（%s）。%s。",
			leader.Candidate, strength, strings.Join(details, "；"))
	}
	return summary, nil
}

func writeQualityReports(opts options, summary qualitySummary) error {
	localDir := filepath.Join(opts.OutputDir, "aggregate")
	if err := writeQualityReportSet(localDir, summary); err != nil {
		return err
	}
	if strings.TrimSpace(opts.ReportDir) != "" {
		if err := writeQualityReportSet(opts.ReportDir, summary); err != nil {
			return err
		}
	}
	fmt.Printf("stage-quality report: %s\nwinner: %s\n%s\n", localDir, summary.Winner, summary.Conclusion)
	return nil
}

func writeQualityReportSet(dir string, summary qualitySummary) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create report dir: %w", err)
	}
	if err := writeJSONAtomic(filepath.Join(dir, "summary.json"), summary); err != nil {
		return err
	}
	var csvBuffer bytes.Buffer
	writer := csv.NewWriter(&csvBuffer)
	_ = writer.Write([]string{"candidate", "stage", "samples", "hard_mean", "judge_mean", "final_mean", "stddev"})
	for _, candidate := range summary.Candidates {
		for _, stage := range candidate.Stages {
			_ = writer.Write([]string{
				candidate.Candidate, stage.Stage, strconv.Itoa(stage.Samples),
				fmt.Sprintf("%.1f", stage.HardMean), fmt.Sprintf("%.1f", stage.JudgeMean),
				fmt.Sprintf("%.1f", stage.FinalMean), fmt.Sprintf("%.1f", stage.StdDev),
			})
		}
		for _, diagnostic := range candidate.WriterTooling {
			_ = writer.Write([]string{
				candidate.Candidate, "writer_tooling:" + diagnostic.CaseID, "1",
				fmt.Sprintf("%.1f", diagnostic.HardScore), fmt.Sprintf("%.1f", diagnostic.JudgeScore),
				fmt.Sprintf("%.1f", diagnostic.FinalScore), "0.0",
			})
		}
	}
	writer.Flush()
	if err := os.WriteFile(filepath.Join(dir, "scores.csv"), append([]byte{0xEF, 0xBB, 0xBF}, csvBuffer.Bytes()...), 0o644); err != nil {
		return fmt.Errorf("write scores CSV: %w", err)
	}
	var markdown strings.Builder
	markdown.WriteString("# 全阶段模型质量 Benchmark\n\n")
	fmt.Fprintf(&markdown, "- 套件：`%s`\n- 裁判：`%s`\n- 评分：硬指标 30%% + 匿名盲评 70%%\n\n", summary.Version, qualityModelLabel(summary.Judge))
	markdown.WriteString("阶段输出预算与生产路径对齐：共创 4096 tokens、资料分析 6144 tokens、正文 7000 tokens；其他阶段使用各自测试契约中的固定预算。模型若在预算内结束但未满足篇幅或协议，仍按实际结果扣分。\n\n")
	markdown.WriteString("| 排名 | 候选 | 总分 | 非创作 | 正文 | Writer 工具编排* | 阶段胜场 |\n|---:|---|---:|---:|---:|---:|---:|\n")
	for index, candidate := range summary.Candidates {
		fmt.Fprintf(&markdown, "| %d | `%s` | %.1f | %.1f | %.1f | %.1f | %d |\n",
			index+1, candidate.Candidate, candidate.OverallScore, candidate.NonCreativeScore, candidate.WritingScore,
			candidate.WriterToolingScore, candidate.StageWins)
	}
	markdown.WriteString("\n\\* Writer 工具编排是额外诊断，不计入 8 阶段主榜。\n")
	markdown.WriteString("\n## 分阶段结果\n\n")
	markdown.WriteString("| 阶段 | 候选 | 硬指标 | 盲评 | 最终分 | 标准差 |\n|---|---|---:|---:|---:|---:|\n")
	for _, candidate := range summary.Candidates {
		for _, stage := range candidate.Stages {
			fmt.Fprintf(&markdown, "| %s | `%s` | %.1f | %.1f | %.1f | %.1f |\n",
				stage.Stage, candidate.Candidate, stage.HardMean, stage.JudgeMean, stage.FinalMean, stage.StdDev)
		}
	}
	markdown.WriteString("\n## Writer 确定性检查工具编排诊断\n\n")
	markdown.WriteString("| 候选 | 诊断题 | 硬指标 | 盲评 | 最终分 |\n|---|---|---:|---:|---:|\n")
	for _, candidate := range summary.Candidates {
		for _, diagnostic := range candidate.WriterTooling {
			fmt.Fprintf(&markdown, "| `%s` | %s | %.1f | %.1f | %.1f |\n",
				candidate.Candidate, diagnostic.CaseID, diagnostic.HardScore, diagnostic.JudgeScore, diagnostic.FinalScore)
		}
	}
	if len(summary.Comparisons) > 0 {
		markdown.WriteString("\n## 配对比较\n\n")
		markdown.WriteString("| 左候选 | 右候选 | 左减右逐题均差 | 95% bootstrap 区间 | 判定 |\n|---|---|---:|---:|---|\n")
		for _, comparison := range summary.Comparisons {
			fmt.Fprintf(&markdown, "| `%s` | `%s` | %.1f | [%.1f, %.1f] | %s |\n",
				comparison.LeftCandidate, comparison.RightCandidate, comparison.MeanPairedDelta,
				comparison.ConfidenceLow, comparison.ConfidenceHigh,
				map[bool]string{true: "优势较弱", false: "优势明确"}[comparison.WeakAdvantage])
		}
	}
	fmt.Fprintf(&markdown, "\n## 结论\n\n%s\n\n原始提示、模型响应和裁判响应仅保存在本地 `.ainovel/benchmarks/stage-quality-v1`，本报告不包含正文或凭证。\n", summary.Conclusion)
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte(markdown.String()), 0o644); err != nil {
		return fmt.Errorf("write report markdown: %w", err)
	}
	return nil
}

func qualityCandidatePath(outputDir string, spec modelSpec, caseID string) string {
	return filepath.Join(outputDir, "candidates", spec.ID(), sanitizePathPart(caseID)+".json")
}

func qualityJudgePath(outputDir, caseID string) string {
	return filepath.Join(outputDir, "judges", sanitizePathPart(caseID)+".json")
}

func readJSONFile(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	return nil
}

func qualityAttemptID(attempt qualityAttempt) string {
	return modelSpec{Provider: attempt.Provider, Model: attempt.Model, ReasoningEffort: attempt.ReasoningEffort}.ID()
}

func qualityModelLabel(spec modelSpec) string {
	label := spec.Provider + "/" + spec.Model
	if spec.ReasoningEffort != "" {
		label += "@" + spec.ReasoningEffort
	}
	return label
}

func meanQuality(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var total float64
	for _, value := range values {
		total += value
	}
	return total / float64(len(values))
}

func stdDevQuality(values []float64) float64 {
	if len(values) < 2 {
		return 0
	}
	mean := meanQuality(values)
	var sum float64
	for _, value := range values {
		delta := value - mean
		sum += delta * delta
	}
	return math.Sqrt(sum / float64(len(values)-1))
}

func roundQuality(value float64) float64 {
	return math.Round(value*10) / 10
}

func bootstrapQualityCI(values []float64, iterations int) (float64, float64) {
	if len(values) == 0 {
		return 0, 0
	}
	random := rand.New(rand.NewSource(45))
	means := make([]float64, iterations)
	for iteration := range iterations {
		sample := make([]float64, len(values))
		for index := range sample {
			sample[index] = values[random.Intn(len(values))]
		}
		means[iteration] = meanQuality(sample)
	}
	sort.Float64s(means)
	lowIndex := int(0.025 * float64(iterations-1))
	highIndex := int(0.975 * float64(iterations-1))
	return means[lowIndex], means[highIndex]
}

func stageScore(candidate qualityCandidateSummary, stage string) float64 {
	for _, item := range candidate.Stages {
		if item.Stage == stage {
			return item.FinalMean
		}
	}
	return 0
}
