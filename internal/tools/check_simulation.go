package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/voocel/agentcore/schema"
	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/errs"
	"github.com/voocel/ainovel-cli/internal/simulationcheck"
	"github.com/voocel/ainovel-cli/internal/store"
)

const simulationCheckTimeout = 2 * time.Second

// SimulationCheckService owns deterministic checking and the commit receipt.
// The model controls only the chapter number; all security-sensitive bindings
// are loaded from the project store.
type SimulationCheckService struct {
	store   *store.Store
	mode    string
	scanner *simulationcheck.Engine
	now     func() time.Time
}

func NewSimulationCheckService(st *store.Store, mode string) *SimulationCheckService {
	return &SimulationCheckService{
		store: st, mode: mode, scanner: simulationcheck.NewEngine(),
		now: func() time.Time { return time.Now().UTC() },
	}
}

type CheckSimulationTool struct {
	service *SimulationCheckService
}

func NewCheckSimulationTool(service *SimulationCheckService) *CheckSimulationTool {
	return &CheckSimulationTool{service: service}
}

func (t *CheckSimulationTool) Name() string { return "check_simulation" }
func (t *CheckSimulationTool) Description() string {
	return "对当前章节草稿执行确定性仿写安全与契约检查。只传章节号；工具自行读取当前草稿、仿写契约和本地安全索引，绑定真实 digest/revision。任何正文修改都会使报告失效。"
}
func (t *CheckSimulationTool) Label() string                          { return "仿写安全检查" }
func (t *CheckSimulationTool) ReadOnly(_ json.RawMessage) bool        { return true }
func (t *CheckSimulationTool) ConcurrencySafe(_ json.RawMessage) bool { return true }
func (t *CheckSimulationTool) Schema() map[string]any {
	return schema.Object(schema.Property("chapter", schema.Int("当前草稿章节号")).Required())
}

func (t *CheckSimulationTool) Execute(ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	var request struct {
		Chapter int `json:"chapter"`
	}
	if err := json.Unmarshal(args, &request); err != nil {
		return nil, fmt.Errorf("invalid args: %w: %w", errs.ErrToolArgs, err)
	}
	if request.Chapter <= 0 {
		return nil, fmt.Errorf("chapter must be > 0: %w", errs.ErrToolArgs)
	}
	report, err := t.service.Check(ctx, request.Chapter)
	if err != nil {
		return nil, err
	}
	return json.Marshal(report)
}

func (s *SimulationCheckService) Check(ctx context.Context, chapter int) (*simulationcheck.Report, error) {
	if s == nil || s.store == nil || s.store.SimulationChecks == nil {
		return nil, fmt.Errorf("simulation check service is unavailable: %w", errs.ErrToolPrecondition)
	}
	content, _, err := s.store.Drafts.LoadChapterContent(chapter)
	if err != nil {
		return nil, fmt.Errorf("load simulation draft: %w: %w", errs.ErrStoreRead, err)
	}
	if strings.TrimSpace(content) == "" {
		return nil, fmt.Errorf("no content found for chapter %d: %w", chapter, errs.ErrToolPrecondition)
	}
	contract, profile, err := s.store.EnsureSimulationContract(s.mode)
	if err != nil {
		return nil, fmt.Errorf("load simulation contract: %w: %w", errs.ErrStoreRead, err)
	}
	if contract == nil {
		return nil, fmt.Errorf("simulation contract is unavailable: %w", errs.ErrToolPrecondition)
	}

	evidence, evidenceErr := s.store.Simulation.LoadLocalEvidence()
	if evidenceErr != nil {
		return nil, fmt.Errorf("load simulation safety index: %w: %w", errs.ErrStoreRead, evidenceErr)
	}
	index := currentSafetyIndex(profile, evidence)
	indexDigest := simulationcheck.SafetyIndexDigest(index)
	binding := s.binding(chapter, content, profile, contract, indexDigest)

	scanCtx, cancel := context.WithTimeout(ctx, simulationCheckTimeout)
	defer cancel()
	var risks []simulationcheck.Risk
	if index != nil {
		sourceCount := 0
		if profile != nil {
			sourceCount = profile.Corpus.SourceCount
		}
		risks, err = s.scanner.Scan(scanCtx, content, index, sourceCount)
		if err != nil {
			if scanCtx.Err() != nil {
				return nil, fmt.Errorf("simulation scanner canceled or timed out: %w", scanCtx.Err())
			}
			return nil, fmt.Errorf("simulation scanner failed: %w", err)
		}
	}

	mustChecks, advisories, contractStatus, remediation := s.evaluateContract(chapter, contract, profile)
	report := simulationcheck.Report{
		Version: simulationcheck.ReportVersion, ProjectDigest: binding.ProjectDigest,
		Chapter: chapter, DraftDigest: binding.DraftDigest, ProfileDigest: binding.ProfileDigest,
		ContractRevision: binding.ContractRevision, ContractDigest: binding.ContractDigest,
		EffectiveMode: binding.EffectiveMode, CheckerVersion: simulationcheck.CheckerVersion,
		CheckerDigest: binding.CheckerDigest, SafetyIndexDigest: indexDigest,
		CheckedAt: s.now().Format(time.RFC3339), Risks: risks, MustChecks: mustChecks,
		ShouldAdvisories: advisories, ContractStatus: contractStatus, Remediation: remediation,
	}
	report.Capability = simulationCapability(profile, contract, index)
	for _, check := range mustChecks {
		if check.Status == "unverifiable" {
			report.Warnings = append(report.Warnings,
				"契约包含不能由结构化证据确定性验证的 must；该项已降为 Editor 审阅，不作为硬失败。",
			)
			break
		}
	}
	if index == nil {
		report.CopyStatus = simulationcheck.StatusUnverified
		report.Warnings = append(report.Warnings,
			"本项目没有与当前画像绑定的本地安全索引；已执行契约检查，但未声称通过完整来源相似性扫描。",
		)
	} else if len(risks) > 0 {
		report.CopyStatus = simulationcheck.StatusFail
		report.Remediation = append(report.Remediation, "改写报告标出的当前草稿片段，保留情节功能但更换表达、专名或高辨识度组合，然后重新检查。")
	} else {
		report.CopyStatus = simulationcheck.StatusPass
	}
	report.Passed = len(risks) == 0 && contractStatus != simulationcheck.StatusFail

	for attempt := 0; attempt < 3; attempt++ {
		current, loadErr := s.store.SimulationChecks.Load(chapter)
		if loadErr != nil {
			return nil, fmt.Errorf("load simulation check report: %w: %w", errs.ErrStoreRead, loadErr)
		}
		expectedRevision := int64(0)
		if current != nil {
			expectedRevision = current.Revision
		}
		report.Revision = expectedRevision + 1
		finalized, finalizeErr := simulationcheck.Finalize(report)
		if finalizeErr != nil {
			return nil, fmt.Errorf("finalize simulation check report: %w", finalizeErr)
		}
		if saveErr := s.store.SimulationChecks.SaveCAS(finalized, expectedRevision); saveErr == nil {
			slog.Info("simulation check completed",
				"module", "simulation_check", "chapter", chapter,
				"draft_digest", finalized.DraftDigest, "profile_digest", finalized.ProfileDigest,
				"report_digest", finalized.ReportDigest, "risk_count", len(finalized.Risks),
				"must_count", len(finalized.MustChecks), "capability", finalized.Capability.State,
			)
			return &finalized, nil
		} else if attempt == 2 {
			return nil, fmt.Errorf("save simulation check report: %w: %w", errs.ErrStoreWrite, saveErr)
		}
	}
	return nil, fmt.Errorf("simulation check report synchronization failed: %w", errs.ErrStoreWrite)
}

// EnsureCurrent is the commit gate. A missing profile keeps the pre-PR-04
// creation path unchanged; every enabled profile needs a current receipt.
func (s *SimulationCheckService) EnsureCurrent(_ context.Context, chapter int, content string) error {
	contract, profile, err := s.store.EnsureSimulationContract(s.mode)
	if err != nil {
		return fmt.Errorf("load simulation contract for commit: %w: %w", errs.ErrStoreRead, err)
	}
	if profile == nil {
		return nil
	}
	if contract == nil || contract.Status == domain.SimulationContractInactive {
		return nil
	}
	evidence, err := s.store.Simulation.LoadLocalEvidence()
	if err != nil {
		return fmt.Errorf("load simulation safety index for commit: %w: %w", errs.ErrStoreRead, err)
	}
	indexDigest := simulationcheck.SafetyIndexDigest(currentSafetyIndex(profile, evidence))
	binding := s.binding(chapter, content, profile, contract, indexDigest)
	report, err := s.store.SimulationChecks.Load(chapter)
	if err != nil {
		return fmt.Errorf("load simulation check report for commit: %w: %w", errs.ErrStoreRead, err)
	}
	if current, reason := simulationcheck.Current(report, binding); !current {
		return fmt.Errorf("第 %d 章仿写检查报告已失效（%s）。请在最终正文上重新调用 check_simulation，再 commit_chapter: %w",
			chapter, reason, errs.ErrToolPrecondition)
	}
	if !report.Passed {
		return fmt.Errorf("第 %d 章未通过仿写门禁：copy_status=%s contract_status=%s remediation=%v: %w",
			chapter, report.CopyStatus, report.ContractStatus, report.Remediation, errs.ErrToolPrecondition)
	}
	return nil
}

func (s *SimulationCheckService) binding(
	chapter int,
	content string,
	profile *domain.SimulationProfileV2,
	contract *domain.SimulationContract,
	indexDigest string,
) simulationcheck.Binding {
	binding := simulationcheck.Binding{
		ProjectDigest: store.TextSHA256(strings.ToLower(strings.TrimSpace(s.store.Dir()))),
		Chapter:       chapter, DraftDigest: store.TextSHA256(content),
		CheckerDigest: simulationcheck.ConfigurationDigest(), SafetyIndexDigest: indexDigest,
	}
	if profile != nil {
		binding.ProfileDigest = profile.ProfileDigest
	}
	if contract != nil {
		binding.ContractRevision = contract.Revision
		binding.ContractDigest = contract.ContractDigest
		binding.EffectiveMode = contract.EffectiveMode
	}
	return binding
}

func currentSafetyIndex(profile *domain.SimulationProfileV2, evidence *domain.SimulationLocalEvidence) *domain.SimulationSafetyIndex {
	if profile == nil || evidence == nil || evidence.ProfileDigest != profile.ProfileDigest {
		return nil
	}
	return evidence.SafetyIndex
}

func simulationCapability(
	profile *domain.SimulationProfileV2,
	contract *domain.SimulationContract,
	index *domain.SimulationSafetyIndex,
) simulationcheck.Capability {
	capability := simulationcheck.Capability{ContractChecks: contract != nil}
	switch {
	case profile == nil:
		capability.State = simulationcheck.CapabilityUnavailable
		capability.Reason = "profile_missing"
	case index == nil:
		capability.State = simulationcheck.CapabilityPartial
		capability.Reason = "local_safety_index_unavailable"
	default:
		capability.State = simulationcheck.CapabilityFull
		capability.LocalIndex = true
	}
	return capability
}

func (s *SimulationCheckService) evaluateContract(
	chapter int,
	contract *domain.SimulationContract,
	profile *domain.SimulationProfileV2,
) ([]simulationcheck.MustCheck, []simulationcheck.ShouldAdvisory, string, []string) {
	if contract == nil || profile == nil {
		return nil, nil, simulationcheck.StatusUnverified, nil
	}
	writerView := contract.View(domain.SimulationRoleWriter, "chapter")
	editorView := contract.View(domain.SimulationRoleEditor, "review")
	if writerView == nil && editorView == nil {
		return nil, nil, simulationcheck.StatusUnverified, nil
	}
	var editorShould []string
	if editorView != nil {
		editorShould = editorView.Should
	}
	advisories := make([]simulationcheck.ShouldAdvisory, 0, len(editorShould))
	for _, featureID := range editorShould {
		advisories = append(advisories, simulationcheck.ShouldAdvisory{
			FeatureID: featureID, Status: "editor_review_required",
		})
	}
	mustFeatureIDs := simulationChapterMustFeatureIDs(writerView, editorView)
	if contract.EffectiveMode != domain.SimulationModeReinforced || len(mustFeatureIDs) == 0 {
		if len(advisories) > 0 {
			return nil, advisories, simulationcheck.StatusAdvisory, nil
		}
		return nil, advisories, simulationcheck.StatusPass, nil
	}

	features := make(map[string]domain.SimulationFeature, len(profile.Features))
	for _, feature := range profile.Features {
		features[feature.ID] = feature
	}
	outline, _ := s.store.Outline.GetChapterOutline(chapter)
	plan, _ := s.store.Drafts.LoadChapterPlan(chapter)
	status := simulationcheck.StatusPass
	var checks []simulationcheck.MustCheck
	var remediation []string
	for _, featureID := range mustFeatureIDs {
		feature, exists := features[featureID]
		if !exists || feature.Disabled {
			continue
		}
		check := measurableSimulationMust(feature, outline, plan)
		checks = append(checks, check)
		if check.Status == "missing" {
			status = simulationcheck.StatusFail
			remediation = append(remediation, check.Remediation)
		}
	}
	return checks, advisories, status, remediation
}

func simulationChapterMustFeatureIDs(views ...*domain.SimulationContractView) []string {
	seen := make(map[string]struct{})
	var featureIDs []string
	for _, view := range views {
		if view == nil {
			continue
		}
		for _, featureID := range view.Must {
			if _, exists := seen[featureID]; exists {
				continue
			}
			seen[featureID] = struct{}{}
			featureIDs = append(featureIDs, featureID)
		}
	}
	return featureIDs
}

func measurableSimulationMust(
	feature domain.SimulationFeature,
	outline *domain.OutlineEntry,
	plan *domain.ChapterPlan,
) simulationcheck.MustCheck {
	check := simulationcheck.MustCheck{
		FeatureID: feature.ID, Dimension: feature.Dimension, Status: "unverifiable",
	}
	switch {
	case strings.HasPrefix(feature.Dimension, "hook_design."):
		if plan != nil && (strings.TrimSpace(plan.Contract.HookGoal) != "" || strings.TrimSpace(plan.Hook) != "") {
			check.Status, check.Evidence = "met", "chapter_plan.hook_or_hook_goal"
		} else if outline != nil && strings.TrimSpace(outline.Hook) != "" {
			check.Status, check.Evidence = "met", "outline.hook"
		} else {
			check.Status = "missing"
			check.Remediation = "在章节计划的 hook/hook_goal 或正式细纲 hook 中保存可审阅的章末钩子。"
		}
	case strings.HasPrefix(feature.Dimension, "plot_design."):
		if plan != nil && len(plan.Contract.RequiredBeats) > 0 {
			check.Status, check.Evidence = "met", "chapter_contract.required_beats"
		} else if outline != nil && strings.TrimSpace(outline.CoreEvent) != "" && len(outline.Scenes) > 0 {
			check.Status, check.Evidence = "met", "outline.core_event_and_scenes"
		} else {
			check.Status = "missing"
			check.Remediation = "在章节契约 required_beats 或正式细纲 core_event/scenes 中保存结构推进项。"
		}
	case feature.Dimension == "pacing_density.information_release":
		if plan != nil && len(plan.Contract.RequiredBeats) > 0 && len(plan.Contract.ContinuityChecks) > 0 {
			check.Status, check.Evidence = "met", "chapter_contract.required_beats_and_continuity_checks"
		} else {
			check.Status = "missing"
			check.Remediation = "在章节契约中同时保存信息释放推进项（required_beats）与检查点（continuity_checks）。"
		}
	case feature.Dimension == "pacing_density.scene_density":
		if outline != nil && len(outline.Scenes) >= 2 {
			check.Status, check.Evidence = "met", "outline.scenes"
		} else {
			check.Status = "missing"
			check.Remediation = "在正式细纲中保存至少两个可执行场景，作为节奏密度检查点。"
		}
	}
	return check
}
