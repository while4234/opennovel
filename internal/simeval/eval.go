package simeval

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/voocel/agentcore"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	hostsim "github.com/voocel/ainovel-cli/internal/host/sim"
	"github.com/voocel/ainovel-cli/internal/simulationcheck"
	"github.com/voocel/ainovel-cli/internal/store"
	"github.com/voocel/ainovel-cli/internal/tools"
)

const (
	MaxPortableBytes = 256 << 10
	MaxContractBytes = 32 << 10
	MaxContextBytes  = 64 << 10
	MaxSnapshotBytes = 64 << 10
	MaxCheckBytes    = 64 << 10
)

type Result struct {
	Name       string         `json:"name"`
	Status     string         `json:"status"`
	DurationMS int64          `json:"duration_ms"`
	Bytes      int            `json:"bytes,omitempty"`
	Items      int            `json:"items,omitempty"`
	Details    map[string]any `json:"details,omitempty"`
	Failure    string         `json:"failure,omitempty"`
}

type Report struct {
	Schema     string         `json:"schema"`
	Status     string         `json:"status"`
	Fixture    string         `json:"fixture"`
	Invariants []Result       `json:"invariants"`
	Budgets    map[string]int `json:"budgets"`
	Summary    Summary        `json:"summary"`
}

type Summary struct {
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

type evaluator struct {
	fixture            string
	root               string
	st                 *store.Store
	profile            *domain.SimulationProfileV2
	evidence           *domain.SimulationLocalEvidence
	normal, reinforced domain.SimulationContract
	contexts           map[string][]byte
}

func Run(ctx context.Context, fixture string) Report {
	report := Report{
		Schema: "simulation_eval.v1", Fixture: filepath.ToSlash(fixture),
		Status: "pass", Budgets: map[string]int{
			"portable_profile": MaxPortableBytes, "contract": MaxContractBytes,
			"role_context": MaxContextBytes, "snapshot": MaxSnapshotBytes,
			"check_report": MaxCheckBytes,
		},
	}
	root, err := os.MkdirTemp("", "ainovel-simulation-eval-*")
	if err != nil {
		report.Status, report.Summary.Failed = "fail", 1
		report.Invariants = []Result{{Name: "setup", Status: "fail", Failure: err.Error()}}
		return report
	}
	defer os.RemoveAll(root)
	e := &evaluator{fixture: fixture, root: root, contexts: make(map[string][]byte)}
	checks := []struct {
		name string
		fn   func(context.Context) (map[string]any, int, int, error)
	}{
		{"fixture_originality", e.checkFixture},
		{"analysis_and_health", e.checkAnalysis},
		{"evidence_order_invariance", e.checkEvidence},
		{"mode_contract_and_context", e.checkModes},
		{"boundary_no_leakage", e.checkNoLeakage},
		{"copy_scanner_and_commit_gate", e.checkGate},
		{"migration_and_health", e.checkMigration},
		{"payload_budgets", e.checkBudgets},
		{"bounded_performance", e.checkPerformance},
	}
	for _, check := range checks {
		start := time.Now()
		details, bytes, items, err := check.fn(ctx)
		item := Result{Name: check.name, Status: "pass", DurationMS: time.Since(start).Milliseconds(), Bytes: bytes, Items: items, Details: details}
		if err != nil {
			item.Status, item.Failure = "fail", err.Error()
			report.Status = "fail"
			report.Summary.Failed++
		} else {
			report.Summary.Passed++
		}
		report.Invariants = append(report.Invariants, item)
	}
	return report
}

func (e *evaluator) checkFixture(context.Context) (map[string]any, int, int, error) {
	readme, err := os.ReadFile(filepath.Join(e.fixture, "README.md"))
	if err != nil {
		return nil, 0, 0, err
	}
	if !strings.Contains(string(readme), "synthetic and original") {
		return nil, 0, 0, fmt.Errorf("fixture source declaration is missing")
	}
	corpusDir := filepath.Join(e.fixture, "corpus")
	entries, err := os.ReadDir(corpusDir)
	if err != nil {
		return nil, 0, 0, err
	}
	var total, count int
	required := map[string]bool{"body-alpha": false, "opening-only": false, "stage-fast": false, "stage-slow": false, "outlier": false, "preface": false}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".txt" {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(corpusDir, entry.Name()))
		if readErr != nil {
			return nil, 0, 0, readErr
		}
		total, count = total+len(data), count+1
		for id := range required {
			if strings.Contains(string(data), "FIXTURE_ID="+id) {
				required[id] = true
			}
		}
	}
	for id, found := range required {
		if !found {
			return nil, total, count, fmt.Errorf("fixture identity %q is missing", id)
		}
	}
	return map[string]any{"files": count, "source": "repository-original"}, total, count, nil
}

func (e *evaluator) checkAnalysis(ctx context.Context) (map[string]any, int, int, error) {
	e.st = store.NewStore(filepath.Join(e.root, "output", "novel"))
	if err := e.st.Init(); err != nil {
		return nil, 0, 0, err
	}
	llm := &fakeLLM{}
	events, err := hostsim.Run(ctx, hostsim.Deps{
		Store: e.st, LLM: llm, ModelIdentity: "offline/fake-identity-v1",
		ModelCallMaxAttempts: 1, StructureRepairMaxAttempts: 1,
		Prompts: hostsim.Prompts{Source: "offline-source-v1", Merge: "offline-merge-v1"},
	}, hostsim.Options{SourceDir: filepath.Join(e.fixture, "corpus")})
	if err != nil {
		return nil, 0, 0, err
	}
	for event := range events {
		if event.Err != nil {
			return nil, 0, 0, event.Err
		}
	}
	e.profile, err = e.st.Simulation.LoadPortable()
	if err != nil || e.profile == nil {
		return nil, 0, 0, fmt.Errorf("load portable profile: %w", err)
	}
	e.evidence, err = e.st.Simulation.LoadLocalEvidence()
	if err != nil || e.evidence == nil {
		return nil, 0, 0, fmt.Errorf("load local evidence: %w", err)
	}
	if e.profile.Health.State != "fresh" || e.profile.Corpus.SourceCount != 8 {
		return nil, 0, 0, fmt.Errorf("unexpected profile health/count: %s/%d", e.profile.Health.State, e.profile.Corpus.SourceCount)
	}
	data, _ := domain.MarshalSimulationPortableProfile(*e.profile)
	return map[string]any{"llm_calls": llm.Calls(), "features": len(e.profile.Features), "health": e.profile.Health.State}, len(data), len(e.profile.Features), nil
}

func (e *evaluator) checkEvidence(context.Context) (map[string]any, int, int, error) {
	reports := append([]domain.SimulationSourceReport(nil), e.evidence.SourceReports...)
	a, refsA, _ := domain.AggregateSimulationEvidence(reports, time.Unix(1, 0).UTC())
	for left, right := 0, len(reports)-1; left < right; left, right = left+1, right-1 {
		reports[left], reports[right] = reports[right], reports[left]
	}
	b, refsB, _ := domain.AggregateSimulationEvidence(reports, time.Unix(1, 0).UTC())
	if !reflect.DeepEqual(a, b) || !reflect.DeepEqual(refsA, refsB) {
		return nil, 0, 0, fmt.Errorf("evidence aggregation changed with report order")
	}
	for _, batchSize := range []int{1, 3, 7} {
		var batched []domain.SimulationSourceReport
		for start := 0; start < len(reports); start += batchSize {
			end := min(start+batchSize, len(reports))
			batched = append(batched, reports[start:end]...)
		}
		got, gotRefs, _ := domain.AggregateSimulationEvidence(batched, time.Unix(1, 0).UTC())
		if !reflect.DeepEqual(a, got) || !reflect.DeepEqual(refsA, gotRefs) {
			return nil, 0, 0, fmt.Errorf("evidence aggregation changed with batch size %d", batchSize)
		}
	}
	classes := map[string]int{}
	for _, feature := range a {
		classes[feature.Classification]++
	}
	if classes["stable"] == 0 || classes["local"] == 0 || classes["contradictory"] < 2 || classes["outlier"] == 0 {
		return map[string]any{"classes": classes}, 0, len(a), fmt.Errorf("fixture did not exercise expected evidence classes: %v", classes)
	}
	return map[string]any{"classes": classes, "evidence_refs": len(refsA)}, 0, len(a), nil
}

func (e *evaluator) checkModes(ctx context.Context) (map[string]any, int, int, error) {
	var err error
	e.normal, err = domain.CompileSimulationContract(domain.SimulationContractInput{Profile: e.profile, RequestedMode: domain.SimulationModeNormal, FoundationRevision: 1, Now: time.Unix(2, 0).UTC()})
	if err != nil {
		return nil, 0, 0, err
	}
	e.reinforced, err = domain.CompileSimulationContract(domain.SimulationContractInput{Profile: e.profile, RequestedMode: domain.SimulationModeReinforced, FoundationRevision: 1, Now: time.Unix(2, 0).UTC()})
	if err != nil {
		return nil, 0, 0, err
	}
	if contractMustCount(e.normal) != 0 || contractMustCount(e.reinforced) == 0 {
		return nil, 0, 0, fmt.Errorf("normal/reinforced must policy is not distinct")
	}
	if err := e.st.Outline.SaveOutline([]domain.OutlineEntry{{
		Chapter: 1, Title: "Synthetic", CoreEvent: "A choice follows a consequence",
		Scenes: []string{"reveal changes the route", "choice accepts the cost"},
	}}); err != nil {
		return nil, 0, 0, err
	}
	if err := e.st.Progress.Init("simulation eval", 1); err != nil {
		return nil, 0, 0, err
	}
	specs := []struct{ mode, role, args, key string }{
		{"normal", domain.SimulationRoleArchitect, `{"scope":"planning"}`, "normal_architect"},
		{"reinforced", domain.SimulationRoleArchitect, `{"scope":"planning"}`, "reinforced_architect"},
		{"normal", domain.SimulationRoleWriter, `{"chapter":1}`, "normal_writer"},
		{"reinforced", domain.SimulationRoleWriter, `{"chapter":1}`, "reinforced_writer"},
		{"normal", domain.SimulationRoleEditor, `{"chapter":1}`, "normal_editor"},
		{"reinforced", domain.SimulationRoleEditor, `{"chapter":1}`, "reinforced_editor"},
		{"reinforced", domain.SimulationRoleCoordinator, `{"scope":"status"}`, "coordinator"},
	}
	counts := map[string]int{}
	for _, spec := range specs {
		raw, executeErr := tools.NewContextToolWithOptions(e.st, tools.References{}, "default", tools.ContextToolOptions{SimulationMode: spec.mode, Role: spec.role}).Execute(ctx, json.RawMessage(spec.args))
		if executeErr != nil {
			return nil, 0, 0, executeErr
		}
		e.contexts[spec.key] = raw
		counts[spec.key] = selectedCount(raw)
		if len(raw) > MaxContextBytes {
			return nil, len(raw), 0, fmt.Errorf("%s context exceeds %d bytes", spec.key, MaxContextBytes)
		}
	}
	for _, role := range []string{"architect", "writer", "editor"} {
		if counts["reinforced_"+role] <= counts["normal_"+role] {
			return map[string]any{"selected_counts": counts}, 0, 0, fmt.Errorf("reinforced %s context is not richer than normal", role)
		}
	}
	if counts["coordinator"] != 0 {
		return map[string]any{"selected_counts": counts}, 0, 0, fmt.Errorf("coordinator received feature guidance")
	}
	normalPrompt := host.SimulationCoCreatePromptForEvaluation(e.st, domain.SimulationModeNormal)
	reinforcedPrompt := host.SimulationCoCreatePromptForEvaluation(e.st, domain.SimulationModeReinforced)
	if strings.Contains(normalPrompt, "portable_features_only") ||
		!strings.Contains(reinforcedPrompt, `"safety_boundary": "portable_features_only"`) ||
		!strings.Contains(reinforcedPrompt, `"must"`) {
		return map[string]any{"selected_counts": counts}, 0, 0, fmt.Errorf("co-create injection policy regressed")
	}
	e.contexts["normal_cocreate"] = []byte(normalPrompt)
	e.contexts["reinforced_cocreate"] = []byte(reinforcedPrompt)
	return map[string]any{"selected_counts": counts, "normal_must": 0, "reinforced_must": contractMustCount(e.reinforced)}, 0, len(specs), nil
}

func (e *evaluator) checkNoLeakage(context.Context) (map[string]any, int, int, error) {
	portable, err := domain.MarshalSimulationPortableProfile(*e.profile)
	if err != nil {
		return nil, 0, 0, err
	}
	payloads := map[string][]byte{"portable": portable}
	for name, data := range e.contexts {
		payloads[name] = data
	}
	snapshot, err := json.Marshal(host.SimulationProfileSummaryForEvaluation(e.st, domain.SimulationModeReinforced, 1))
	if err != nil {
		return nil, 0, 0, err
	}
	if len(snapshot) > MaxSnapshotBytes {
		return nil, len(snapshot), 0, fmt.Errorf("host snapshot exceeds %d bytes", MaxSnapshotBytes)
	}
	payloads["host_snapshot"] = snapshot
	payloads["web_api_summary"] = snapshot
	payloads["portable_library_file"] = portable
	forbidden := []string{`"source_reports":`, `"source_dir":`, `"entries":`, "Lumen Orchard", "琥珀潮汐折叠七枚纸月", `D:\`, "/Users/", "sk-test-"}
	var total int
	for _, data := range payloads {
		total += len(data)
	}
	if err := rejectBoundaryLeaks(payloads, forbidden); err != nil {
		return nil, total, len(payloads), err
	}
	return map[string]any{"payloads_scanned": len(payloads), "patterns": len(forbidden)}, total, len(payloads), nil
}

func rejectBoundaryLeaks(payloads map[string][]byte, forbidden []string) error {
	for name, data := range payloads {
		lower := strings.ToLower(string(data))
		for _, token := range forbidden {
			if strings.Contains(lower, strings.ToLower(token)) {
				return fmt.Errorf("%s leaked forbidden token %q", name, token)
			}
		}
	}
	return nil
}

func (e *evaluator) checkGate(ctx context.Context) (map[string]any, int, int, error) {
	const safeDraft = "The courier reveals a fact, faces its consequence, and chooses a new route in wholly original wording."
	if err := e.st.Drafts.SaveDraft(1, safeDraft); err != nil {
		return nil, 0, 0, err
	}
	if err := e.st.Drafts.SaveChapterPlan(domain.ChapterPlan{Chapter: 1, Hook: "choice", Contract: domain.ChapterContract{RequiredBeats: []string{"reveal one fact"}, ContinuityChecks: []string{"record consequence"}}}); err != nil {
		return nil, 0, 0, err
	}
	service := tools.NewSimulationCheckService(e.st, domain.SimulationModeReinforced)
	report, err := service.Check(ctx, 1)
	if err != nil || !report.Passed {
		return nil, 0, 0, fmt.Errorf("safe reinforced check failed: report=%v err=%v", report, err)
	}
	if err := service.EnsureCurrent(ctx, 1, safeDraft); err != nil {
		return nil, 0, 0, fmt.Errorf("current report rejected: %w", err)
	}
	if err := service.EnsureCurrent(ctx, 1, safeDraft+" changed"); err == nil || !strings.Contains(err.Error(), "draft_changed") {
		return nil, 0, 0, fmt.Errorf("changed draft reused stale report: %v", err)
	}
	engine := simulationcheck.NewEngine()
	risks, err := engine.Scan(ctx, "Punctuation variant: 琥珀，潮汐 折叠七枚纸月。", e.evidence.SafetyIndex, e.profile.Corpus.SourceCount)
	if err != nil || len(risks) == 0 {
		return nil, 0, 0, fmt.Errorf("rare phrase Unicode variant was not detected: risks=%d err=%v", len(risks), err)
	}
	negative, err := engine.Scan(ctx, "A different scene uses a similar technique with unrelated expression.", e.evidence.SafetyIndex, e.profile.Corpus.SourceCount)
	if err != nil || len(negative) != 0 {
		return nil, 0, 0, fmt.Errorf("scanner false positive: risks=%d err=%v", len(negative), err)
	}
	data, _ := json.Marshal(report)
	if len(data) > MaxCheckBytes {
		return nil, len(data), len(report.MustChecks), fmt.Errorf("check report exceeds %d bytes", MaxCheckBytes)
	}
	return map[string]any{"positive_risks": len(risks), "negative_risks": len(negative), "stale_rejected": true}, len(data), len(report.MustChecks), nil
}

func (e *evaluator) checkMigration(context.Context) (map[string]any, int, int, error) {
	legacy, err := e.st.Simulation.Load()
	if err != nil || legacy == nil {
		return nil, 0, 0, fmt.Errorf("load compatibility profile: %w", err)
	}
	first, local, err := domain.ProjectSimulationProfileV1(*legacy)
	if err != nil {
		return nil, 0, 0, err
	}
	compat, err := domain.SimulationProfileV2CompatibilityProfile(first, &local)
	if err != nil {
		return nil, 0, 0, err
	}
	second, _, err := domain.ProjectSimulationProfileV1(compat)
	if err != nil {
		return nil, 0, 0, err
	}
	if first.ProfileDigest != second.ProfileDigest || first.Health.State != "legacy" {
		return nil, 0, 0, fmt.Errorf("v1 migration is not idempotent legacy projection")
	}
	portable := first
	portable.Capabilities.LocalEvidence = false
	portable.Health = domain.SimulationProfileHealth{State: "portable_only", Reasons: []string{"local_evidence_unavailable"}}
	if err := domain.SetSimulationProfileDigest(&portable); err != nil {
		return nil, 0, 0, err
	}
	if _, err := domain.MarshalSimulationPortableProfile(portable); err != nil {
		return nil, 0, 0, err
	}
	return map[string]any{"legacy": first.Health.State, "portable": portable.Health.State, "fresh": e.profile.Health.State}, 0, 3, nil
}

func (e *evaluator) checkBudgets(context.Context) (map[string]any, int, int, error) {
	portable, _ := domain.MarshalSimulationPortableProfile(*e.profile)
	normal, _ := json.Marshal(e.normal)
	reinforced, _ := json.Marshal(e.reinforced)
	sizes := map[string]int{"portable": len(portable), "normal_contract": len(normal), "reinforced_contract": len(reinforced)}
	if len(portable) > MaxPortableBytes || len(normal) > MaxContractBytes || len(reinforced) > MaxContractBytes {
		return map[string]any{"sizes": sizes}, len(portable), len(e.profile.Features), fmt.Errorf("profile or contract byte budget exceeded")
	}
	for name, raw := range e.contexts {
		sizes[name] = len(raw)
		if len(raw) > MaxContextBytes {
			return map[string]any{"sizes": sizes}, len(raw), 0, fmt.Errorf("%s exceeds context byte budget", name)
		}
	}
	return map[string]any{"sizes": sizes}, len(portable), len(e.profile.Features), nil
}

func (e *evaluator) checkPerformance(ctx context.Context) (map[string]any, int, int, error) {
	reports := make([]domain.SimulationSourceReport, 0, 512)
	for i := 0; i < 64; i++ {
		reports = append(reports, e.evidence.SourceReports...)
	}
	start := time.Now()
	features, _, index := domain.AggregateSimulationEvidence(reports, time.Unix(3, 0).UTC())
	aggregateDuration := time.Since(start)
	largeDraft := strings.Repeat("entirely different synthetic prose ", 5000)
	start = time.Now()
	_, err := simulationcheck.NewEngine().Scan(ctx, largeDraft, index, len(reports))
	scanDuration := time.Since(start)
	if err != nil {
		return nil, len(largeDraft), len(reports), err
	}
	if aggregateDuration > 3*time.Second || scanDuration > 3*time.Second {
		return nil, len(largeDraft), len(reports), fmt.Errorf("bounded performance exceeded: aggregate=%s scan=%s", aggregateDuration, scanDuration)
	}
	return map[string]any{"reports": len(reports), "features": len(features), "aggregate_ms": aggregateDuration.Milliseconds(), "scan_ms": scanDuration.Milliseconds()}, len(largeDraft), len(reports), nil
}

func selectedCount(raw []byte) int {
	var payload map[string]any
	if json.Unmarshal(raw, &payload) != nil {
		return -1
	}
	if effective, ok := payload["simulation_effective"].(map[string]any); ok {
		if value, ok := effective["selected_count"].(float64); ok {
			return int(value)
		}
	}
	for _, section := range []string{"planning_memory", "working_memory"} {
		if memory, ok := payload[section].(map[string]any); ok {
			if contract, ok := memory["simulation_contract"].(map[string]any); ok {
				if value, ok := contract["selected_count"].(float64); ok {
					return int(value)
				}
			}
		}
	}
	return 0
}

func contractMustCount(contract domain.SimulationContract) int {
	total := 0
	for _, view := range contract.Views {
		total += len(view.Must)
	}
	return total
}

type fakeLLM struct {
	mu    sync.Mutex
	calls int
}

func (f *fakeLLM) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeLLM) Generate(_ context.Context, messages []agentcore.Message, _ []agentcore.ToolSpec, _ ...agentcore.CallOption) (*agentcore.LLMResponse, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	var prompt strings.Builder
	for _, message := range messages {
		for _, block := range message.Content {
			prompt.WriteString(block.Text)
		}
	}
	text := prompt.String()
	var response any
	if strings.Contains(text, "Analyze this simulation corpus source") {
		response = reportForPrompt(text)
	} else {
		response = domain.SimulationSynthesis{
			Style:         domain.SimulationStyle{SentenceRhythm: []string{"Vary sentence length around consequences"}},
			PlotDesign:    domain.SimulationPlotDesign{EscalationPatterns: []string{"Reveal, consequence, choice"}},
			HookDesign:    domain.SimulationHookDesign{Placement: []string{"Place an actionable question at openings"}},
			PacingDensity: domain.SimulationPacingDensity{InformationRelease: []string{"Bind each reveal to a consequence"}},
			RoleGuidance:  domain.SimulationRoleGuidance{Architect: []string{"Plan measurable consequences"}, Writer: []string{"Use original wording"}, Editor: []string{"Review obligations"}},
		}
	}
	data, err := json.Marshal(response)
	if err != nil {
		return nil, err
	}
	return &agentcore.LLMResponse{Message: agentcore.Message{Role: agentcore.RoleAssistant, Content: []agentcore.ContentBlock{agentcore.TextBlock(string(data))}}}, nil
}

func reportForPrompt(prompt string) domain.SimulationSourceReport {
	stable := []domain.SimulationTechniqueCandidate{
		{Dimension: "pacing_density.information_release", Statement: "bind each reveal to an immediate consequence and choice", Scope: "global", Confidence: .96, Tendency: "stable", Safety: "guidance"},
		{Dimension: "pacing_density.scene_density", Statement: "close each scene after one consequential choice", Scope: "global", Confidence: .95, Tendency: "stable", Safety: "guidance"},
		{Dimension: "plot_design.escalation_patterns", Statement: "raise cost after each newly revealed fact", Scope: "global", Confidence: .95, Tendency: "stable", Safety: "guidance"},
		{Dimension: "hook_design.payoff_rules", Statement: "answer one practical question before opening another", Scope: "global", Confidence: .94, Tendency: "stable", Safety: "guidance"},
		{Dimension: "reader_engagement.progression_rewards", Statement: "make changed options visible after each consequence", Scope: "global", Confidence: .94, Tendency: "stable", Safety: "guidance"},
		{Dimension: "style.sentence_rhythm", Statement: "alternate compact actions with reflective consequence lines", Scope: "global", Confidence: .93, Tendency: "stable", Safety: "guidance"},
		{Dimension: "style.prose_texture", Statement: "anchor abstract decisions in one physical action", Scope: "global", Confidence: .93, Tendency: "stable", Safety: "guidance"},
		{Dimension: "lexicon.transition_words", Statement: "prefer causal transitions over decorative connectors", Scope: "global", Confidence: .92, Tendency: "stable", Safety: "guidance"},
	}
	report := domain.SimulationSourceReport{ContentType: "body", Summary: "synthetic body segment", Candidates: append([]domain.SimulationTechniqueCandidate(nil), stable...)}
	switch {
	case strings.Contains(prompt, "FIXTURE_ID=opening-only"):
		report.Summary = "opening-local technique"
		report.Candidates = append(report.Candidates, domain.SimulationTechniqueCandidate{Dimension: "hook_design.placement", Statement: "open with one unanswered practical question", Scope: "opening", Confidence: .91, Tendency: "local", Safety: "guidance"})
	case strings.Contains(prompt, "FIXTURE_ID=stage-fast"):
		report.Summary = "fast contradictory stage"
		report.Candidates = append(report.Candidates, domain.SimulationTechniqueCandidate{Dimension: "pacing_density.scene_density", Statement: "shorten the pause between discovery and response", Scope: "middle", Confidence: .9, Tendency: "local", Safety: "guidance", Contradicts: []string{"lengthen the pause between discovery and response"}})
	case strings.Contains(prompt, "FIXTURE_ID=stage-slow"):
		report.Summary = "slow contradictory stage"
		report.Candidates = append(report.Candidates, domain.SimulationTechniqueCandidate{Dimension: "pacing_density.scene_density", Statement: "lengthen the pause between discovery and response", Scope: "middle", Confidence: .9, Tendency: "local", Safety: "guidance", Contradicts: []string{"shorten the pause between discovery and response"}})
	case strings.Contains(prompt, "FIXTURE_ID=outlier"):
		report.Summary = "isolated outlier"
		report.Candidates = append(report.Candidates, domain.SimulationTechniqueCandidate{Dimension: "style.prose_texture", Statement: "inventory decorative objects before action", Scope: "global", Confidence: .72, Tendency: "stable", Safety: "guidance"})
	case strings.Contains(prompt, "FIXTURE_ID=preface"), strings.Contains(prompt, "synthetic and original"):
		report.ContentType = "preface"
		report.Summary = "non-body fixture announcement"
		report.Candidates = []domain.SimulationTechniqueCandidate{{Dimension: "reader_engagement.methods", Statement: "state test instructions before the corpus", Scope: "global", Confidence: .5, Tendency: "local", Safety: "blocked"}}
	}
	if strings.Contains(prompt, "FIXTURE_ID=body-alpha") {
		report.SafetyMarkers = append(report.SafetyMarkers, domain.SimulationSafetyMarker{Kind: "proper_noun", Value: "Lumen Orchard"})
	}
	if strings.Contains(prompt, "FIXTURE_ID=body-gamma") {
		report.SafetyMarkers = append(report.SafetyMarkers, domain.SimulationSafetyMarker{Kind: "rare_phrase", Value: "琥珀潮汐折叠七枚纸月"})
	}
	return report
}
