package host

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/simulationcheck"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	simulationSummaryDigestLength = 12
	simulationSummaryTextRunes    = 160
	simulationSummaryRiskLimit    = 5
)

func buildSimulationProfileSummary(st *storepkg.Store, selectedMode string, chapter int) *SimulationProfileSummary {
	if st == nil || st.Simulation == nil {
		return nil
	}
	profile, err := st.Simulation.LoadPortable()
	if err != nil {
		return &SimulationProfileSummary{
			Loaded:          true,
			HealthState:     "invalid",
			SelectedMode:    normalizeSummaryMode(selectedMode),
			EffectiveMode:   domain.SimulationModeNormal,
			EffectiveReason: "profile_invalid",
			DiagnosticError: "画像无法读取；请重新导入或重新分析",
		}
	}
	if profile == nil {
		return nil
	}

	summary := &SimulationProfileSummary{
		Loaded:         true,
		Version:        profile.Version,
		ProfileDigest:  shortSimulationDigest(profile.ProfileDigest),
		UpdatedAt:      profile.UpdatedAt,
		SourceCount:    profile.Corpus.SourceCount,
		HealthState:    profile.Health.State,
		HealthReasons:  boundedSummaryStrings(profile.Health.Reasons, 8, 80),
		AnalysisSigned: profile.Capabilities.AnalysisSigned,
		SynthesisSigned: strings.TrimSpace(profile.Analysis.SynthesisSignature) != "" &&
			strings.TrimSpace(profile.Analysis.AggregationSignature) != "",
		Portable:        profile.Capabilities.Portable,
		LocalEvidence:   false,
		SafetyIndex:     false,
		FeatureCounts:   countSimulationFeatures(profile.Features),
		SelectedMode:    normalizeSummaryMode(selectedMode),
		EffectiveMode:   domain.SimulationModeNormal,
		EffectiveReason: "contract_missing",
	}

	evidence, evidenceErr := st.Simulation.LoadLocalEvidence()
	if evidenceErr != nil {
		summary.DiagnosticError = "本地证据无法读取；portable 画像仍可用于受限创作"
		evidence = nil
	}
	if evidence != nil && evidence.ProfileDigest == profile.ProfileDigest {
		summary.ReportCount = len(evidence.SourceReports)
		summary.LocalEvidence = true
		summary.SafetyIndex = evidence.SafetyIndex != nil && len(evidence.SafetyIndex.Entries) > 0
	}
	if summary.SourceCount > 0 {
		summary.CoveragePercent = summary.ReportCount * 100 / summary.SourceCount
		if summary.CoveragePercent > 100 {
			summary.CoveragePercent = 100
		}
	}

	foundationRevision, foundationDigest, briefDigest := currentSimulationBindings(st)
	contract, _, contractErr := st.EnsureSimulationContract(summary.SelectedMode)
	contractCurrent := false
	if contractErr != nil {
		summary.DiagnosticError = "仿写契约无法同步；强化模式不会标记为已生效"
	} else if contract != nil {
		current, staleReason := domain.SimulationContractCurrent(
			contract, profile, summary.SelectedMode, foundationRevision, foundationDigest, briefDigest,
		)
		contractCurrent = current
		summary.Contract = summarizeSimulationContract(contract, profile.Features, current, staleReason)
		if current && contract.Status != domain.SimulationContractInactive {
			summary.EffectiveMode = contract.EffectiveMode
			summary.EffectiveReason = strings.Join(contract.Reasons, ",")
		} else if staleReason != "" {
			summary.EffectiveReason = staleReason
		} else {
			summary.EffectiveReason = strings.Join(contract.Reasons, ",")
		}
	}

	summary.Actions = simulationActions(st, profile, evidence)
	summary.ModePreviews = simulationModePreviews(profile, foundationRevision, foundationDigest, briefDigest)
	if !contractCurrent {
		contract = nil
	}
	summary.Check = summarizeLatestSimulationCheck(st, profile, evidence, contract, chapter)
	return summary
}

func currentSimulationBindings(st *storepkg.Store) (int64, string, string) {
	foundation, err := st.Foundation.Load()
	if err != nil {
		return 0, "", ""
	}
	foundationDigest, _ := domain.FoundationContentSignature(foundation)
	rules, _ := st.UserRules.Load()
	briefDigest := ""
	if rules != nil {
		if data, err := json.Marshal(rules); err == nil {
			sum := sha256.Sum256(data)
			briefDigest = hex.EncodeToString(sum[:])
		}
	}
	return foundation.Revision, foundationDigest, briefDigest
}

func summarizeSimulationContract(
	contract *domain.SimulationContract,
	features []domain.SimulationFeature,
	current bool,
	staleReason string,
) *SimulationContractSummary {
	featureByID := make(map[string]domain.SimulationFeature, len(features))
	for _, feature := range features {
		featureByID[feature.ID] = feature
	}
	summary := &SimulationContractSummary{
		Revision:           contract.Revision,
		Status:             contract.Status,
		Current:            current,
		StaleReason:        staleReason,
		ProfileDigest:      shortSimulationDigest(contract.ProfileDigest),
		FoundationRevision: contract.FoundationRevision,
		CreativeBriefBound: contract.CreativeBriefDigest != "",
		ExclusionCount:     len(contract.Exclusions),
	}
	for _, exclusion := range contract.Exclusions {
		if summary.ExclusionReasons == nil {
			summary.ExclusionReasons = map[string]int{}
		}
		summary.ExclusionReasons[exclusion.Reason]++
	}
	for _, view := range contract.Views {
		item := SimulationContractView{
			Role: view.Role, Phase: view.Phase, Must: len(view.Must),
			Should: len(view.Should), Avoid: len(view.Avoid), ByteBudget: view.ByteBudget,
		}
		item.Features = appendContractFeatureSummaries(item.Features, view.Must, "must", featureByID)
		item.Features = appendContractFeatureSummaries(item.Features, view.Should, "should", featureByID)
		item.Features = appendContractFeatureSummaries(item.Features, view.Avoid, "avoid", featureByID)
		summary.Views = append(summary.Views, item)
	}
	return summary
}

func appendContractFeatureSummaries(
	out []SimulationFeatureSummary,
	ids []string,
	level string,
	featureByID map[string]domain.SimulationFeature,
) []SimulationFeatureSummary {
	for _, id := range ids {
		feature, ok := featureByID[id]
		if !ok {
			continue
		}
		out = append(out, SimulationFeatureSummary{
			ID: id, Dimension: feature.Dimension,
			Statement: truncateRunes(strings.TrimSpace(feature.Statement), simulationSummaryTextRunes),
			Level:     level,
		})
	}
	return out
}

func simulationModePreviews(
	profile *domain.SimulationProfileV2,
	foundationRevision int64,
	foundationDigest string,
	briefDigest string,
) []SimulationModePreview {
	previews := make([]SimulationModePreview, 0, 2)
	for _, mode := range []string{domain.SimulationModeNormal, domain.SimulationModeReinforced} {
		contract, err := domain.CompileSimulationContract(domain.SimulationContractInput{
			Profile: profile, RequestedMode: mode, FoundationRevision: foundationRevision,
			FoundationDigest: foundationDigest, CreativeBriefDigest: briefDigest,
		})
		preview := SimulationModePreview{Mode: mode}
		if err != nil {
			preview.Status = domain.SimulationContractInactive
			preview.Reason = "preview_unavailable"
			previews = append(previews, preview)
			continue
		}
		preview.Status = contract.Status
		preview.Reason = strings.Join(contract.Reasons, ",")
		featureByID := make(map[string]domain.SimulationFeature, len(profile.Features))
		for _, feature := range profile.Features {
			featureByID[feature.ID] = feature
		}
		for _, view := range contract.Views {
			dimensions := map[string]struct{}{}
			for _, ids := range [][]string{view.Must, view.Should, view.Avoid} {
				for _, id := range ids {
					if feature, ok := featureByID[id]; ok {
						dimensions[feature.Dimension] = struct{}{}
					}
				}
			}
			names := make([]string, 0, len(dimensions))
			for dimension := range dimensions {
				names = append(names, dimension)
			}
			sort.Strings(names)
			preview.Roles = append(preview.Roles, SimulationRolePreview{
				Role: view.Role, Phase: view.Phase,
				FeatureCount: len(view.Must) + len(view.Should) + len(view.Avoid),
				Must:         len(view.Must), Should: len(view.Should), Avoid: len(view.Avoid),
				ByteBudget: view.ByteBudget, Dimensions: names,
			})
		}
		previews = append(previews, preview)
	}
	return previews
}

func simulationActions(
	st *storepkg.Store,
	profile *domain.SimulationProfileV2,
	evidence *domain.SimulationLocalEvidence,
) SimulationActionSummary {
	actions := SimulationActionSummary{
		Rescan:       SimulationActionCapability{Reason: "需要当前项目的本地语料"},
		Resynthesize: SimulationActionCapability{Reason: "需要完整且签名有效的本地逐篇报告"},
		Reanalyze:    SimulationActionCapability{Reason: "需要当前项目的本地语料"},
	}
	if projectSimulationSourcesAvailable(st) {
		actions.Rescan = SimulationActionCapability{Enabled: true}
		actions.Reanalyze = SimulationActionCapability{Enabled: true}
	}
	if evidence == nil || evidence.ProfileDigest != profile.ProfileDigest {
		if !actions.Rescan.Enabled {
			actions.Rescan.Reason = "portable 画像没有绑定本地语料；请重新上传原语料"
		}
		return actions
	}
	sourceDirAvailable := false
	if strings.TrimSpace(evidence.SourceDir) != "" {
		if info, err := os.Stat(evidence.SourceDir); err == nil && info.IsDir() {
			sourceDirAvailable = true
			actions.Rescan = SimulationActionCapability{Enabled: true}
			actions.Reanalyze = SimulationActionCapability{Enabled: true}
		}
	}
	if sourceDirAvailable && simulationReportsReusable(profile, evidence) {
		actions.Resynthesize = SimulationActionCapability{Enabled: true}
	}
	return actions
}

func projectSimulationSourcesAvailable(st *storepkg.Store) bool {
	if st == nil {
		return false
	}
	files, err := os.ReadDir(filepath.Join(filepath.Dir(st.Dir()), "simulate"))
	if err != nil {
		return false
	}
	for _, file := range files {
		if file.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(file.Name())) {
		case ".txt", ".md", ".markdown":
			return true
		}
	}
	return false
}

func simulationReportsReusable(profile *domain.SimulationProfileV2, evidence *domain.SimulationLocalEvidence) bool {
	if profile == nil || evidence == nil || profile.Corpus.SourceCount <= 0 ||
		len(evidence.SourceReports) != profile.Corpus.SourceCount ||
		strings.TrimSpace(profile.Analysis.SourceAnalysisSignature) == "" {
		return false
	}
	for _, report := range evidence.SourceReports {
		if report.AnalysisSignature != profile.Analysis.SourceAnalysisSignature {
			return false
		}
	}
	return true
}

func summarizeLatestSimulationCheck(
	st *storepkg.Store,
	profile *domain.SimulationProfileV2,
	evidence *domain.SimulationLocalEvidence,
	contract *domain.SimulationContract,
	chapter int,
) *SimulationCheckSummary {
	if chapter <= 0 {
		return &SimulationCheckSummary{State: "not_run", Reason: "chapter_unavailable"}
	}
	report, err := st.SimulationChecks.Load(chapter)
	if err != nil {
		return &SimulationCheckSummary{State: "error", Reason: "report_invalid", Chapter: chapter}
	}
	if report == nil {
		return &SimulationCheckSummary{State: "not_run", Reason: "report_missing", Chapter: chapter}
	}
	content, _, draftErr := st.Drafts.LoadChapterContent(chapter)
	if draftErr != nil {
		return &SimulationCheckSummary{
			State: "stale", Reason: "draft_unavailable", Chapter: chapter,
			CheckedAt: report.CheckedAt, Capability: report.Capability.State,
		}
	}
	var index *domain.SimulationSafetyIndex
	if evidence != nil && evidence.ProfileDigest == profile.ProfileDigest {
		index = evidence.SafetyIndex
	}
	binding := simulationcheck.Binding{
		ProjectDigest: storepkg.TextSHA256(strings.ToLower(strings.TrimSpace(st.Dir()))),
		Chapter:       chapter, DraftDigest: storepkg.TextSHA256(content),
		ProfileDigest:     profile.ProfileDigest,
		CheckerDigest:     simulationcheck.ConfigurationDigest(),
		SafetyIndexDigest: simulationcheck.SafetyIndexDigest(index),
	}
	if contract != nil {
		binding.ContractRevision = contract.Revision
		binding.ContractDigest = contract.ContractDigest
		binding.EffectiveMode = contract.EffectiveMode
	}
	current, reason := simulationcheck.Current(report, binding)
	summary := &SimulationCheckSummary{
		State: "stale", Reason: reason, Chapter: chapter, CheckedAt: report.CheckedAt,
		DraftCurrent: current, Capability: report.Capability.State,
		CapabilityReason: truncateRunes(report.Capability.Reason, 120),
		CopyStatus:       report.CopyStatus, ContractStatus: report.ContractStatus,
		RiskCount: len(report.Risks), MustTotal: len(report.MustChecks),
		AdvisoryCount: len(report.ShouldAdvisories),
	}
	for _, check := range report.MustChecks {
		switch check.Status {
		case "met":
			summary.MustMet++
		case "missing":
			summary.MustMissing++
		}
	}
	if !current {
		return summary
	}
	switch {
	case !report.Passed:
		summary.State = "fail"
	case report.Capability.State == simulationcheck.CapabilityPartial:
		summary.State = "partial"
	case report.Capability.State == simulationcheck.CapabilityUnavailable:
		summary.State = "error"
	default:
		summary.State = "pass"
	}
	summary.Reason = ""
	for i, risk := range report.Risks {
		if i == simulationSummaryRiskLimit {
			break
		}
		summary.Risks = append(summary.Risks, SimulationRiskSummary{
			Type: risk.Type, DraftExcerpt: truncateRunes(risk.DraftExcerpt, 120),
			StartRune: risk.StartRune, LengthRunes: risk.LengthRunes,
		})
	}
	return summary
}

func countSimulationFeatures(features []domain.SimulationFeature) SimulationFeatureCounts {
	counts := SimulationFeatureCounts{Total: len(features)}
	for _, feature := range features {
		switch feature.Classification {
		case "stable":
			counts.Stable++
		case "local", "legacy_unknown":
			counts.Local++
		case "outlier":
			counts.Outlier++
		case "contradictory":
			counts.Contradictory++
		}
	}
	return counts
}

func normalizeSummaryMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), domain.SimulationModeReinforced) {
		return domain.SimulationModeReinforced
	}
	return domain.SimulationModeNormal
}

func shortSimulationDigest(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= simulationSummaryDigestLength {
		return value
	}
	return value[:simulationSummaryDigestLength]
}

func boundedSummaryStrings(values []string, limit, runeLimit int) []string {
	out := make([]string, 0, min(len(values), limit))
	for _, value := range values {
		value = truncateRunes(strings.TrimSpace(value), runeLimit)
		if value == "" {
			continue
		}
		out = append(out, value)
		if len(out) == limit {
			break
		}
	}
	return out
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
