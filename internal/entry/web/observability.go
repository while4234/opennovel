package web

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
	modelreg "github.com/voocel/ainovel-cli/internal/models"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

type observabilityGroup struct {
	Key                   string   `json:"key"`
	Provider              string   `json:"provider,omitempty"`
	Model                 string   `json:"model,omitempty"`
	Role                  string   `json:"role,omitempty"`
	Workflow              string   `json:"workflow,omitempty"`
	Stage                 string   `json:"stage,omitempty"`
	Calls                 int      `json:"calls"`
	UsageCalls            int      `json:"usage_calls"`
	Failures              int      `json:"failures"`
	Retries               int      `json:"retries"`
	Input                 int      `json:"input_tokens"`
	NonCached             int      `json:"non_cached_input_tokens"`
	Output                int      `json:"output_tokens"`
	CacheRead             int      `json:"cache_read_tokens"`
	CacheWrite            int      `json:"cache_write_tokens"`
	CostUSD               float64  `json:"cost_usd"`
	SavedUSD              float64  `json:"saved_usd"`
	LatencyMSSum          int64    `json:"latency_ms_sum"`
	AvgLatencyMS          float64  `json:"avg_latency_ms"`
	FailureRate           float64  `json:"failure_rate"`
	RetryRate             float64  `json:"retry_rate"`
	QualitySamples        int      `json:"quality_samples"`
	FirstReviewPassRate   *float64 `json:"first_review_pass_rate"`
	ConsistencyPassRate   *float64 `json:"consistency_pass_rate"`
	AdaptationPassRate    *float64 `json:"adaptation_pass_rate"`
	AverageReviewScore    *float64 `json:"average_review_score"`
	CostPerAcceptedKRunes *float64 `json:"cost_per_accepted_1000_runes"`
	firstReviewPasses     int
	consistencyPasses     int
	adaptationPasses      int
	reviewScoreSum        float64
	acceptedRunes         int
	Coverage              float64 `json:"coverage"`
	UsageIncomplete       bool    `json:"usage_incomplete"`
	CacheCapable          *bool   `json:"cache_capable"`
	Confidence            string  `json:"confidence"`
}

type observabilityTrend struct {
	Date       string  `json:"date"`
	Calls      int     `json:"calls"`
	Input      int     `json:"input_tokens"`
	CacheRead  int     `json:"cache_read_tokens"`
	CacheWrite int     `json:"cache_write_tokens"`
	CostUSD    float64 `json:"cost_usd"`
}

type observabilityReport struct {
	GeneratedAt time.Time            `json:"generated_at"`
	GroupBy     string               `json:"group_by"`
	Overall     observabilityGroup   `json:"overall"`
	Groups      []observabilityGroup `json:"groups"`
	Trend       []observabilityTrend `json:"trend"`
	Legacy      bool                 `json:"legacy_aggregate"`
}

type observabilityRecommendation struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Model      string  `json:"model"`
	Confidence string  `json:"confidence"`
	Evidence   string  `json:"evidence"`
	Action     string  `json:"action"`
	HitRate    float64 `json:"cache_hit_rate"`
}

func (s *Server) handleObservabilityUsage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	report, err := s.buildObservabilityReport(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleObservabilityRecommendations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	report, err := s.buildObservabilityReport(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	recommendations := make([]observabilityRecommendation, 0)
	for _, group := range report.Groups {
		if group.Confidence == "low" || group.Input == 0 || (group.CacheRead == 0 && group.CacheWrite == 0) {
			continue
		}
		hitRate := float64(group.CacheRead) / float64(group.Input)
		if hitRate >= 0.30 {
			continue
		}
		recommendations = append(recommendations, observabilityRecommendation{
			ID: "prompt-cache:" + group.Key, Kind: "prompt_cache", Model: group.Model,
			Confidence: group.Confidence, HitRate: hitRate,
			Evidence: "同组调用的数据覆盖充分，但近期 Prompt Cache 命中率低于 30%。",
			Action:   "稳定系统提示前缀，把时间戳、run ID 和本轮动态上下文后移；不缓存生成结果。",
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"generated_at":           report.GeneratedAt,
		"recommendations":        recommendations,
		"automatic_model_switch": false,
	})
}

func (s *Server) handleProjectApplyModelRecommendation(w http.ResponseWriter, r *http.Request, id string) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var req struct {
		RecommendationID string `json:"recommendation_id"`
		Role             string `json:"role"`
		Provider         string `json:"provider"`
		Model            string `json:"model"`
		ExpectedRevision string `json:"expected_revision"`
		Confirmed        bool   `json:"confirmed"`
	}
	if err := decodeJSONBody(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !req.Confirmed || strings.TrimSpace(req.RecommendationID) == "" {
		writeError(w, http.StatusBadRequest, "explicit recommendation confirmation is required")
		return
	}
	if !strings.HasPrefix(strings.TrimSpace(req.RecommendationID), "model-route:") {
		writeError(w, http.StatusBadRequest, "this recommendation does not change model routing")
		return
	}
	if strings.TrimSpace(req.Role) == "" || strings.TrimSpace(req.Provider) == "" || strings.TrimSpace(req.Model) == "" {
		writeError(w, http.StatusBadRequest, "role, provider and model are required")
		return
	}
	session, manifest, err := s.sessions.Open(id)
	if err != nil {
		writeProjectSessionError(w, err)
		return
	}
	current := session.ModelConfig()
	currentRevision := modelConfigRevision(current)
	if strings.TrimSpace(req.ExpectedRevision) == "" || req.ExpectedRevision != currentRevision {
		writeJSON(w, http.StatusConflict, map[string]any{"error": "model configuration changed", "current_revision": currentRevision})
		return
	}
	models, err := session.SwitchModel(req.Role, req.Provider, req.Model)
	if err != nil {
		writeProjectLifecycleError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project": manifest, "models": models, "snapshot": session.Snapshot(),
		"revision": modelConfigRevision(models), "automatic_model_switch": false,
	})
}

func modelConfigRevision(config apiModelConfig) string {
	data, _ := json.Marshal(config)
	hash := sha256.Sum256(data)
	return hex.EncodeToString(hash[:8])
}

func (s *Server) buildObservabilityReport(r *http.Request) (observabilityReport, error) {
	groupBy := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("group_by")))
	if groupBy == "" {
		groupBy = "model"
	}
	projectID := strings.TrimSpace(r.URL.Query().Get("project_id"))
	from := parseUsageTime(r.URL.Query().Get("from"), time.Time{})
	to := parseUsageTime(r.URL.Query().Get("to"), time.Now().UTC().Add(24*time.Hour))
	projects, err := s.store.ListProjects()
	if err != nil {
		return observabilityReport{}, err
	}
	groups := make(map[string]*observabilityGroup)
	trends := make(map[string]*observabilityTrend)
	report := observabilityReport{GeneratedAt: time.Now().UTC(), GroupBy: groupBy}
	for _, project := range projects {
		if projectID != "" && project.ID != projectID {
			continue
		}
		usageStore := storepkg.NewStore(project.OutputDir).Usage
		calls, err := usageStore.LoadCalls()
		if err != nil {
			return observabilityReport{}, err
		}
		for _, call := range calls {
			if call.Timestamp.Before(from) || !call.Timestamp.Before(to) {
				continue
			}
			addObservabilityCall(&report.Overall, call)
			key := observabilityGroupKey(call, groupBy)
			group := groups[key]
			if group == nil {
				group = &observabilityGroup{Key: key, Provider: call.Provider, Model: call.Model, Role: call.Role, Workflow: call.Workflow, Stage: call.Stage}
				groups[key] = group
			}
			addObservabilityCall(group, call)
			date := call.Timestamp.UTC().Format("2006-01-02")
			trend := trends[date]
			if trend == nil {
				trend = &observabilityTrend{Date: date}
				trends[date] = trend
			}
			trend.Calls++
			trend.Input += call.Input
			trend.CacheRead += call.CacheRead
			trend.CacheWrite += call.CacheWrite
			trend.CostUSD += call.CostUSD
		}
		daily, err := usageStore.LoadDailyAggregates()
		if err != nil {
			return observabilityReport{}, err
		}
		for _, item := range daily {
			day, err := time.Parse("2006-01-02", item.Date)
			if err != nil || day.Before(from) || !day.Before(to) {
				continue
			}
			addObservabilityDaily(&report.Overall, item)
			callShape := domain.UsageCallEvent{Provider: item.Provider, Model: item.Model, Role: item.Role, Workflow: item.Workflow, Stage: item.Stage}
			key := observabilityGroupKey(callShape, groupBy)
			group := groups[key]
			if group == nil {
				group = &observabilityGroup{Key: key, Provider: item.Provider, Model: item.Model, Role: item.Role, Workflow: item.Workflow, Stage: item.Stage}
				groups[key] = group
			}
			addObservabilityDaily(group, item)
			trend := trends[item.Date]
			if trend == nil {
				trend = &observabilityTrend{Date: item.Date}
				trends[item.Date] = trend
			}
			trend.Calls += item.Calls
			trend.Input += item.Input
			trend.CacheRead += item.CacheRead
			trend.CacheWrite += item.CacheWrite
			trend.CostUSD += item.CostUSD
		}
		state, _ := usageStore.Load()
		if state != nil && state.LegacyAggregate {
			report.Legacy = true
		}
	}
	finalizeObservabilityGroup(&report.Overall)
	for _, group := range groups {
		finalizeObservabilityGroup(group)
		report.Groups = append(report.Groups, *group)
	}
	sort.Slice(report.Groups, func(i, j int) bool {
		if report.Groups[i].CostUSD != report.Groups[j].CostUSD {
			return report.Groups[i].CostUSD > report.Groups[j].CostUSD
		}
		return report.Groups[i].Input > report.Groups[j].Input
	})
	for _, trend := range trends {
		report.Trend = append(report.Trend, *trend)
	}
	sort.Slice(report.Trend, func(i, j int) bool { return report.Trend[i].Date < report.Trend[j].Date })
	return report, nil
}

func addObservabilityDaily(total *observabilityGroup, item domain.UsageDailyAggregate) {
	total.Calls += item.Calls
	total.UsageCalls += item.UsageCalls
	total.Failures += item.Failures
	total.Retries += item.Retries
	total.Input += item.Input
	total.NonCached += maxInt(0, item.Input-item.CacheRead)
	total.Output += item.Output
	total.CacheRead += item.CacheRead
	total.CacheWrite += item.CacheWrite
	total.CostUSD += item.CostUSD
	total.SavedUSD += item.SavedUSD
	total.LatencyMSSum += item.LatencyMSSum
	total.QualitySamples += item.QualitySamples
	total.firstReviewPasses += item.FirstReviewPasses
	total.consistencyPasses += item.ConsistencyPasses
	total.adaptationPasses += item.AdaptationPasses
	total.reviewScoreSum += item.ReviewScoreSum
	total.acceptedRunes += item.AcceptedRunes
}

func maxInt(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func addObservabilityCall(total *observabilityGroup, call domain.UsageCallEvent) {
	total.Calls++
	if call.UsagePresent {
		total.UsageCalls++
	}
	if call.Status != "ok" {
		total.Failures++
	}
	if call.Attempt > 1 {
		total.Retries++
	}
	total.Input += call.Input
	total.NonCached += call.NonCachedInput
	total.Output += call.Output
	total.CacheRead += call.CacheRead
	total.CacheWrite += call.CacheWrite
	total.CostUSD += call.CostUSD
	total.SavedUSD += call.SavedUSD
	total.LatencyMSSum += call.LatencyMS
	if call.FirstReviewPassed != nil {
		total.QualitySamples++
		if *call.FirstReviewPassed {
			total.firstReviewPasses++
		}
	}
	if call.ConsistencyCheckPassed != nil && *call.ConsistencyCheckPassed {
		total.consistencyPasses++
	}
	if call.AdaptationCheckPassed != nil && *call.AdaptationCheckPassed {
		total.adaptationPasses++
	}
	if call.ReviewScore != nil {
		total.reviewScoreSum += *call.ReviewScore
	}
	total.acceptedRunes += call.AcceptedRunes
}

func finalizeObservabilityGroup(group *observabilityGroup) {
	if group.Calls > 0 {
		group.Coverage = float64(group.UsageCalls) / float64(group.Calls)
		group.AvgLatencyMS = float64(group.LatencyMSSum) / float64(group.Calls)
		group.FailureRate = float64(group.Failures) / float64(group.Calls)
		group.RetryRate = float64(group.Retries) / float64(group.Calls)
	}
	group.UsageIncomplete = group.UsageCalls < group.Calls
	group.CacheCapable = observabilityCacheCapability(*group)
	if group.QualitySamples > 0 {
		firstReview := float64(group.firstReviewPasses) / float64(group.QualitySamples)
		consistency := float64(group.consistencyPasses) / float64(group.QualitySamples)
		adaptation := float64(group.adaptationPasses) / float64(group.QualitySamples)
		averageScore := group.reviewScoreSum / float64(group.QualitySamples)
		group.FirstReviewPassRate = &firstReview
		group.ConsistencyPassRate = &consistency
		group.AdaptationPassRate = &adaptation
		group.AverageReviewScore = &averageScore
	}
	if group.acceptedRunes > 0 {
		costPerK := group.CostUSD / (float64(group.acceptedRunes) / 1000)
		group.CostPerAcceptedKRunes = &costPerK
	}
	switch {
	case group.Coverage >= .95 && group.Calls >= 30:
		group.Confidence = "high"
	case group.Coverage >= .80 && group.Calls >= 10:
		group.Confidence = "medium"
	default:
		group.Confidence = "low"
	}
}

func observabilityCacheCapability(group observabilityGroup) *bool {
	if group.CacheRead > 0 || group.CacheWrite > 0 {
		capable := true
		return &capable
	}
	modelName := strings.Trim(strings.TrimSpace(group.Provider)+"/"+strings.TrimSpace(group.Model), "/")
	if modelName == "" {
		return nil
	}
	entry, ok := modelreg.DefaultRegistry().Resolve(modelName)
	if !ok {
		return nil
	}
	capable := entry.CacheReadCostPer1M > 0 || entry.CacheWriteCostPer1M > 0
	return &capable
}

func observabilityGroupKey(call domain.UsageCallEvent, groupBy string) string {
	switch groupBy {
	case "role":
		return call.Role
	case "workflow":
		return call.Workflow
	case "stage":
		return call.Stage
	default:
		return strings.Trim(strings.TrimSpace(call.Provider)+"/"+strings.TrimSpace(call.Model), "/")
	}
}

func parseUsageTime(value string, fallback time.Time) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed
	}
	if parsed, err := time.Parse("2006-01-02", value); err == nil {
		return parsed
	}
	return fallback
}
