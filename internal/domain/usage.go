package domain

import "time"

// UsageSchemaVersion 是 meta/usage.json 的兼容版本号。
// 未来若 AgentUsageTotals 字段语义变化，递增此值；UsageStore.Load 见到不同版本应忽略并触发 replay 重建。
const UsageSchemaVersion = 3

// UsageState 是累计 token / cost 用量的可持久化快照。
// 内存中由 UsageTracker 维护，定期 debounce 落盘到 meta/usage.json。
//
// 注意：UsageTracker 内部的滑动窗 samples（"近 N 次命中率"）**不持久化**——
// 它只服务 UI 短期诊断，进程重启从空开始重新积累几轮即可恢复语义。
// MissingAssistantUsage 保留持久化，跨重启累积更有诊断价值。
type UsageState struct {
	Schema          int                         `json:"schema"`
	UpdatedAt       time.Time                   `json:"updated_at"`
	Overall         AgentUsageTotals            `json:"overall"`
	PerAgent        map[string]AgentUsageTotals `json:"per_agent"`
	PerModel        map[string]AgentUsageTotals `json:"per_model,omitempty"`
	MissingUsage    int                         `json:"missing_assistant_usage"`
	LegacyAggregate bool                        `json:"legacy_aggregate,omitempty"`
	CoverageUnknown bool                        `json:"coverage_unknown,omitempty"`
}

// AgentUsageTotals 是单个角色（或 overall）累计计数的可持久化形态。
type AgentUsageTotals struct {
	Input        int     `json:"input"`
	Output       int     `json:"output"`
	CacheRead    int     `json:"cache_read"`
	CacheWrite   int     `json:"cache_write"`
	Cost         float64 `json:"cost_usd"`
	Saved        float64 `json:"saved_usd"`
	CacheCapable bool    `json:"cache_capable"`
}

// UsageCallEvent is a privacy-safe fact record for one completed model call.
// It intentionally excludes prompts, responses, tool arguments and reasoning.
type UsageCallEvent struct {
	Schema                 int       `json:"schema"`
	ID                     string    `json:"id"`
	Timestamp              time.Time `json:"timestamp"`
	ProjectID              string    `json:"project_id,omitempty"`
	RunID                  string    `json:"run_id,omitempty"`
	Workflow               string    `json:"workflow,omitempty"`
	Stage                  string    `json:"stage,omitempty"`
	Role                   string    `json:"role"`
	CallKind               string    `json:"call_kind"`
	Provider               string    `json:"provider,omitempty"`
	Model                  string    `json:"model,omitempty"`
	Input                  int       `json:"input_tokens"`
	NonCachedInput         int       `json:"non_cached_input_tokens"`
	Output                 int       `json:"output_tokens"`
	CacheRead              int       `json:"cache_read_tokens"`
	CacheWrite             int       `json:"cache_write_tokens"`
	CostUSD                float64   `json:"cost_usd"`
	SavedUSD               float64   `json:"saved_usd"`
	CostSource             string    `json:"cost_source"`
	PriceVersion           string    `json:"price_version,omitempty"`
	LatencyMS              int64     `json:"latency_ms,omitempty"`
	Attempt                int       `json:"attempt"`
	RetryReason            string    `json:"retry_reason,omitempty"`
	Status                 string    `json:"status"`
	UsagePresent           bool      `json:"usage_present"`
	FirstReviewPassed      *bool     `json:"first_review_passed,omitempty"`
	ConsistencyCheckPassed *bool     `json:"consistency_check_passed,omitempty"`
	AdaptationCheckPassed  *bool     `json:"adaptation_check_passed,omitempty"`
	ReviewScore            *float64  `json:"review_score,omitempty"`
	AcceptedRunes          int       `json:"accepted_runes,omitempty"`
}

type UsageDailyAggregate struct {
	Date              string  `json:"date"`
	Provider          string  `json:"provider,omitempty"`
	Model             string  `json:"model,omitempty"`
	Role              string  `json:"role,omitempty"`
	Workflow          string  `json:"workflow,omitempty"`
	Stage             string  `json:"stage,omitempty"`
	Calls             int     `json:"calls"`
	UsageCalls        int     `json:"usage_calls"`
	Failures          int     `json:"failures"`
	Retries           int     `json:"retries"`
	Input             int     `json:"input_tokens"`
	Output            int     `json:"output_tokens"`
	CacheRead         int     `json:"cache_read_tokens"`
	CacheWrite        int     `json:"cache_write_tokens"`
	CostUSD           float64 `json:"cost_usd"`
	SavedUSD          float64 `json:"saved_usd"`
	LatencyMSSum      int64   `json:"latency_ms_sum"`
	QualitySamples    int     `json:"quality_samples"`
	FirstReviewPasses int     `json:"first_review_passes"`
	ConsistencyPasses int     `json:"consistency_passes"`
	AdaptationPasses  int     `json:"adaptation_passes"`
	ReviewScoreSum    float64 `json:"review_score_sum"`
	AcceptedRunes     int     `json:"accepted_runes"`
}
