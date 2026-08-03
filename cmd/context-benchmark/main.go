package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	corecontext "github.com/voocel/agentcore/context"
	"github.com/voocel/ainovel-cli/internal/bootstrap"
)

const (
	defaultCallsPerModel = 40
	benchmarkVersion     = "2026-07-12-v1"
)

type modelSpec struct {
	Provider        string `json:"provider"`
	Model           string `json:"model"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
}

func (s modelSpec) ID() string {
	id := s.Provider + "__" + s.Model
	if s.ReasoningEffort != "" {
		id += "__" + s.ReasoningEffort
	}
	return sanitizePathPart(id)
}

type benchmarkCase struct {
	ID               string   `json:"id"`
	Stage            string   `json:"stage"`
	TargetTokens     int      `json:"target_tokens"`
	Replica          int      `json:"replica"`
	Structured       bool     `json:"structured"`
	MaxOutputTokens  int      `json:"max_output_tokens"`
	SystemPrompt     string   `json:"-"`
	TaskPrompt       string   `json:"-"`
	ContextDocuments []string `json:"-"`
	AnalysisSegments int      `json:"analysis_segments,omitempty"`
}

type attemptRecord struct {
	Version          string               `json:"version"`
	Case             benchmarkCase        `json:"case"`
	Provider         string               `json:"provider"`
	Model            string               `json:"model"`
	Status           string               `json:"status"`
	StartedAt        time.Time            `json:"started_at"`
	FinishedAt       *time.Time           `json:"finished_at,omitempty"`
	EstimatedInput   int                  `json:"estimated_input_tokens"`
	DurationMillis   int64                `json:"duration_millis,omitempty"`
	StopReason       agentcore.StopReason `json:"stop_reason,omitempty"`
	Usage            *agentcore.Usage     `json:"usage,omitempty"`
	Response         string               `json:"response,omitempty"`
	Error            string               `json:"error,omitempty"`
	JSONValid        bool                 `json:"json_valid"`
	OutputRunes      int                  `json:"output_runes,omitempty"`
	WouldRetry       bool                 `json:"would_retry"`
	ValidationIssues []string             `json:"validation_issues,omitempty"`
}

type options struct {
	ProjectRoot    string
	OutputDir      string
	ReportDir      string
	ConfigPath     string
	ModelSpecs     []modelSpec
	Suite          string
	JudgeSpec      modelSpec
	AllConfigured  bool
	CallsPerModel  int
	DryRun         bool
	RequestTimeout time.Duration
	Cooldown       time.Duration
	Concurrency    int
	StartStagger   time.Duration
}

func main() {
	opts, err := parseOptions()
	if err != nil {
		fatal(err)
	}
	if err := run(context.Background(), opts); err != nil {
		fatal(err)
	}
}

func parseOptions() (options, error) {
	var rawModels string
	var rawJudge string
	var opts options
	flag.StringVar(&opts.ProjectRoot, "project-root", "", "AINovel project root used as benchmark evidence")
	flag.StringVar(&opts.OutputDir, "output", "", "benchmark result directory")
	flag.StringVar(&opts.ReportDir, "report-output", "", "optional sanitized aggregate report directory")
	flag.StringVar(&opts.ConfigPath, "config", "", "optional config override")
	flag.StringVar(&opts.Suite, "suite", "context-window-v1", "benchmark suite: context-window-v1, stage-quality-v1, or writing-length-ab-v1")
	flag.StringVar(&rawModels, "models", "deepseek-yuanyu-0/deepseek-v4-pro,grok-oauth/grok-4.5", "comma-separated provider/model pairs")
	flag.BoolVar(&opts.AllConfigured, "all-configured", false, "benchmark every provider/model pair in the loaded config")
	flag.StringVar(&rawJudge, "judge", "", "blind judge provider/model@reasoning for stage-quality-v1")
	flag.IntVar(&opts.CallsPerModel, "max-calls-per-model", defaultCallsPerModel, "hard attempt cap for each model")
	flag.DurationVar(&opts.RequestTimeout, "request-timeout", 5*time.Minute, "per-request timeout")
	flag.DurationVar(&opts.Cooldown, "cooldown", 10*time.Second, "delay after each model request")
	flag.IntVar(&opts.Concurrency, "concurrency-per-model", 3, "maximum concurrent requests for each model")
	flag.DurationVar(&opts.StartStagger, "start-stagger", 2*time.Second, "stagger concurrent workers for the same model")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "build the suite without calling models")
	flag.Parse()

	if strings.TrimSpace(opts.ProjectRoot) == "" {
		return opts, errors.New("--project-root is required")
	}
	projectRoot, err := filepath.Abs(opts.ProjectRoot)
	if err != nil {
		return opts, fmt.Errorf("resolve project root: %w", err)
	}
	opts.ProjectRoot = projectRoot
	if opts.OutputDir == "" {
		opts.OutputDir = filepath.Join(projectRoot, ".ainovel", "benchmarks", opts.Suite)
	}
	opts.OutputDir, err = filepath.Abs(opts.OutputDir)
	if err != nil {
		return opts, fmt.Errorf("resolve output dir: %w", err)
	}
	if opts.CallsPerModel <= 0 || opts.CallsPerModel > defaultCallsPerModel {
		return opts, fmt.Errorf("--max-calls-per-model must be between 1 and %d", defaultCallsPerModel)
	}
	if opts.Cooldown < 0 {
		return opts, errors.New("--cooldown must not be negative")
	}
	if opts.Concurrency <= 0 || opts.Concurrency > 5 {
		return opts, errors.New("--concurrency-per-model must be between 1 and 5")
	}
	if opts.StartStagger < 0 {
		return opts, errors.New("--start-stagger must not be negative")
	}
	opts.ModelSpecs, err = parseModelSpecs(rawModels)
	if err != nil {
		return opts, err
	}
	if strings.TrimSpace(rawJudge) != "" {
		judges, parseErr := parseModelSpecs(rawJudge)
		if parseErr != nil {
			return opts, fmt.Errorf("parse --judge: %w", parseErr)
		}
		if len(judges) != 1 {
			return opts, errors.New("--judge must contain exactly one provider/model")
		}
		opts.JudgeSpec = judges[0]
	}
	switch opts.Suite {
	case "context-window-v1":
		if opts.AllConfigured {
			return opts, errors.New("--all-configured is only supported by stage-quality-v1")
		}
	case qualityBenchmarkVersion, writingLengthABVersion:
		if opts.JudgeSpec.Model == "" && !opts.DryRun {
			return opts, fmt.Errorf("--judge is required for %s", opts.Suite)
		}
	default:
		return opts, fmt.Errorf("unknown --suite %q", opts.Suite)
	}
	return opts, nil
}

func parseModelSpecs(raw string) ([]modelSpec, error) {
	parts := strings.Split(raw, ",")
	result := make([]modelSpec, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimSpace(part)
		reasoning := ""
		if at := strings.LastIndex(value, "@"); at >= 0 {
			reasoning = strings.TrimSpace(value[at+1:])
			value = strings.TrimSpace(value[:at])
			if reasoning == "" {
				return nil, fmt.Errorf("invalid model spec %q; reasoning effort is empty", part)
			}
		}
		provider, model, ok := strings.Cut(value, "/")
		if !ok || strings.TrimSpace(provider) == "" || strings.TrimSpace(model) == "" {
			return nil, fmt.Errorf("invalid model spec %q; want provider/model@reasoning", part)
		}
		result = append(result, modelSpec{
			Provider:        strings.TrimSpace(provider),
			Model:           strings.TrimSpace(model),
			ReasoningEffort: reasoning,
		})
	}
	if len(result) == 0 {
		return nil, errors.New("at least one model is required")
	}
	return result, nil
}

func run(ctx context.Context, opts options) error {
	if opts.Suite == writingLengthABVersion {
		return runWritingLengthAB(ctx, opts)
	}
	if opts.Suite == qualityBenchmarkVersion {
		return runStageQuality(ctx, opts)
	}
	corpus, err := loadCorpus(opts.ProjectRoot)
	if err != nil {
		return err
	}
	cases := buildSuite(corpus)
	if len(cases) != defaultCallsPerModel {
		return fmt.Errorf("benchmark suite has %d cases, want %d", len(cases), defaultCallsPerModel)
	}
	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	if err := writeManifest(opts.OutputDir, opts, cases); err != nil {
		return err
	}
	if opts.DryRun {
		fmt.Printf("dry-run: %d cases/model, %d models, output=%s\n", len(cases), len(opts.ModelSpecs), opts.OutputDir)
		return nil
	}

	cfg, err := bootstrap.LoadConfig(opts.ConfigPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	models := make(map[string]agentcore.ChatModel, len(opts.ModelSpecs))
	for _, spec := range opts.ModelSpecs {
		pc, ok := cfg.Providers[spec.Provider]
		if !ok {
			return fmt.Errorf("provider %q is not configured", spec.Provider)
		}
		minimumTimeout := int(opts.RequestTimeout / time.Second)
		if pc.RequestTimeoutSeconds < minimumTimeout {
			pc.RequestTimeoutSeconds = minimumTimeout
		}
		model, modelErr := bootstrap.NewProviderModelWithConfig(cfg, spec.Provider, spec.Model, pc)
		if modelErr != nil {
			return fmt.Errorf("create %s/%s: %w", spec.Provider, spec.Model, modelErr)
		}
		models[spec.ID()] = model
	}

	return runConcurrent(ctx, opts, models, cases)
}

type indexedCase struct {
	value  benchmarkCase
	number int
}

func runConcurrent(ctx context.Context, opts options, models map[string]agentcore.ChatModel, cases []benchmarkCase) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, len(opts.ModelSpecs))
	var modelGroups sync.WaitGroup

	for _, spec := range opts.ModelSpecs {
		pending, err := pendingCases(opts.OutputDir, spec, cases, opts.CallsPerModel)
		if err != nil {
			return err
		}
		if len(pending) == 0 {
			continue
		}
		modelGroups.Add(1)
		go func(spec modelSpec, pending []indexedCase) {
			defer modelGroups.Done()
			jobs := make(chan indexedCase)
			var workers sync.WaitGroup
			workerCount := min(opts.Concurrency, len(pending))
			for workerID := 0; workerID < workerCount; workerID++ {
				workers.Add(1)
				go func(workerID int) {
					defer workers.Done()
					if delay := time.Duration(workerID) * opts.StartStagger; delay > 0 {
						if err := waitFor(ctx, delay); err != nil {
							return
						}
					}
					for job := range jobs {
						if ctx.Err() != nil {
							return
						}
						if err := runCase(ctx, opts, spec, models[spec.ID()], job.value, job.number, len(cases)); err != nil {
							select {
							case errCh <- err:
							default:
							}
							cancel()
							return
						}
					}
				}(workerID)
			}
			for _, job := range pending {
				select {
				case <-ctx.Done():
					close(jobs)
					workers.Wait()
					return
				case jobs <- job:
				}
			}
			close(jobs)
			workers.Wait()
		}(spec, pending)
	}

	done := make(chan struct{})
	go func() {
		modelGroups.Wait()
		close(done)
	}()
	select {
	case err := <-errCh:
		return err
	case <-done:
		select {
		case err := <-errCh:
			return err
		default:
			return nil
		}
	}
}

func pendingCases(outputDir string, spec modelSpec, cases []benchmarkCase, cap int) ([]indexedCase, error) {
	attempts, err := countAttempts(outputDir, spec)
	if err != nil {
		return nil, err
	}
	remaining := cap - attempts
	if remaining <= 0 {
		return nil, nil
	}
	pending := make([]indexedCase, 0, remaining)
	for index, testCase := range cases {
		if len(pending) >= remaining {
			break
		}
		path := resultPath(outputDir, spec, testCase)
		if _, statErr := os.Stat(path); statErr == nil {
			continue
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return nil, fmt.Errorf("inspect attempt %s: %w", path, statErr)
		}
		pending = append(pending, indexedCase{value: testCase, number: index + 1})
	}
	return pending, nil
}

func runCase(
	parent context.Context,
	opts options,
	spec modelSpec,
	model agentcore.ChatModel,
	testCase benchmarkCase,
	caseNumber int,
	totalCases int,
) error {
	path := resultPath(opts.OutputDir, spec, testCase)
	if _, err := os.Stat(path); err == nil {
		fmt.Printf("skip %s %s: attempt already recorded\n", spec.ID(), testCase.ID)
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect attempt %s: %w", path, err)
	}

	contextText := assembleContext(testCase.ContextDocuments, testCase.TargetTokens, testCase.TaskPrompt)
	userPrompt := contextText + "\n\n--- 当前任务（必须以此为准）---\n" + testCase.TaskPrompt
	messages := []agentcore.Message{
		agentcore.SystemMsg(testCase.SystemPrompt),
		agentcore.UserMsg(userPrompt),
	}
	record := attemptRecord{
		Version:        benchmarkVersion,
		Case:           testCase,
		Provider:       spec.Provider,
		Model:          spec.Model,
		Status:         "started",
		StartedAt:      time.Now(),
		EstimatedInput: estimateMessages(messages),
	}
	if err := writeJSONAtomic(path, record); err != nil {
		return err
	}
	fmt.Printf("[%s] case %d/%d %s stage=%s target=%d estimated=%d\n",
		spec.ID(), caseNumber, totalCases, testCase.ID, testCase.Stage, testCase.TargetTokens, record.EstimatedInput)

	callCtx, cancel := context.WithTimeout(parent, opts.RequestTimeout)
	defer cancel()
	callOptions := []agentcore.CallOption{agentcore.WithMaxTokens(testCase.MaxOutputTokens)}
	if testCaseReasoning := strings.TrimSpace(spec.ReasoningEffort); testCaseReasoning != "" {
		callOptions = append(callOptions, agentcore.WithThinking(agentcore.ThinkingLevel(testCaseReasoning)))
	}
	if testCase.Structured {
		callOptions = append(callOptions, agentcore.WithJSONMode())
	}
	started := time.Now()
	response, callErr := model.Generate(callCtx, messages, nil, callOptions...)
	finished := time.Now()
	record.FinishedAt = &finished
	record.DurationMillis = time.Since(started).Milliseconds()
	if callErr != nil {
		record.Status = "error"
		record.Error = callErr.Error()
		record.WouldRetry = true
		record.ValidationIssues = []string{"model_call_error"}
	} else {
		record.Status = "success"
		record.StopReason = response.Message.StopReason
		record.Usage = response.Message.Usage
		record.Response = response.Message.TextContent()
		record.OutputRunes = utf8.RuneCountInString(record.Response)
		record.JSONValid, record.ValidationIssues = validateResponse(testCase, record.Response, record.StopReason)
		record.WouldRetry = len(record.ValidationIssues) > 0
	}
	if err := writeJSONAtomic(path, record); err != nil {
		return err
	}
	fmt.Printf("[%s] done %s status=%s duration=%s output_runes=%d retry=%t issues=%s\n",
		spec.ID(), testCase.ID, record.Status, time.Duration(record.DurationMillis)*time.Millisecond,
		record.OutputRunes, record.WouldRetry, strings.Join(record.ValidationIssues, ","))
	if opts.Cooldown > 0 {
		fmt.Printf("cooldown %s\n", opts.Cooldown)
		return waitFor(parent, opts.Cooldown)
	}
	return nil
}

func waitFor(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func resultPath(outputDir string, spec modelSpec, testCase benchmarkCase) string {
	return filepath.Join(outputDir, "results", spec.ID(), testCase.ID+".json")
}

func validateResponse(testCase benchmarkCase, response string, stopReason agentcore.StopReason) (bool, []string) {
	issues := make([]string, 0, 4)
	jsonValid := false
	if testCase.Structured {
		var value any
		jsonValid = json.Unmarshal([]byte(extractJSONObject(response)), &value) == nil
		if !jsonValid {
			issues = append(issues, "invalid_json")
		}
	}
	if utf8.RuneCountInString(strings.TrimSpace(response)) < 500 {
		issues = append(issues, "output_too_short")
	}
	if stopReason == agentcore.StopReasonLength {
		issues = append(issues, "truncated")
	}
	if testCase.Stage == "source_analysis" && testCase.AnalysisSegments > 0 {
		for segment := 1; segment <= testCase.AnalysisSegments; segment++ {
			marker := fmt.Sprintf("SEG-%03d", segment)
			if !strings.Contains(response, marker) {
				issues = append(issues, "missing_segment_coverage")
				break
			}
		}
	}
	if testCase.Stage == "initial_cocreate_draft" {
		for _, tag := range []string{"reply", "draft", "ready", "suggestions"} {
			if !strings.Contains(response, "<"+tag+">") || !strings.Contains(response, "</"+tag+">") {
				issues = append(issues, "incomplete_cocreate_protocol")
				break
			}
		}
	}
	return jsonValid, issues
}

func extractJSONObject(text string) string {
	trimmed := strings.TrimSpace(text)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	return strings.TrimSpace(trimmed)
}

type corpus struct {
	ProjectRoot       string
	SourceText        string
	Premise           string
	Characters        string
	WorldRules        string
	Outline           string
	LayeredOutline    string
	SimulationProfile string
	SourceFoundation  string
	WrittenChapters   string
	ChapterTenPlan    string
	ChapterTenDraft   string
}

func loadCorpus(projectRoot string) (corpus, error) {
	c := corpus{ProjectRoot: projectRoot}
	var err error
	if c.SourceText, err = readFirstMatching(filepath.Join(projectRoot, "uploads", "adaptation"), "*.txt"); err != nil {
		return c, fmt.Errorf("load source text: %w", err)
	}
	readOptional := func(relative string) string {
		data, readErr := os.ReadFile(filepath.Join(projectRoot, relative))
		if readErr != nil {
			return ""
		}
		return string(data)
	}
	c.Premise = readOptional(filepath.Join("output", "premise.md"))
	c.Characters = readOptional(filepath.Join("output", "characters.md"))
	c.WorldRules = readOptional(filepath.Join("output", "world_rules.md"))
	c.Outline = readOptional(filepath.Join("output", "outline.md"))
	c.LayeredOutline = readOptional(filepath.Join("output", "layered_outline.md"))
	c.SimulationProfile = readOptional(filepath.Join("output", "meta", "simulation_profile.json"))
	c.SourceFoundation = readOptional(filepath.Join("output", "meta", "adaptation", "source_foundation.json"))
	c.ChapterTenPlan = readOptional(filepath.Join("output", "drafts", "10.plan.json"))
	c.ChapterTenDraft = readOptional(filepath.Join("output", "drafts", "10.draft.md"))

	chapterFiles, _ := filepath.Glob(filepath.Join(projectRoot, "output", "chapters", "*.md"))
	sort.Strings(chapterFiles)
	var chapters strings.Builder
	for _, path := range chapterFiles {
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			continue
		}
		fmt.Fprintf(&chapters, "\n\n### %s\n%s", filepath.Base(path), data)
	}
	c.WrittenChapters = chapters.String()
	return c, nil
}

func readFirstMatching(dir, pattern string) (string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return "", err
	}
	if len(paths) == 0 {
		return "", fmt.Errorf("no %s under %s", pattern, dir)
	}
	sort.Strings(paths)
	data, err := os.ReadFile(paths[0])
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func buildSuite(c corpus) []benchmarkCase {
	creativeTargets := []int{48_000, 96_000, 160_000}
	stages := []struct {
		name       string
		structured bool
		maxOutput  int
		system     string
		task       string
		documents  []string
	}{
		{
			name:       "skeleton_planning",
			structured: true,
			maxOutput:  5_000,
			system:     planningSystemPrompt(),
			task:       skeletonTaskPrompt(),
			documents: labeledDocuments(
				"改编意图与基础", c.SourceFoundation,
				"作品画像", c.SimulationProfile,
				"原作正文", c.SourceText,
			),
		},
		{
			name:       "detail_outline",
			structured: true,
			maxOutput:  6_000,
			system:     planningSystemPrompt(),
			task:       detailOutlineTaskPrompt(),
			documents: labeledDocuments(
				"前提", c.Premise,
				"人物", c.Characters,
				"世界规则", c.WorldRules,
				"现有骨架", c.Outline,
				"分层提纲", c.LayeredOutline,
				"原作正文", c.SourceText,
			),
		},
		{
			name:       "initial_cocreate_draft",
			structured: false,
			maxOutput:  4_000,
			system:     coCreateSystemPrompt(),
			task:       coCreateDraftTaskPrompt(),
			documents: labeledDocuments(
				"改编意图与资料包", c.SourceFoundation,
				"作品画像", c.SimulationProfile,
				"人物", c.Characters,
				"世界规则", c.WorldRules,
				"原作正文", c.SourceText,
			),
		},
		{
			name:       "opening_draft",
			structured: false,
			maxOutput:  4_000,
			system:     writingSystemPrompt(),
			task:       openingTaskPrompt(),
			documents: labeledDocuments(
				"前提", c.Premise,
				"人物", c.Characters,
				"世界规则", c.WorldRules,
				"提纲", c.LayeredOutline,
				"原作正文", c.SourceText,
			),
		},
		{
			name:       "mid_continuation",
			structured: false,
			maxOutput:  4_000,
			system:     writingSystemPrompt(),
			task:       continuationTaskPrompt(),
			documents: labeledDocuments(
				"前提", c.Premise,
				"人物", c.Characters,
				"世界规则", c.WorldRules,
				"提纲", c.LayeredOutline,
				"已完成章节", c.WrittenChapters,
				"原作正文", c.SourceText,
			),
		},
		{
			name:       "arc_close_edit",
			structured: false,
			maxOutput:  4_000,
			system:     editingSystemPrompt(),
			task:       arcCloseTaskPrompt(),
			documents: labeledDocuments(
				"前提", c.Premise,
				"人物", c.Characters,
				"世界规则", c.WorldRules,
				"提纲", c.LayeredOutline,
				"已完成章节", c.WrittenChapters,
				"第十章计划", c.ChapterTenPlan,
				"第十章草稿", c.ChapterTenDraft,
				"原作正文", c.SourceText,
			),
		},
	}

	result := make([]benchmarkCase, 0, defaultCallsPerModel)
	doubleReplicaStages := map[string]bool{
		"skeleton_planning":      true,
		"detail_outline":         true,
		"initial_cocreate_draft": true,
		"mid_continuation":       true,
	}
	for _, stage := range stages {
		for _, target := range creativeTargets {
			replicas := 1
			if doubleReplicaStages[stage.name] {
				replicas = 2
			}
			for replica := 1; replica <= replicas; replica++ {
				result = append(result, benchmarkCase{
					ID:               fmt.Sprintf("%s-t%d-r%d", stage.name, target, replica),
					Stage:            stage.name,
					TargetTokens:     target,
					Replica:          replica,
					Structured:       stage.structured,
					MaxOutputTokens:  stage.maxOutput,
					SystemPrompt:     stage.system,
					TaskPrompt:       stage.task,
					ContextDocuments: stage.documents,
				})
			}
		}
	}

	for _, target := range []int{12_000, 24_000, 48_000, 72_000, 96_000} {
		segments := segmentSource(c.SourceText, target)
		for replica := 1; replica <= 2; replica++ {
			result = append(result, benchmarkCase{
				ID:               fmt.Sprintf("source-analysis-t%d-r%d", target, replica),
				Stage:            "source_analysis",
				TargetTokens:     target,
				Replica:          replica,
				Structured:       true,
				MaxOutputTokens:  7_000,
				SystemPrompt:     analysisSystemPrompt(),
				TaskPrompt:       analysisTaskPrompt(len(segments)),
				ContextDocuments: []string{strings.Join(segments, "\n\n")},
				AnalysisSegments: len(segments),
			})
		}
	}
	return result
}

func labeledDocuments(pairs ...string) []string {
	documents := make([]string, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		if strings.TrimSpace(pairs[i+1]) == "" {
			continue
		}
		documents = append(documents, "## "+pairs[i]+"\n"+pairs[i+1])
	}
	return documents
}

func assembleContext(documents []string, targetTokens int, taskPrompt string) string {
	const chunkRunes = 6_000
	remaining := make([][]rune, 0, len(documents))
	for _, document := range documents {
		remaining = append(remaining, []rune(document))
	}
	var builder strings.Builder
	for {
		added := false
		for index := range remaining {
			if len(remaining[index]) == 0 {
				continue
			}
			added = true
			take := min(chunkRunes, len(remaining[index]))
			candidate := builder.String() + "\n\n" + string(remaining[index][:take])
			if estimatePromptTokens(candidate, taskPrompt) > targetTokens {
				return trimContextToTarget(candidate, targetTokens, taskPrompt)
			}
			builder.WriteString("\n\n")
			builder.WriteString(string(remaining[index][:take]))
			remaining[index] = remaining[index][take:]
		}
		if !added {
			return builder.String()
		}
	}
}

func trimContextToTarget(contextText string, targetTokens int, taskPrompt string) string {
	runes := []rune(contextText)
	low, high := 0, len(runes)
	for low < high {
		mid := (low + high + 1) / 2
		if estimatePromptTokens(string(runes[:mid]), taskPrompt) <= targetTokens {
			low = mid
		} else {
			high = mid - 1
		}
	}
	return string(runes[:low])
}

func estimatePromptTokens(contextText, taskPrompt string) int {
	return estimateMessages([]agentcore.Message{
		agentcore.SystemMsg(planningSystemPrompt()),
		agentcore.UserMsg(contextText + "\n\n" + taskPrompt),
	})
}

func estimateMessages(messages []agentcore.Message) int {
	total := 0
	for _, message := range messages {
		total += corecontext.EstimateTokens(message)
	}
	return total
}

func segmentSource(source string, targetTokens int) []string {
	const segmentRunes = 3_000
	runes := []rune(source)
	segments := make([]string, 0, targetTokens/2_000)
	for start := 0; start < len(runes); start += segmentRunes {
		end := min(start+segmentRunes, len(runes))
		marker := fmt.Sprintf("[SEG-%03d]", len(segments)+1)
		candidate := append(append([]string(nil), segments...), marker+"\n"+string(runes[start:end]))
		if estimatePromptTokens(strings.Join(candidate, "\n\n"), analysisTaskPrompt(len(candidate))) > targetTokens {
			break
		}
		segments = candidate
	}
	return segments
}

func planningSystemPrompt() string {
	return "你是长篇中文小说总架构师。质量优先，必须一次输出可直接使用的结果；严格遵守事实、因果、人物弧与用户约束，不得用空泛套话替代设计。"
}

func writingSystemPrompt() string {
	return "你是长篇中文小说主笔。质量优先，必须一次交付可直接使用的正文；保持人物、时间线、因果和文风一致，不解释创作过程。"
}

func editingSystemPrompt() string {
	return "你是长篇中文小说责任编辑。质量优先，在不破坏既有事实和人物动机的前提下完成弧尾收束；直接给出修订后的成稿，不解释过程。"
}

func analysisSystemPrompt() string {
	return "你是长篇小说资料分析员。只根据提供的原文逐段提取事实；不补写、不猜测。一次输出完整合法 JSON，保证每个输入段都有对应记录。"
}

func coCreateSystemPrompt() string {
	return `你是小说改编首次共创助手。系统已提供全书资料包，用户已经选定建议；你必须把选择压缩成能直接交给后续提案、分卷大纲和详细提纲生成器执行的 draft，而不是写正文或逐章提纲。
输出必须严格包含完整的 <reply>、<draft>、<ready>、<suggestions> 四组 XML 标签。draft 质量优先：保留关键事实锚点、关系线、硬禁令、模式合同和验收标准，合并重复规则。`
}

func skeletonTaskPrompt() string {
	return `请为一部长篇科技商战小说设计第一卷骨架。保持原作核心人物与技术成长因果，但采用全新叙事表达。只输出合法 JSON：
{"volume_goal":"","central_conflict":"","character_arcs":[],"arcs":[{"index":1,"chapters":"1-10","goal":"","turning_point":"","payoff":"","continuity_constraints":[]}],"risks":[]}
要求：4 个弧、总计 40 章；每弧有清晰升级、选择、代价和回收；明确王廉、段天狼、方冲的动机变化；不得把人物能力写成无代价万能外挂。不得解释。`
}

func detailOutlineTaskPrompt() string {
	return `基于资料，为第一卷第 2 弧生成第 11-18 章详细提纲。只输出合法 JSON：
{"arc_goal":"","chapters":[{"chapter":11,"title":"","goal":"","conflict":"","beats":[],"character_change":"","continuity_checks":[],"hook":""}],"arc_payoff":"","unresolved_threads":[]}
必须恰好 8 章；每章至少 4 个可执行节拍；前后因果连续；包含技术推进、人际选择和对手反制；不能复制资料原句，不得解释。`
}

func coCreateDraftTaskPrompt() string {
	return `用户已选择建议：“保留原作技术成长与商战主线；强化段天狼和凌雪伤从对抗、互认到合作的关系线；避免无铺垫恋爱与万能外挂。”
当前固定模式：mode_contract=adaptation；granularity=arc；rewrite_policy=full_rewrite；word_tolerance=0.15。
请一次生成可直接执行的共创 draft。<draft> 控制在 1200-2200 个中文字符，必须包含：改编模式、核心目标、原作事实锚点、人物与关系规则、必须保留、禁止事项、质量验收。不得生成分卷大纲、逐章策略或正文。信息已经足够，<ready> 必须为 true；<reply> 简短确认；<suggestions> 留空。`
}

func openingTaskPrompt() string {
	return `写第一章开篇正文，约 2600-3200 个汉字。必须在前三段建立具体场景、主角当前困境和一个可感知的异常；让王廉的“寻找天才”通过行动显现，不做履历式介绍；结尾落在他第一次注意到段天狼的可验证线索上。禁止总结、提纲、解释和元叙述。`
}

func continuationTaskPrompt() string {
	return `续写第 10 章正文，约 2600-3200 个汉字。承接已完成章节的时间、持有物、关系和语言风格；段天狼对凌雪伤的兴趣必须来自能力识别与对抗性好奇，不得写成恋爱式心动；通过一场有规则、有反转的游戏对局推进关系；结尾形成下一章可执行悬念。禁止解释和复述提纲。`
}

func arcCloseTaskPrompt() string {
	return `将第 10 章草稿修订为弧尾成稿，约 2600-3200 个汉字。保留既定主线事实，修复重复、跳跃和人物失真；回收本弧“社会规则学习”线索，同时留下凌雪伤身份与能力的下一弧悬念；段天狼不得出现恋爱式生理反应或占有欲。直接输出完整修订正文。`
}

func analysisTaskPrompt(segmentCount int) string {
	return fmt.Sprintf(`分析上方 %d 个带编号原文段。只输出合法 JSON：
{"reports":[{"segment":"SEG-001","summary":"","characters":[],"events":[],"world_rules":[],"continuity":[],"uncertainties":[]}]}
reports 必须与输入段一一对应且按编号排列；每段 summary 1-2 句，events 最多 4 条；只写原文可验证事实，不跨段合并，不得遗漏编号，不得解释。`, segmentCount)
}

func writeManifest(outputDir string, opts options, cases []benchmarkCase) error {
	manifest := struct {
		Version       string          `json:"version"`
		CreatedAt     time.Time       `json:"created_at"`
		ProjectRoot   string          `json:"project_root"`
		Models        []modelSpec     `json:"models"`
		CallsPerModel int             `json:"calls_per_model"`
		Cases         []benchmarkCase `json:"cases"`
	}{
		Version:       benchmarkVersion,
		CreatedAt:     time.Now(),
		ProjectRoot:   opts.ProjectRoot,
		Models:        opts.ModelSpecs,
		CallsPerModel: opts.CallsPerModel,
		Cases:         cases,
	}
	return writeJSONAtomic(filepath.Join(outputDir, "manifest.json"), manifest)
}

func countAttempts(outputDir string, spec modelSpec) (int, error) {
	paths, err := filepath.Glob(filepath.Join(outputDir, "results", spec.ID(), "*.json"))
	if err != nil {
		return 0, fmt.Errorf("count attempts: %w", err)
	}
	return len(paths), nil
}

func writeJSONAtomic(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(path), err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal %s: %w", path, err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", temporary, err)
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("replace %s: %w", path, err)
	}
	return nil
}

func sanitizePathPart(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_", " ", "_")
	return replacer.Replace(value)
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "context-benchmark:", err)
	os.Exit(1)
}
