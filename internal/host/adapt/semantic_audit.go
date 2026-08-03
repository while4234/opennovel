package adapt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/adaptaudit"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/modeldiag"
	"github.com/voocel/ainovel-cli/internal/store"
)

const (
	semanticAuditVersion    = 1
	semanticAuditChunkRunes = 12_000
	semanticAuditMaxCalls   = 500
)

type SemanticAuditOptions struct {
	SourceTo                int     `json:"source_to,omitempty"`
	Provider                string  `json:"provider,omitempty"`
	Model                   string  `json:"model,omitempty"`
	ReasoningEffort         string  `json:"reasoning_effort,omitempty"`
	MaxCalls                int     `json:"max_calls,omitempty"`
	MaxCostUSD              float64 `json:"max_cost_usd,omitempty"`
	AcknowledgeUnknownPrice bool    `json:"acknowledge_unknown_price,omitempty"`
}

type SemanticAuditEstimate struct {
	Scope            adaptaudit.Scope `json:"scope"`
	ArtifactRunes    int              `json:"artifact_runes"`
	EstimatedCalls   int              `json:"estimated_calls"`
	PriceKnown       bool             `json:"price_known"`
	EstimatedCostUSD *float64         `json:"estimated_cost_usd,omitempty"`
}

type SemanticAuditProgress struct {
	CompletedCalls int `json:"completed_calls"`
	AttemptedCalls int `json:"attempted_calls"`
	TotalCalls     int `json:"total_calls"`
	CoveredRunes   int `json:"covered_runes"`
	TotalRunes     int `json:"total_runes"`
}

type SemanticAuditEvidence struct {
	ArtifactID     string `json:"artifact_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Quote          string `json:"quote"`
	FromRune       int    `json:"from_rune"`
	ToRune         int    `json:"to_rune"`
}

type SemanticAuditFinding struct {
	ID               string                `json:"id"`
	Fingerprint      string                `json:"fingerprint"`
	Code             string                `json:"code"`
	Severity         string                `json:"severity"`
	Message          string                `json:"message"`
	TargetChapters   []int                 `json:"target_chapters,omitempty"`
	Evidence         SemanticAuditEvidence `json:"evidence"`
	EvidenceVerified bool                  `json:"evidence_verified"`
}

type SemanticAuditJudgment struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	TargetChapters []int  `json:"target_chapters,omitempty"`
	VerifiedFact   bool   `json:"verified_fact"`
}

type SemanticAuditUsage struct {
	InputTokens   int      `json:"input_tokens"`
	OutputTokens  int      `json:"output_tokens"`
	TotalTokens   int      `json:"total_tokens"`
	CostUSD       *float64 `json:"cost_usd,omitempty"`
	PriceKnown    bool     `json:"price_known"`
	UnpricedCalls int      `json:"unpriced_calls,omitempty"`
}

type SemanticAuditRun struct {
	Version          int                                 `json:"version"`
	RunID            string                              `json:"run_id"`
	Status           string                              `json:"status"`
	ReadOnly         bool                                `json:"read_only"`
	Stale            bool                                `json:"stale"`
	Error            string                              `json:"error,omitempty"`
	StartedAt        string                              `json:"started_at"`
	CompletedAt      string                              `json:"completed_at,omitempty"`
	Scope            adaptaudit.Scope                    `json:"scope"`
	InputDigest      string                              `json:"input_digest"`
	Provider         string                              `json:"provider,omitempty"`
	Model            string                              `json:"model,omitempty"`
	ReasoningEffort  string                              `json:"reasoning_effort,omitempty"`
	Progress         SemanticAuditProgress               `json:"progress"`
	Usage            SemanticAuditUsage                  `json:"usage"`
	Findings         []SemanticAuditFinding              `json:"findings,omitempty"`
	Judgments        []SemanticAuditJudgment             `json:"judgments,omitempty"`
	RejectedEvidence int                                 `json:"rejected_evidence"`
	Options          SemanticAuditOptions                `json:"options"`
	CompletedStages  map[string]SemanticAuditStageResult `json:"completed_stages,omitempty"`
	PublishedRunID   string                              `json:"published_run_id,omitempty"`
}

type SemanticAuditStageResult struct {
	Stage           string                  `json:"stage"`
	Summary         string                  `json:"summary,omitempty"`
	SummarySource   string                  `json:"summary_source,omitempty"`
	SummaryVerified bool                    `json:"summary_verified"`
	FindingIDs      []string                `json:"finding_ids,omitempty"`
	Judgments       []SemanticAuditJudgment `json:"judgments,omitempty"`
}

type semanticArtifact struct {
	ID             string `json:"id"`
	SHA256         string `json:"sha256"`
	Text           string `json:"text"`
	TargetChapters []int  `json:"target_chapters,omitempty"`
}

type semanticModelOutput struct {
	Findings  []semanticModelFinding  `json:"findings"`
	Judgments []SemanticAuditJudgment `json:"judgments"`
	Summary   string                  `json:"summary"`
}

type semanticAuditUnit struct {
	ID        string
	Artifacts []semanticArtifact
}
type semanticArtifactChunk struct {
	ArtifactID     string `json:"artifact_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	FromRune       int    `json:"from_rune"`
	ToRune         int    `json:"to_rune"`
	Text           string `json:"text"`
	TargetChapters []int  `json:"target_chapters,omitempty"`
	ContextOnly    bool   `json:"context_only,omitempty"`
}

type semanticModelFinding struct {
	Code           string `json:"code"`
	Severity       string `json:"severity"`
	Message        string `json:"message"`
	ArtifactID     string `json:"artifact_id"`
	ArtifactSHA256 string `json:"artifact_sha256"`
	Quote          string `json:"quote"`
	FromRune       int    `json:"from_rune"`
	ToRune         int    `json:"to_rune"`
	TargetChapters []int  `json:"target_chapters"`
}

func EstimateSemanticAudit(st *store.Store, options SemanticAuditOptions) (*SemanticAuditEstimate, error) {
	artifacts, scope, _, err := semanticAuditArtifacts(st, options)
	if err != nil {
		return nil, err
	}
	units, err := semanticAuditUnits(st, scope, artifacts)
	if err != nil {
		return nil, err
	}
	total, calls := 0, 1
	for _, unit := range units {
		for _, artifact := range unit.Artifacts {
			total += utf8.RuneCountInString(artifact.Text)
		}
	}
	for _, unit := range units {
		calls += len(semanticUnitWindows(unit)) + 1
	}
	return &SemanticAuditEstimate{Scope: scope, ArtifactRunes: total, EstimatedCalls: calls, PriceKnown: false}, nil
}

func PrepareSemanticAudit(st *store.Store, options SemanticAuditOptions, provider, model string) (*SemanticAuditRun, error) {
	estimate, err := EstimateSemanticAudit(st, options)
	if err != nil {
		return nil, err
	}
	if options.MaxCostUSD > 0 && !estimate.PriceKnown {
		return nil, fmt.Errorf("model pricing is unavailable; set max_calls instead of max_cost_usd")
	}
	if !estimate.PriceKnown && !options.AcknowledgeUnknownPrice {
		return nil, fmt.Errorf("model pricing is unavailable; set acknowledge_unknown_price=true and use max_calls")
	}
	maxCalls := options.MaxCalls
	if maxCalls <= 0 {
		maxCalls = 100
	}
	if maxCalls > semanticAuditMaxCalls {
		return nil, fmt.Errorf("max_calls must be <= %d", semanticAuditMaxCalls)
	}
	if estimate.EstimatedCalls > maxCalls {
		return nil, fmt.Errorf("estimated calls %d exceed max_calls %d", estimate.EstimatedCalls, maxCalls)
	}
	_, _, digest, err := semanticAuditArtifacts(st, options)
	if err != nil {
		return nil, err
	}
	run := &SemanticAuditRun{Version: semanticAuditVersion, RunID: newSemanticRunID(), Status: "queued", ReadOnly: true,
		StartedAt: time.Now().UTC().Format(time.RFC3339), Scope: estimate.Scope, InputDigest: digest,
		Provider: provider, Model: model, ReasoningEffort: options.ReasoningEffort,
		Progress: SemanticAuditProgress{TotalCalls: estimate.EstimatedCalls, TotalRunes: estimate.ArtifactRunes}, Options: options, CompletedStages: make(map[string]SemanticAuditStageResult)}
	if err := SaveSemanticAuditRun(st, run); err != nil {
		return nil, err
	}
	return run, nil
}

func ExecuteSemanticAudit(ctx context.Context, st *store.Store, runID string, model agentcore.ChatModel) error {
	run, err := LoadSemanticAuditRun(st, runID)
	if err != nil {
		return err
	}
	if model == nil {
		return failSemanticRun(st, run, errors.New("auditor model is unavailable"))
	}
	artifacts, scope, digest, err := semanticAuditArtifacts(st, run.Options)
	if err != nil {
		return failSemanticRun(st, run, err)
	}
	if scope != run.Scope || digest != run.InputDigest {
		run.Status = "stale"
		run.Stale = true
		return SaveSemanticAuditRun(st, run)
	}
	run.Status = "running"
	run.Error = ""
	if run.CompletedStages == nil {
		run.CompletedStages = make(map[string]SemanticAuditStageResult)
	}
	if err := SaveSemanticAuditRun(st, run); err != nil {
		return err
	}
	units, err := semanticAuditUnits(st, scope, artifacts)
	if err != nil {
		return failSemanticRun(st, run, err)
	}
	artifactMap := make(map[string]semanticArtifact, len(artifacts))
	for _, artifact := range artifacts {
		artifactMap[artifact.ID] = artifact
	}
	unitSummaries := make([]string, 0, len(units))
	for _, unit := range units {
		localSummaries := make([]string, 0)
		for windowIndex, window := range semanticUnitWindows(unit) {
			stageKey := fmt.Sprintf("%s/window/%04d", unit.ID, windowIndex)
			if checkpoint, ok := run.CompletedStages[stageKey]; ok {
				if checkpoint.Summary != "" {
					localSummaries = append(localSummaries, checkpoint.Summary)
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				run.Status = "canceled"
				run.Error = err.Error()
				_ = SaveSemanticAuditRun(st, run)
				return err
			}
			output, usage, callErr := callVerifiedSemanticStage(ctx, st, model, "unit_window", map[string]any{"unit_id": unit.ID, "artifacts": window}, run.ReasoningEffort, run, artifactMap)
			if callErr != nil {
				return failSemanticRun(st, run, callErr)
			}
			if strings.TrimSpace(output.Summary) != "" {
				localSummaries = append(localSummaries, output.Summary)
			}
			addSemanticUsage(&run.Usage, usage)
			run.Progress.CompletedCalls++
			for _, chunk := range window {
				if !chunk.ContextOnly {
					run.Progress.CoveredRunes += chunk.ToRune - chunk.FromRune
				}
			}
			checkpointSemanticStage(run, stageKey, "unit_window", output)
			if err := SaveSemanticAuditRun(st, run); err != nil {
				return err
			}
		}
		stageKey := unit.ID + "/synthesis"
		if checkpoint, ok := run.CompletedStages[stageKey]; ok {
			unitSummaries = append(unitSummaries, checkpoint.Summary)
			continue
		}
		output, usage, callErr := callVerifiedSemanticStage(ctx, st, model, "unit_synthesis", map[string]any{"unit_id": unit.ID, "unverified_structured_summaries": localSummaries, "verified_findings": run.Findings, "unverified_model_assessments": run.Judgments}, run.ReasoningEffort, run, artifactMap)
		if callErr != nil {
			return failSemanticRun(st, run, callErr)
		}
		addSemanticUsage(&run.Usage, usage)
		run.Progress.CompletedCalls++
		unitSummaries = append(unitSummaries, strings.TrimSpace(output.Summary))
		checkpointSemanticStage(run, stageKey, "unit_synthesis", output)
		if err := SaveSemanticAuditRun(st, run); err != nil {
			return err
		}
	}
	globalKey := "global/synthesis"
	if _, ok := run.CompletedStages[globalKey]; !ok {
		globalOutput, usage, callErr := callVerifiedSemanticStage(ctx, st, model, "global_synthesis", map[string]any{"unverified_unit_summaries": unitSummaries, "verified_findings": run.Findings, "unverified_model_assessments": run.Judgments}, run.ReasoningEffort, run, artifactMap)
		if callErr != nil {
			return failSemanticRun(st, run, callErr)
		}
		addSemanticUsage(&run.Usage, usage)
		run.Progress.CompletedCalls++
		checkpointSemanticStage(run, globalKey, "global_synthesis", globalOutput)
		if err := SaveSemanticAuditRun(st, run); err != nil {
			return err
		}
	}
	_, _, currentDigest, err := semanticAuditArtifacts(st, run.Options)
	if err != nil || currentDigest != run.InputDigest {
		run.Status = "stale"
		run.Stale = true
	} else {
		run.Status = "completed"
	}
	run.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	sort.Slice(run.Findings, func(i, j int) bool { return run.Findings[i].Fingerprint < run.Findings[j].Fingerprint })
	if err := SaveSemanticAuditRun(st, run); err != nil {
		return err
	}
	if run.Status == "completed" && run.Progress.CoveredRunes == run.Progress.TotalRunes && run.RejectedEvidence == 0 {
		publishedRunID, publishErr := publishSemanticAuditHistory(st, run)
		if publishErr != nil {
			return failSemanticRun(st, run, fmt.Errorf("publish semantic audit history: %w", publishErr))
		}
		run.PublishedRunID = publishedRunID
		if err := SaveSemanticAuditRun(st, run); err != nil {
			return err
		}
	}
	return nil
}

func MarkSemanticAuditInterrupted(st *store.Store, runID string) (*SemanticAuditRun, error) {
	run, err := LoadSemanticAuditRun(st, runID)
	if err != nil {
		return nil, err
	}
	if run.Status == "running" || run.Status == "queued" {
		run.Status = "interrupted"
		run.Error = "service restarted before the audit completed"
		run.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		if err := SaveSemanticAuditRun(st, run); err != nil {
			return nil, err
		}
	}
	return run, nil
}

func ResumeSemanticAudit(st *store.Store, runID string) (*SemanticAuditRun, error) {
	run, err := LoadSemanticAuditRun(st, runID)
	if err != nil {
		return nil, err
	}
	switch run.Status {
	case "failed", "canceled", "interrupted":
	default:
		return nil, fmt.Errorf("semantic audit run %s cannot be retried from status %s", runID, run.Status)
	}
	run.Status = "queued"
	run.Error = ""
	run.CompletedAt = ""
	if err := SaveSemanticAuditRun(st, run); err != nil {
		return nil, err
	}
	return run, nil
}

func LoadSemanticAuditRun(st *store.Store, runID string) (*SemanticAuditRun, error) {
	if !validSemanticRunID(runID) {
		return nil, fmt.Errorf("invalid semantic audit run id")
	}
	data, err := os.ReadFile(semanticAuditRunPath(st, runID))
	if err != nil {
		return nil, err
	}
	var run SemanticAuditRun
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func SaveSemanticAuditRun(st *store.Store, run *SemanticAuditRun) error {
	if st == nil || run == nil || !validSemanticRunID(run.RunID) {
		return fmt.Errorf("valid semantic audit run is required")
	}
	path := semanticAuditRunPath(st, run.RunID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(run, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func semanticAuditArtifacts(st *store.Store, options SemanticAuditOptions) ([]semanticArtifact, adaptaudit.Scope, string, error) {
	scope, err := ResolveProjectAuditScope(st, AuditOptions{SourceTo: options.SourceTo})
	if err != nil {
		return nil, scope, "", err
	}
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return nil, scope, "", fmt.Errorf("load confirmed adaptation plan: %w", err)
	}
	artifacts := make([]semanticArtifact, 0, (scope.SourceTo-scope.SourceFrom+1)+(scope.TargetTo-scope.TargetFrom+1)*2)
	for ch := scope.SourceFrom; ch <= scope.SourceTo; ch++ {
		text, meta, e := st.Adaptation.LoadSourceChapter(ch)
		if e != nil {
			return nil, scope, "", e
		}
		if meta == nil || strings.TrimSpace(text) == "" {
			return nil, scope, "", fmt.Errorf("source chapter %d is unavailable", ch)
		}
		artifacts = append(artifacts, semanticArtifact{ID: fmt.Sprintf("source-%04d", ch), SHA256: store.TextSHA256(text), Text: text})
	}
	for ch := scope.TargetFrom; ch <= scope.TargetTo; ch++ {
		body, e := st.Drafts.LoadChapterText(ch)
		if e != nil {
			return nil, scope, "", e
		}
		if strings.TrimSpace(body) == "" {
			return nil, scope, "", fmt.Errorf("target chapter %d is unavailable", ch)
		}
		artifacts = append(artifacts, semanticArtifact{ID: fmt.Sprintf("target-body-%04d", ch), SHA256: store.TextSHA256(body), Text: body, TargetChapters: []int{ch}})
	}
	for _, chapter := range plan.Chapters {
		if chapter.Chapter < scope.TargetFrom || chapter.Chapter > scope.TargetTo {
			continue
		}
		raw, _ := json.Marshal(chapter)
		text := string(raw)
		artifacts = append(artifacts, semanticArtifact{ID: fmt.Sprintf("target-plan-%04d", chapter.Chapter), SHA256: store.TextSHA256(text), Text: text, TargetChapters: []int{chapter.Chapter}})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].ID < artifacts[j].ID })
	identity := struct {
		Scope     adaptaudit.Scope           `json:"scope"`
		Artifacts []struct{ ID, SHA string } `json:"artifacts"`
	}{Scope: scope}
	for _, a := range artifacts {
		identity.Artifacts = append(identity.Artifacts, struct{ ID, SHA string }{a.ID, a.SHA256})
	}
	raw, _ := json.Marshal(identity)
	return artifacts, scope, store.TextSHA256(string(raw)), nil
}

func semanticAuditUnits(st *store.Store, scope adaptaudit.Scope, artifacts []semanticArtifact) ([]semanticAuditUnit, error) {
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return nil, fmt.Errorf("load adaptation plan: %w", err)
	}
	byID := make(map[string]semanticArtifact, len(artifacts))
	for _, a := range artifacts {
		byID[a.ID] = a
	}
	makeUnit := func(id string, sourceFrom, sourceTo, targetFrom, targetTo int) semanticAuditUnit {
		u := semanticAuditUnit{ID: id}
		for ch := sourceFrom; ch <= sourceTo; ch++ {
			if a, ok := byID[fmt.Sprintf("source-%04d", ch)]; ok {
				u.Artifacts = append(u.Artifacts, a)
			}
		}
		for ch := targetFrom; ch <= targetTo; ch++ {
			if a, ok := byID[fmt.Sprintf("target-plan-%04d", ch)]; ok {
				u.Artifacts = append(u.Artifacts, a)
			}
			if a, ok := byID[fmt.Sprintf("target-body-%04d", ch)]; ok {
				u.Artifacts = append(u.Artifacts, a)
			}
		}
		return u
	}
	mode := domain.NormalizeAdaptationGranularity(plan.Granularity)
	units := make([]semanticAuditUnit, 0)
	switch mode {
	case domain.AdaptationGranularityArc:
		for i, b := range auditBatches(*plan) {
			if b.SourceFrom < scope.SourceFrom || b.SourceTo > scope.SourceTo || b.TargetFrom < scope.TargetFrom || b.TargetTo > scope.TargetTo {
				continue
			}
			units = append(units, makeUnit(fmt.Sprintf("arc-%04d", i+1), b.SourceFrom, b.SourceTo, b.TargetFrom, b.TargetTo))
		}
	case domain.AdaptationGranularityChapter:
		for _, chapter := range plan.Chapters {
			if chapter.Chapter < scope.TargetFrom || chapter.Chapter > scope.TargetTo {
				continue
			}
			sources := chapterSourceChapters(chapter)
			if len(sources) == 0 {
				continue
			}
			u := semanticAuditUnit{ID: fmt.Sprintf("chapter-%04d", chapter.Chapter)}
			for _, source := range sources {
				if source < scope.SourceFrom || source > scope.SourceTo {
					continue
				}
				if a, ok := byID[fmt.Sprintf("source-%04d", source)]; ok {
					u.Artifacts = append(u.Artifacts, a)
				}
			}
			if a, ok := byID[fmt.Sprintf("target-plan-%04d", chapter.Chapter)]; ok {
				u.Artifacts = append(u.Artifacts, a)
			}
			if a, ok := byID[fmt.Sprintf("target-body-%04d", chapter.Chapter)]; ok {
				u.Artifacts = append(u.Artifacts, a)
			}
			if len(u.Artifacts) >= 3 {
				units = append(units, u)
			}
		}
	default:
		units = append(units, makeUnit("free-0001", scope.SourceFrom, scope.SourceTo, scope.TargetFrom, scope.TargetTo))
	}
	if len(units) == 0 {
		return nil, fmt.Errorf("no complete semantic audit units are available")
	}
	return units, nil
}

func semanticUnitWindows(unit semanticAuditUnit) [][]semanticArtifactChunk {
	if len(unit.Artifacts) == 0 {
		return nil
	}
	quota := max(256, semanticAuditChunkRunes/len(unit.Artifacts))
	windows := 0
	for _, a := range unit.Artifacts {
		windows = max(windows, max(1, (utf8.RuneCountInString(a.Text)+quota-1)/quota))
	}
	out := make([][]semanticArtifactChunk, 0, windows)
	for index := 0; index < windows; index++ {
		window := make([]semanticArtifactChunk, 0, len(unit.Artifacts))
		for _, a := range unit.Artifacts {
			r := []rune(a.Text)
			from := index * quota
			contextOnly := false
			if from >= len(r) {
				from = 0
				contextOnly = true
			}
			to := min(len(r), from+quota)
			if contextOnly {
				to = min(len(r), 512)
			}
			window = append(window, semanticArtifactChunk{ArtifactID: a.ID, ArtifactSHA256: a.SHA256, FromRune: from, ToRune: to, Text: string(r[from:to]), TargetChapters: a.TargetChapters, ContextOnly: contextOnly})
		}
		out = append(out, window)
	}
	return out
}

var (
	errSemanticAuditEmptyResponse = errors.New("semantic auditor returned no response")
	errSemanticAuditInvalidJSON   = errors.New("semantic auditor returned invalid JSON")
)

func semanticAuditorPrompt(stage string) string {
	return "You are a read-only novel adaptation auditor. Stage=" + stage + ". Compare source material, target plan, and target prose together. For global_synthesis check cross-unit character state, causality, foreshadowing, repetition and contradictions. Return one JSON object only. Findings must quote exact text from a supplied artifact and provide absolute rune offsets [from_rune,to_rune). Claims that something is absent must go in judgments, never findings. Include a concise structured summary. Schema: {\"summary\":string,\"findings\":[{\"code\":string,\"severity\":\"info|warning|blocking\",\"message\":string,\"artifact_id\":string,\"artifact_sha256\":string,\"quote\":string,\"from_rune\":int,\"to_rune\":int,\"target_chapters\":[int]}],\"judgments\":[{\"code\":string,\"message\":string,\"target_chapters\":[int],\"verified_fact\":false}]}"
}

func callSemanticAuditor(ctx context.Context, model agentcore.ChatModel, stage string, payloadValue any, reasoning string) (semanticModelOutput, *agentcore.Usage, error) {
	out, usage, _, err := callSemanticAuditorRaw(ctx, model, stage, payloadValue, reasoning)
	return out, usage, err
}

func callSemanticAuditorRaw(ctx context.Context, model agentcore.ChatModel, stage string, payloadValue any, reasoning string) (semanticModelOutput, *agentcore.Usage, string, error) {
	payload, _ := json.Marshal(payloadValue)
	prompt := semanticAuditorPrompt(stage)
	opts := []agentcore.CallOption{agentcore.WithMaxTokens(2200), agentcore.WithJSONMode()}
	if strings.TrimSpace(reasoning) != "" {
		opts = append(opts, agentcore.WithThinking(agentcore.ThinkingLevel(reasoning)))
	}
	resp, err := model.Generate(ctx, []agentcore.Message{agentcore.SystemMsg(prompt), agentcore.UserMsg(string(payload))}, nil, opts...)
	if err != nil {
		return semanticModelOutput{}, nil, "", err
	}
	if resp == nil {
		return semanticModelOutput{}, nil, "", errSemanticAuditEmptyResponse
	}
	var out semanticModelOutput
	rawResponse := resp.Message.TextContent()
	raw := extractSemanticJSONObject(rawResponse)
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return out, resp.Message.Usage, rawResponse, fmt.Errorf("%w: %v", errSemanticAuditInvalidJSON, err)
	}
	return out, resp.Message.Usage, rawResponse, nil
}

func callVerifiedSemanticStage(ctx context.Context, st *store.Store, model agentcore.ChatModel, stage string, payload any, reasoning string, run *SemanticAuditRun, artifacts map[string]semanticArtifact) (semanticModelOutput, *agentcore.Usage, error) {
	var combined agentcore.Usage
	for attempt := 0; attempt < 2; attempt++ {
		maxCalls := run.Options.MaxCalls
		if maxCalls <= 0 {
			maxCalls = 100
		}
		if run.Progress.AttemptedCalls >= maxCalls {
			return semanticModelOutput{}, &combined, fmt.Errorf("semantic audit max_calls %d exhausted", maxCalls)
		}
		run.Progress.AttemptedCalls++
		encoded, _ := json.Marshal(payload)
		prompt := semanticAuditorPrompt(stage)
		recorder, beginErr := modeldiag.Begin(modeldiag.Request{Store: st, Task: "adaptation_map_reduce_" + stage, RevisionID: run.RunID, Batch: run.Progress.AttemptedCalls, System: prompt, User: encoded, InputLimitBytes: manuscriptSemanticAuditBudgetBytes, OutputLimitTokens: 2200, SelectorCounts: map[string]int{"source_units": run.Progress.TotalRunes, "model_calls": run.Progress.AttemptedCalls}, SplitReason: stage, ContractSignature: run.InputDigest})
		if beginErr != nil {
			return semanticModelOutput{}, &combined, beginErr
		}
		out, usage, rawOutput, err := callSemanticAuditorRaw(ctx, model, stage, payload, reasoning)
		status := modeldiag.StatusCompleted
		if err != nil {
			status = modeldiag.StatusProviderError
			if errors.Is(err, errSemanticAuditInvalidJSON) {
				status = modeldiag.StatusDecodeError
			} else if errors.Is(err, errSemanticAuditEmptyResponse) {
				status = modeldiag.StatusEmptyResponse
			}
		}
		if diagnosticErr := recorder.Finish(status, rawOutput, usage); diagnosticErr != nil {
			return semanticModelOutput{}, &combined, diagnosticErr
		}
		combined.Add(usage)
		if err != nil {
			if attempt == 0 {
				continue
			}
			return out, &combined, err
		}
		findingCount, judgmentCount, rejected := len(run.Findings), len(run.Judgments), run.RejectedEvidence
		if acceptSemanticOutput(run, out, artifacts) {
			return out, &combined, nil
		}
		run.Findings = run.Findings[:findingCount]
		run.Judgments = run.Judgments[:judgmentCount]
		run.RejectedEvidence = rejected
	}
	return semanticModelOutput{}, &combined, errors.New("auditor returned invalid evidence after 2 attempts")
}

const manuscriptSemanticAuditBudgetBytes = 60 * 1024

func acceptSemanticOutput(run *SemanticAuditRun, out semanticModelOutput, artifacts map[string]semanticArtifact) bool {
	rejectedBefore := run.RejectedEvidence
	for _, f := range out.Findings {
		artifact, ok := artifacts[f.ArtifactID]
		runes := []rune(artifact.Text)
		if !ok || f.ArtifactSHA256 != artifact.SHA256 || f.FromRune < 0 || f.ToRune > len(runes) || f.ToRune <= f.FromRune || string(runes[f.FromRune:f.ToRune]) != f.Quote {
			run.RejectedEvidence++
			continue
		}
		fp := store.TextSHA256(strings.Join([]string{strings.TrimSpace(f.Code), artifact.ID, fmt.Sprint(f.FromRune), fmt.Sprint(f.ToRune)}, "|"))
		run.Findings = append(run.Findings, SemanticAuditFinding{ID: "sem-" + fp[:12], Fingerprint: fp, Code: strings.TrimSpace(f.Code), Severity: strings.TrimSpace(f.Severity), Message: strings.TrimSpace(f.Message), TargetChapters: f.TargetChapters, Evidence: SemanticAuditEvidence{ArtifactID: artifact.ID, ArtifactSHA256: artifact.SHA256, Quote: f.Quote, FromRune: f.FromRune, ToRune: f.ToRune}, EvidenceVerified: true})
	}
	for _, j := range out.Judgments {
		j.VerifiedFact = false
		run.Judgments = append(run.Judgments, j)
	}
	return run.RejectedEvidence == rejectedBefore
}

func checkpointSemanticStage(run *SemanticAuditRun, key, stage string, out semanticModelOutput) {
	if run.CompletedStages == nil {
		run.CompletedStages = make(map[string]SemanticAuditStageResult)
	}
	result := SemanticAuditStageResult{Stage: stage, Summary: strings.TrimSpace(out.Summary), SummarySource: "model_assessment", SummaryVerified: false, Judgments: append([]SemanticAuditJudgment(nil), out.Judgments...)}
	for _, finding := range out.Findings {
		fp := store.TextSHA256(strings.Join([]string{strings.TrimSpace(finding.Code), finding.ArtifactID, fmt.Sprint(finding.FromRune), fmt.Sprint(finding.ToRune)}, "|"))
		result.FindingIDs = append(result.FindingIDs, "sem-"+fp[:12])
	}
	run.CompletedStages[key] = result
}

func addSemanticUsage(dst *SemanticAuditUsage, u *agentcore.Usage) {
	if u == nil {
		dst.UnpricedCalls++
		dst.PriceKnown = false
		return
	}
	dst.InputTokens += u.Input
	dst.OutputTokens += u.Output
	dst.TotalTokens += u.TotalTokens
	if u.Cost != nil {
		v := u.Cost.Total
		if dst.CostUSD != nil {
			v += *dst.CostUSD
		}
		dst.CostUSD = &v
	} else {
		dst.UnpricedCalls++
	}
	dst.PriceKnown = dst.UnpricedCalls == 0 && dst.CostUSD != nil
}
func failSemanticRun(st *store.Store, run *SemanticAuditRun, err error) error {
	run.Status = "failed"
	if errors.Is(err, context.Canceled) {
		run.Status = "canceled"
	}
	run.Error = err.Error()
	run.CompletedAt = time.Now().UTC().Format(time.RFC3339)
	_ = SaveSemanticAuditRun(st, run)
	return err
}
func semanticAuditRunPath(st *store.Store, id string) string {
	return filepath.Join(st.Dir(), "meta", "adaptation", "audits", "semantic", id+".json")
}
func validSemanticRunID(id string) bool {
	return strings.HasPrefix(id, "sem_") && len(id) == 28 && !strings.ContainsAny(id, "/\\.")
}
func newSemanticRunID() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "sem_" + hex.EncodeToString(b)
}
func extractSemanticJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < start {
		return s
	}
	return s[start : end+1]
}

func publishSemanticAuditHistory(st *store.Store, run *SemanticAuditRun) (string, error) {
	plan, err := st.Adaptation.LoadPlan()
	if err != nil || plan == nil {
		return "", fmt.Errorf("load adaptation plan: %w", err)
	}
	findings := make([]adaptaudit.Finding, 0, len(run.Findings))
	for _, finding := range run.Findings {
		blocking := finding.Severity == "blocking" || finding.Severity == "critical"
		findings = append(findings, adaptaudit.Finding{ID: finding.ID, Fingerprint: finding.Fingerprint, Code: finding.Code, Severity: finding.Severity, Blocking: blocking, Message: finding.Message, TargetChapters: finding.TargetChapters, Evidence: []adaptaudit.Evidence{{ArtifactID: finding.Evidence.ArtifactID, Quote: finding.Evidence.Quote}}, Source: "model_evidence"})
	}
	for _, judgment := range run.Judgments {
		fp := store.TextSHA256(strings.Join([]string{"model_assessment", judgment.Code, judgment.Message, fmt.Sprint(judgment.TargetChapters)}, "|"))
		findings = append(findings, adaptaudit.Finding{ID: "assessment-" + fp[:12], Fingerprint: fp, Code: judgment.Code, Severity: "warning", Blocking: false, Message: judgment.Message, TargetChapters: judgment.TargetChapters, Source: "model_assessment"})
	}
	report := adaptaudit.NewModelSecondPassReport(adaptaudit.Mode(domain.NormalizeAdaptationGranularity(plan.Granularity)), run.Scope, run.InputDigest, findings)
	completedAt, _ := time.Parse(time.RFC3339, run.CompletedAt)
	startedAt, _ := time.Parse(time.RFC3339, run.StartedAt)
	historyRun, err := adaptaudit.NewAuditRunAt(report, adaptaudit.AuditKindModelSecondPass, adaptaudit.AuditTriggerManual, &adaptaudit.ModelSnapshot{Provider: run.Provider, Model: run.Model, ReasoningEffort: run.ReasoningEffort}, startedAt, completedAt)
	if err != nil {
		return "", err
	}
	historyRun.Usage = adaptaudit.AuditUsage{InputTokens: run.Usage.InputTokens, OutputTokens: run.Usage.OutputTokens, TotalTokens: run.Usage.TotalTokens, CallCount: run.Progress.CompletedCalls, CostUSD: run.Usage.CostUSD, PriceKnown: run.Usage.PriceKnown}
	if err := st.Adaptation.SaveAuditRun(historyRun); err != nil {
		return "", err
	}
	return historyRun.RunID, nil
}
