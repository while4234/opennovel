package domain

import (
	"encoding/json"
	"strings"
)

// ArcReviewBatchRuneBudget keeps one arc-review dispatch comfortably below
// the production request boundary even if the Editor also emits a small
// amount of unnecessary context. Normal 3k-5k chapters therefore review one
// complete chapter per persisted batch instead of accumulating a whole arc.
const ArcReviewBatchRuneBudget = 5000

// TimelineEvent 时间线事件。
type TimelineEvent struct {
	Chapter    int      `json:"chapter"`
	Time       string   `json:"time"`
	Event      string   `json:"event"`
	Characters []string `json:"characters,omitempty"`
}

func (e *TimelineEvent) UnmarshalJSON(data []byte) error {
	var legacy string
	if err := json.Unmarshal(data, &legacy); err == nil {
		*e = TimelineEvent{Event: strings.TrimSpace(legacy)}
		return nil
	}

	type timelineEvent TimelineEvent
	var event timelineEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	*e = TimelineEvent(event)
	return nil
}

// ForeshadowEntry 伏笔条目。
type ForeshadowEntry struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	PlantedAt   int    `json:"planted_at"`
	Status      string `json:"status"` // planted / advanced / resolved
	ResolvedAt  int    `json:"resolved_at,omitempty"`
}

// ForeshadowUpdate 伏笔增量操作。
type ForeshadowUpdate struct {
	ID          string `json:"id"`
	Action      string `json:"action"` // plant / advance / resolve
	Description string `json:"description,omitempty"`
}

// RelationshipEntry 人物关系条目。
type RelationshipEntry struct {
	SourceCharacterID string `json:"source_character_id,omitempty"`
	TargetCharacterID string `json:"target_character_id,omitempty"`
	CharacterA        string `json:"character_a"`
	CharacterB        string `json:"character_b"`
	Relation          string `json:"relation"`
	Chapter           int    `json:"chapter"`
}

// ConsistencyIssue 一致性问题。
type ConsistencyIssue struct {
	Type          string `json:"type"`     // consistency / character / pacing / continuity / foreshadow / hook / aesthetic
	Severity      string `json:"severity"` // critical / error / warning
	CharacterID   string `json:"character_id,omitempty"`
	Scene         string `json:"scene,omitempty"`
	ViolatedField string `json:"violated_field,omitempty"`
	Description   string `json:"description"`
	Evidence      string `json:"evidence,omitempty"` // 证据：原文片段、具体情节或状态数据
	Suggestion    string `json:"suggestion,omitempty"`
}

// DimensionScore 单维度评审评分。
type DimensionScore struct {
	Dimension string `json:"dimension"`         // consistency / character / pacing / continuity / foreshadow / hook / aesthetic
	Score     int    `json:"score"`             // 0-100
	Verdict   string `json:"verdict"`           // pass / warning / fail
	Comment   string `json:"comment,omitempty"` // 该维度的简要结论
}

// ReviewEntry Editor 的审阅条目。
type ReviewEntry struct {
	Chapter                  int                       `json:"chapter"`
	DraftSHA256              string                    `json:"draft_sha256,omitempty"`
	SimulationCheckDigest    string                    `json:"simulation_check_digest,omitempty"`
	SimulationShouldFindings []SimulationShouldFinding `json:"simulation_should_findings,omitempty"`
	Scope                    string                    `json:"scope"` // chapter / global / arc / arc_batch
	Volume                   int                       `json:"volume,omitempty"`
	Arc                      int                       `json:"arc,omitempty"`
	BatchFrom                int                       `json:"batch_from,omitempty"`
	BatchTo                  int                       `json:"batch_to,omitempty"`
	Issues                   []ConsistencyIssue        `json:"issues"`
	Dimensions               []DimensionScore          `json:"dimensions,omitempty"`      // 分维度评分
	ContractStatus           string                    `json:"contract_status,omitempty"` // met / partial / missed
	ContractMisses           []string                  `json:"contract_misses,omitempty"` // 未达成的 contract 条目
	ContractNotes            string                    `json:"contract_notes,omitempty"`  // 对 contract 履行情况的简述
	Verdict                  string                    `json:"verdict"`                   // accept / polish / rewrite
	Summary                  string                    `json:"summary"`
	AffectedChapters         []int                     `json:"affected_chapters,omitempty"` // 需要重写/打磨的章节号
}

// SimulationShouldFinding is Editor-owned subjective evidence. Deterministic
// copy and measurable-must facts remain owned by SimulationCheckReport.
type SimulationShouldFinding struct {
	FeatureID  string `json:"feature_id"`
	Evidence   string `json:"evidence"`
	Suggestion string `json:"suggestion"`
}

// CriticalCount 返回 critical 级别问题数量。
func (r *ReviewEntry) CriticalCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "critical" {
			n++
		}
	}
	return n
}

// ErrorCount 返回 error 级别问题数量。
func (r *ReviewEntry) ErrorCount() int {
	n := 0
	for _, issue := range r.Issues {
		if issue.Severity == "error" {
			n++
		}
	}
	return n
}

// Dimension 返回指定维度的评分；不存在则返回 nil。
func (r *ReviewEntry) Dimension(name string) *DimensionScore {
	if r == nil {
		return nil
	}
	for i := range r.Dimensions {
		if r.Dimensions[i].Dimension == name {
			return &r.Dimensions[i]
		}
	}
	return nil
}
