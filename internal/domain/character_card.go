package domain

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
)

const CharacterCardLifecycleVersion = 1
const CharacterCardCandidateVersion = 1

const (
	characterCardEvidenceReferenceLimit = 256
	characterCardEvidenceSummaryLimit   = 1000
)

type CharacterTier string

const (
	CharacterTierCore       CharacterTier = "core"
	CharacterTierImportant  CharacterTier = "important"
	CharacterTierSecondary  CharacterTier = "secondary"
	CharacterTierDecorative CharacterTier = "decorative"
)

type CharacterCardCompletenessStatus string

const (
	CharacterCardComplete   CharacterCardCompletenessStatus = "complete"
	CharacterCardIncomplete CharacterCardCompletenessStatus = "incomplete"
)

type CharacterCardSeverity string

const (
	CharacterCardSeverityWarning  CharacterCardSeverity = "warning"
	CharacterCardSeverityBlocking CharacterCardSeverity = "blocking"
)

type CharacterCardMissingItem struct {
	Code        string                `json:"code"`
	Field       string                `json:"field,omitempty"`
	Severity    CharacterCardSeverity `json:"severity"`
	Description string                `json:"description"`
}

type CharacterCardCompletenessResult struct {
	CharacterID string                          `json:"character_id"`
	Tier        CharacterTier                   `json:"tier"`
	Status      CharacterCardCompletenessStatus `json:"status"`
	Missing     []CharacterCardMissingItem      `json:"missing"`
}

// EvaluateCharacterCardCompleteness is the shared deterministic completeness
// contract for agents, workflows, and UI consumers.
func EvaluateCharacterCardCompleteness(
	foundation StoryFoundation,
	coreCast *CoreCastContract,
) ([]CharacterCardCompletenessResult, error) {
	normalized, err := NormalizeStoryFoundation(foundation)
	if err != nil {
		return nil, err
	}
	coreDeclarations := make(map[string]bool)
	if coreCast != nil {
		contract, normalizeErr := NormalizeCoreCastContract(*coreCast)
		if normalizeErr != nil {
			return nil, normalizeErr
		}
		for _, member := range contract.Members {
			coreDeclarations[member.Character.ID] = member.NoCoreRelationships
		}
	}
	relationshipCount := make(map[string]int, len(normalized.Characters))
	for _, relationship := range normalized.Relationships {
		relationshipCount[relationship.SourceCharacterID]++
		relationshipCount[relationship.TargetCharacterID]++
	}
	results := make([]CharacterCardCompletenessResult, 0, len(normalized.Characters))
	for _, character := range normalized.Characters {
		tier, tierErr := normalizedCharacterTier(character.Tier)
		if tierErr != nil {
			return nil, fmt.Errorf("character %q: %w", character.ID, tierErr)
		}
		missing := characterCardMissing(
			character,
			tier,
			relationshipCount[character.ID] > 0,
			normalized.RelationshipsReviewed || coreDeclarations[character.ID],
		)
		status := CharacterCardComplete
		for _, item := range missing {
			if item.Severity == CharacterCardSeverityBlocking {
				status = CharacterCardIncomplete
				break
			}
		}
		if missing == nil {
			missing = []CharacterCardMissingItem{}
		}
		results = append(results, CharacterCardCompletenessResult{
			CharacterID: character.ID,
			Tier:        tier,
			Status:      status,
			Missing:     missing,
		})
	}
	return results, nil
}

func characterCardMissing(
	character Character,
	tier CharacterTier,
	hasRelationship bool,
	noRelationshipReviewed bool,
) []CharacterCardMissingItem {
	var missing []CharacterCardMissingItem
	require := func(code, field, value, description string) {
		if strings.TrimSpace(value) == "" {
			missing = appendCharacterCardMissing(missing, code, field, description)
		}
	}
	requireIdentity := func() {
		require("name_required", "name", character.Name, "角色姓名不能为空")
		require("role_required", "role", character.Role, "角色身份或故事职责不能为空")
	}
	requireGender := func() {
		require("gender_required", "gender", character.Gender, "非装饰角色需要明确性别，以固定称谓与代词")
	}
	requireInitialState := func() {
		if !hasCharacterInitialState(character.InitialState) {
			missing = appendCharacterCardMissing(missing, "initial_state_required", "initial_state", "需要说明故事开始时的身份、处境、情绪、资源或主要关系")
		}
	}
	requireKnowledgeBoundary := func() {
		if character.KnowledgeBoundary == nil {
			missing = appendCharacterCardMissing(missing, "knowledge_boundary_required", "knowledge_boundary", "核心与重要角色需要明确已知、未知、误解或禁用信息，避免越界知情")
		}
	}
	requireRelationship := func(allowReviewedNone bool) {
		if !hasRelationship && !(allowReviewedNone && noRelationshipReviewed) {
			missing = appendCharacterCardMissing(missing, "relationship_required", "relationships", "需要规范关系，或明确确认没有核心关系")
		}
	}

	switch tier {
	case CharacterTierCore:
		requireIdentity()
		requireGender()
		require("description_required", "description", character.Description, "核心角色需要人物描述")
		require("goal_required", "goal", character.Goal, "核心角色需要外在目标")
		require("motivation_required", "motivation", character.Motivation, "核心角色需要内在动机")
		require("conflict_required", "conflict", character.Conflict, "核心角色需要核心冲突")
		require("arc_required", "arc", character.Arc, "核心角色需要角色弧")
		if len(character.Traits) == 0 && character.Voice == "" {
			missing = appendCharacterCardMissing(missing, "traits_or_voice_required", "traits", "核心角色至少需要一项特质或明确语言风格")
		}
		requireInitialState()
		requireKnowledgeBoundary()
		if len(character.Constraints) == 0 {
			missing = appendCharacterCardMissing(missing, "constraints_required", "constraints", "核心角色至少需要一条约束")
		}
		requireRelationship(true)
	case CharacterTierImportant:
		requireIdentity()
		requireGender()
		require("description_required", "description", character.Description, "重要角色需要说明其对主线的作用")
		require("goal_required", "goal", character.Goal, "重要角色需要独立目标")
		require("motivation_required", "motivation", character.Motivation, "重要角色需要独立动机")
		require("conflict_required", "conflict", character.Conflict, "重要角色需要冲突")
		require("arc_required", "arc", character.Arc, "重要角色需要变化方向")
		if len(character.Traits) == 0 && character.Voice == "" && len(character.Constraints) == 0 {
			missing = appendCharacterCardMissing(missing, "distinguishing_feature_required", "traits", "重要角色需要语言或行为特征")
		}
		requireInitialState()
		requireKnowledgeBoundary()
		requireRelationship(false)
	case CharacterTierSecondary:
		requireIdentity()
		requireGender()
		if character.Goal == "" && character.Motivation == "" {
			missing = appendCharacterCardMissing(missing, "goal_or_motivation_required", "goal", "次要角色至少需要一个可驱动行为的目标或动机")
		}
		if len(character.Traits) == 0 && len(character.ContrastDetails) == 0 && character.Voice == "" {
			missing = appendCharacterCardMissing(missing, "distinguishing_feature_required", "traits", "次要角色需要可辨识特征、反差或语言风格")
		}
		requireInitialState()
		requireRelationship(false)
	case CharacterTierDecorative:
		if character.ID == "" && character.Name == "" && character.Role == "" {
			missing = appendCharacterCardMissing(missing, "identity_or_function_required", "name", "装饰角色需要稳定身份或明确场景功能")
		}
	}
	return missing
}

func appendCharacterCardMissing(items []CharacterCardMissingItem, code, field, description string) []CharacterCardMissingItem {
	return append(items, CharacterCardMissingItem{
		Code:        code,
		Field:       field,
		Severity:    CharacterCardSeverityBlocking,
		Description: description,
	})
}

func normalizedCharacterTier(value string) (CharacterTier, error) {
	tier := CharacterTier(strings.ToLower(strings.TrimSpace(value)))
	if tier == "" {
		tier = CharacterTierImportant
	}
	switch tier {
	case CharacterTierCore, CharacterTierImportant, CharacterTierSecondary, CharacterTierDecorative:
		return tier, nil
	default:
		return "", fmt.Errorf("tier %q is invalid", value)
	}
}

func CloneCharacter(in Character) Character {
	out := in
	out.Aliases = append([]string(nil), in.Aliases...)
	out.Traits = append([]string(nil), in.Traits...)
	out.Constraints = append([]string(nil), in.Constraints...)
	out.ContrastDetails = append([]CharacterContrastDetail(nil), in.ContrastDetails...)
	out.KeyBackstory = append([]CharacterBackstory(nil), in.KeyBackstory...)
	if in.InitialState != nil {
		initial := *in.InitialState
		initial.Resources = append([]string(nil), in.InitialState.Resources...)
		out.InitialState = &initial
	}
	if in.KnowledgeBoundary != nil {
		knowledge := *in.KnowledgeBoundary
		knowledge.Known = append([]string(nil), in.KnowledgeBoundary.Known...)
		knowledge.Unknown = append([]string(nil), in.KnowledgeBoundary.Unknown...)
		knowledge.Misconceptions = append([]string(nil), in.KnowledgeBoundary.Misconceptions...)
		knowledge.Forbidden = append([]string(nil), in.KnowledgeBoundary.Forbidden...)
		out.KnowledgeBoundary = &knowledge
	}
	return out
}

func normalizeCharacter(character *Character) {
	character.ID = strings.TrimSpace(character.ID)
	character.Name = strings.TrimSpace(character.Name)
	character.Role = strings.TrimSpace(character.Role)
	character.Gender = strings.ToLower(strings.TrimSpace(character.Gender))
	character.Description = strings.TrimSpace(character.Description)
	character.Arc = strings.TrimSpace(character.Arc)
	character.Tier = strings.ToLower(strings.TrimSpace(character.Tier))
	character.Faction = strings.TrimSpace(character.Faction)
	character.Goal = strings.TrimSpace(character.Goal)
	character.Motivation = strings.TrimSpace(character.Motivation)
	character.Conflict = strings.TrimSpace(character.Conflict)
	character.Voice = strings.TrimSpace(character.Voice)
	character.Notes = strings.TrimSpace(character.Notes)
	character.Aliases = normalizedStrings(character.Aliases)
	character.Traits = normalizedStrings(character.Traits)
	character.Constraints = normalizedStrings(character.Constraints)
	normalizeCharacterCardFields(character)
}

func normalizeCharacterCardFields(character *Character) {
	for i := range character.ContrastDetails {
		character.ContrastDetails[i].Surface = strings.TrimSpace(character.ContrastDetails[i].Surface)
		character.ContrastDetails[i].Depth = strings.TrimSpace(character.ContrastDetails[i].Depth)
	}
	character.ContrastDetails = compactContrasts(character.ContrastDetails)
	for i := range character.KeyBackstory {
		character.KeyBackstory[i].Event = strings.TrimSpace(character.KeyBackstory[i].Event)
		character.KeyBackstory[i].Impact = strings.TrimSpace(character.KeyBackstory[i].Impact)
	}
	character.KeyBackstory = compactBackstory(character.KeyBackstory)
	if character.InitialState != nil {
		character.InitialState.Identity = strings.TrimSpace(character.InitialState.Identity)
		character.InitialState.Situation = strings.TrimSpace(character.InitialState.Situation)
		character.InitialState.Emotion = strings.TrimSpace(character.InitialState.Emotion)
		character.InitialState.Resources = normalizedStrings(character.InitialState.Resources)
		character.InitialState.Relationships = strings.TrimSpace(character.InitialState.Relationships)
		if !hasCharacterInitialState(character.InitialState) {
			character.InitialState = nil
		}
	}
	if character.KnowledgeBoundary != nil {
		character.KnowledgeBoundary.Known = normalizedStrings(character.KnowledgeBoundary.Known)
		character.KnowledgeBoundary.Unknown = normalizedStrings(character.KnowledgeBoundary.Unknown)
		character.KnowledgeBoundary.Misconceptions = normalizedStrings(character.KnowledgeBoundary.Misconceptions)
		character.KnowledgeBoundary.Forbidden = normalizedStrings(character.KnowledgeBoundary.Forbidden)
		if len(character.KnowledgeBoundary.Known)+len(character.KnowledgeBoundary.Unknown)+
			len(character.KnowledgeBoundary.Misconceptions)+len(character.KnowledgeBoundary.Forbidden) == 0 {
			character.KnowledgeBoundary = nil
		}
	}
}

func compactContrasts(values []CharacterContrastDetail) []CharacterContrastDetail {
	out := values[:0]
	for _, value := range values {
		if value.Surface != "" || value.Depth != "" {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Surface+"\x00"+out[i].Depth < out[j].Surface+"\x00"+out[j].Depth
	})
	return out
}

func compactBackstory(values []CharacterBackstory) []CharacterBackstory {
	out := values[:0]
	for _, value := range values {
		if value.Event != "" || value.Impact != "" {
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Event+"\x00"+out[i].Impact < out[j].Event+"\x00"+out[j].Impact
	})
	return out
}

func hasCharacterInitialState(value *CharacterInitialState) bool {
	return value != nil && (value.Identity != "" || value.Situation != "" || value.Emotion != "" ||
		len(value.Resources) > 0 || value.Relationships != "")
}

type CharacterCardProjectMode string

const (
	CharacterCardProjectOriginal   CharacterCardProjectMode = "original"
	CharacterCardProjectAdaptation CharacterCardProjectMode = "adaptation"
)

type CharacterCardAnalysisStatus string

const (
	CharacterCardAnalysisNotGenerated   CharacterCardAnalysisStatus = "not_generated"
	CharacterCardAnalysisCandidateReady CharacterCardAnalysisStatus = "candidate_ready"
	CharacterCardAnalysisFailed         CharacterCardAnalysisStatus = "failed"
	CharacterCardAnalysisStale          CharacterCardAnalysisStatus = "stale"
)

type CharacterCardReviewStatus string

const (
	CharacterCardReviewNotReviewed   CharacterCardReviewStatus = "not_reviewed"
	CharacterCardReviewInProgress    CharacterCardReviewStatus = "in_progress"
	CharacterCardReviewPassed        CharacterCardReviewStatus = "passed"
	CharacterCardReviewNeedsRevision CharacterCardReviewStatus = "needs_revision"
	CharacterCardReviewFailed        CharacterCardReviewStatus = "failed"
	CharacterCardReviewStale         CharacterCardReviewStatus = "stale"
)

type CharacterCardConfirmationStatus string

const (
	CharacterCardUnconfirmed       CharacterCardConfirmationStatus = "unconfirmed"
	CharacterCardConfirmed         CharacterCardConfirmationStatus = "confirmed"
	CharacterCardConfirmationStale CharacterCardConfirmationStatus = "stale"
)

type CharacterCardCandidateReference struct {
	FoundationRevision       int64  `json:"foundation_revision"`
	FoundationAuditSignature string `json:"foundation_audit_signature"`
	CharacterContentDigest   string `json:"character_content_digest"`
}

type CharacterCardNamedSignature struct {
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

// CharacterCardInputSignatures stores only compact input identities. It never
// stores source text or model requests.
type CharacterCardInputSignatures struct {
	CreativeBrief    string                        `json:"creative_brief,omitempty"`
	CoreCast         string                        `json:"core_cast,omitempty"`
	SourceFoundation string                        `json:"source_foundation,omitempty"`
	AdaptationIntent string                        `json:"adaptation_intent,omitempty"`
	Additional       []CharacterCardNamedSignature `json:"additional,omitempty"`
}

type CharacterCardBinding struct {
	Candidate   CharacterCardCandidateReference `json:"candidate"`
	Inputs      CharacterCardInputSignatures    `json:"inputs"`
	InputDigest string                          `json:"input_digest"`
}

type CharacterCardFindingScope string

const (
	CharacterCardFindingGlobal    CharacterCardFindingScope = "global"
	CharacterCardFindingCharacter CharacterCardFindingScope = "character"
)

type CharacterCardReviewFinding struct {
	ID              string                    `json:"id"`
	Scope           CharacterCardFindingScope `json:"scope"`
	CharacterID     string                    `json:"character_id,omitempty"`
	Location        string                    `json:"location,omitempty"`
	Severity        CharacterCardSeverity     `json:"severity"`
	IssueType       string                    `json:"issue_type"`
	Description     string                    `json:"description"`
	EvidenceSummary string                    `json:"evidence_summary,omitempty"`
	Suggestion      string                    `json:"suggestion,omitempty"`
	Blocking        bool                      `json:"blocking"`
}

type CharacterSourceEvidenceKind string

const (
	CharacterSourceOriginalFact       CharacterSourceEvidenceKind = "source_fact"
	CharacterSourceAdaptationDecision CharacterSourceEvidenceKind = "adaptation_decision"
	CharacterSourceOriginalAddition   CharacterSourceEvidenceKind = "target_original_addition"
)

type CharacterSourceEvidence struct {
	Kind      CharacterSourceEvidenceKind `json:"kind"`
	Reference string                      `json:"reference"`
	Summary   string                      `json:"summary,omitempty"`
}

type CharacterSourceMappingAction string

const (
	CharacterSourceKeep           CharacterSourceMappingAction = "keep"
	CharacterSourceRename         CharacterSourceMappingAction = "rename"
	CharacterSourceMerge          CharacterSourceMappingAction = "merge"
	CharacterSourceSplit          CharacterSourceMappingAction = "split"
	CharacterSourceExclude        CharacterSourceMappingAction = "exclude"
	CharacterSourceTargetOriginal CharacterSourceMappingAction = "target_original"
)

type CharacterSourceMapping struct {
	ID                 string                       `json:"id"`
	Action             CharacterSourceMappingAction `json:"action"`
	SourceCharacterIDs []string                     `json:"source_character_ids,omitempty"`
	TargetCharacterIDs []string                     `json:"target_character_ids,omitempty"`
	Rationale          string                       `json:"rationale"`
	Evidence           []CharacterSourceEvidence    `json:"evidence,omitempty"`
}

type CharacterCardError struct {
	Class   string `json:"class"`
	Message string `json:"message"`
}

// CharacterCardLifecycle is sidecar metadata. Character bodies and planned
// relationships remain exclusively in StoryFoundation.
type CharacterCardLifecycle struct {
	Version             int                               `json:"version"`
	Revision            int64                             `json:"revision"`
	Mode                CharacterCardProjectMode          `json:"mode"`
	Candidate           CharacterCardCandidateReference   `json:"candidate"`
	Inputs              CharacterCardInputSignatures      `json:"inputs"`
	InputDigest         string                            `json:"input_digest"`
	AnalysisSummary     string                            `json:"analysis_summary,omitempty"`
	Completeness        []CharacterCardCompletenessResult `json:"completeness"`
	AnalysisStatus      CharacterCardAnalysisStatus       `json:"analysis_status"`
	ReviewStatus        CharacterCardReviewStatus         `json:"review_status"`
	ReviewedCandidate   CharacterCardCandidateReference   `json:"reviewed_candidate"`
	ReviewedInputDigest string                            `json:"reviewed_input_digest,omitempty"`
	ReviewSummary       string                            `json:"review_summary,omitempty"`
	Findings            []CharacterCardReviewFinding      `json:"findings"`
	ConfirmationStatus  CharacterCardConfirmationStatus   `json:"confirmation_status"`
	RunID               string                            `json:"run_id,omitempty"`
	IdempotencyKey      string                            `json:"idempotency_key,omitempty"`
	SubmissionDigest    string                            `json:"submission_digest,omitempty"`
	Error               *CharacterCardError               `json:"error,omitempty"`
	SourceMappings      []CharacterSourceMapping          `json:"source_mappings"`
	Coverage            *AdaptationCharacterCoverage      `json:"coverage,omitempty"`
	CreatedAt           string                            `json:"created_at,omitempty"`
	UpdatedAt           string                            `json:"updated_at,omitempty"`
}

// CharacterCardCandidate is the durable staging boundary between Character
// analysis and canonical StoryFoundation publication. Base binds the candidate
// to the exact canonical Foundation and creative inputs that were analyzed.
type CharacterCardCandidate struct {
	Version       int                  `json:"version"`
	Revision      int64                `json:"revision"`
	Base          CharacterCardBinding `json:"base"`
	Foundation    StoryFoundation      `json:"foundation"`
	ProjectedCast CoreCastContract     `json:"projected_core_cast"`
	CreatedAt     string               `json:"created_at,omitempty"`
	UpdatedAt     string               `json:"updated_at,omitempty"`
}

// ProjectCharacterCandidateCoreCast deterministically selects the complete
// candidate's core tier and the relationships wholly contained by that tier.
// A confirmed legacy CoreCast is immutable input: semantic conflicts are
// returned as blocking findings rather than silently resolved.
func ProjectCharacterCandidateCoreCast(
	foundation StoryFoundation,
	existing *CoreCastContract,
) (CoreCastContract, []CharacterCardReviewFinding, error) {
	normalized, err := NormalizeStoryFoundation(foundation)
	if err != nil {
		return CoreCastContract{}, nil, err
	}
	core := make(map[string]Character)
	for _, character := range normalized.Characters {
		tier, tierErr := normalizedCharacterTier(character.Tier)
		if tierErr != nil {
			return CoreCastContract{}, nil, tierErr
		}
		if tier == CharacterTierCore {
			core[character.ID] = CloneCharacter(character)
		}
	}
	if len(core) == 0 {
		return CoreCastContract{}, []CharacterCardReviewFinding{{
			ID:          "core_cast:no_core_character",
			Scope:       CharacterCardFindingGlobal,
			Severity:    CharacterCardSeverityBlocking,
			IssueType:   "core_cast_projection",
			Description: "完整角色候选中没有 tier=core 的核心角色",
			Suggestion:  "至少将一名主角标记为 core 后重新审核",
			Blocking:    true,
		}}, nil
	}

	var findings []CharacterCardReviewFinding
	var normalizedExisting *CoreCastContract
	if existing != nil && existing.ConfirmedSignature != "" {
		confirmed, normalizeErr := NormalizeCoreCastContract(*existing)
		if normalizeErr != nil {
			return CoreCastContract{}, nil, normalizeErr
		}
		normalizedExisting = &confirmed
		for _, member := range confirmed.Members {
			candidate, ok := core[member.Character.ID]
			if !ok || !reflect.DeepEqual(candidate, member.Character) {
				findings = append(findings, CharacterCardReviewFinding{
					ID:              "core_cast:confirmed_conflict:" + member.Character.ID,
					Scope:           CharacterCardFindingCharacter,
					CharacterID:     member.Character.ID,
					Location:        "projected_core_cast",
					Severity:        CharacterCardSeverityBlocking,
					IssueType:       "confirmed_core_cast_conflict",
					Description:     "角色候选与已确认 CoreCast 核心事实冲突",
					EvidenceSummary: "confirmed CoreCast member " + member.Character.ID,
					Suggestion:      "由用户明确保留旧事实或修改候选后重新审核",
					Blocking:        true,
				})
			}
		}
	}

	ids := make([]string, 0, len(core))
	for id := range core {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	members := make([]CoreCastMember, 0, len(ids))
	existingMembers := make(map[string]CoreCastMember)
	if normalizedExisting != nil {
		for _, member := range normalizedExisting.Members {
			existingMembers[member.Character.ID] = member
		}
	}
	relationshipCount := make(map[string]int, len(ids))
	coreRelationships := make([]CharacterRelationship, 0)
	for _, relationship := range normalized.Relationships {
		if _, source := core[relationship.SourceCharacterID]; !source {
			continue
		}
		if _, target := core[relationship.TargetCharacterID]; !target {
			continue
		}
		coreRelationships = append(coreRelationships, relationship)
		relationshipCount[relationship.SourceCharacterID]++
		relationshipCount[relationship.TargetCharacterID]++
	}
	for _, id := range ids {
		character := core[id]
		importance := CoreCastImportanceMajorSupport
		if characterRoleIsProtagonist(character.Role) {
			importance = CoreCastImportanceProtagonist
		}
		member := CoreCastMember{
			Character:           character,
			Importance:          importance,
			Origin:              CoreCastOriginOriginal,
			MainlineFunction:    character.Role,
			InclusionRationale:  "projected from reviewed full character candidate",
			NoCoreRelationships: relationshipCount[id] == 0 && normalized.RelationshipsReviewed,
		}
		if existingMember, ok := existingMembers[id]; ok {
			member.Importance = existingMember.Importance
			if characterRoleIsProtagonist(character.Role) {
				member.Importance = CoreCastImportanceProtagonist
			}
			member.Origin = existingMember.Origin
			member.MainlineFunction = existingMember.MainlineFunction
			member.InclusionRationale = existingMember.InclusionRationale
			member.SourceCharacterIDs = append([]string(nil), existingMember.SourceCharacterIDs...)
		}
		members = append(members, member)
	}
	projectedValue := CoreCastContract{
		Version:              CoreCastContractVersion,
		Mode:                 CoreCastModeNormal,
		Members:              members,
		PlannedRelationships: coreRelationships,
		SourceDispositions:   []SourceCharacterDisposition{},
	}
	if normalizedExisting != nil && normalizedExisting.Mode == CoreCastModeAdaptation {
		projectedValue.Mode = CoreCastModeAdaptation
		projectedValue.DraftRevision = normalizedExisting.DraftRevision
		projectedValue.DraftHash = normalizedExisting.DraftHash
		projectedValue.SourceSignature = normalizedExisting.SourceSignature
		projectedValue.AdaptationIntentHash = normalizedExisting.AdaptationIntentHash
		projectedValue.SourceDispositions = append(
			[]SourceCharacterDisposition(nil),
			normalizedExisting.SourceDispositions...,
		)
	}
	projected, err := NormalizeCoreCastContract(projectedValue)
	return projected, findings, err
}

func characterRoleIsProtagonist(role string) bool {
	role = strings.ToLower(strings.TrimSpace(role))
	return strings.Contains(role, "主角") ||
		strings.Contains(role, "男主") ||
		strings.Contains(role, "女主") ||
		strings.Contains(role, "主视角") ||
		strings.Contains(role, "protagonist") ||
		strings.Contains(role, "lead")
}

func CharacterCardBindingFromFoundation(
	foundation StoryFoundation,
	inputs CharacterCardInputSignatures,
) (CharacterCardBinding, error) {
	normalized, err := NormalizeStoryFoundation(foundation)
	if err != nil {
		return CharacterCardBinding{}, err
	}
	audit, err := FoundationAuditSignature(normalized)
	if err != nil {
		return CharacterCardBinding{}, err
	}
	digest, err := CharacterCardContentDigest(normalized)
	if err != nil {
		return CharacterCardBinding{}, err
	}
	inputs = normalizeCharacterCardInputs(inputs)
	if err := validateCharacterCardInputs(inputs); err != nil {
		return CharacterCardBinding{}, err
	}
	inputDigest, err := jsonSignature(inputs)
	if err != nil {
		return CharacterCardBinding{}, err
	}
	return CharacterCardBinding{
		Candidate: CharacterCardCandidateReference{
			FoundationRevision:       normalized.Revision,
			FoundationAuditSignature: audit,
			CharacterContentDigest:   digest,
		},
		Inputs:      inputs,
		InputDigest: inputDigest,
	}, nil
}

func CharacterCardContentDigest(foundation StoryFoundation) (string, error) {
	normalized, err := NormalizeStoryFoundation(foundation)
	if err != nil {
		return "", err
	}
	return jsonSignature(struct {
		Characters    []Character             `json:"characters"`
		Relationships []CharacterRelationship `json:"relationships"`
	}{normalized.Characters, normalized.Relationships})
}

func NormalizeCharacterCardLifecycle(value CharacterCardLifecycle) (CharacterCardLifecycle, error) {
	out := cloneCharacterCardLifecycle(value)
	if out.Version == 0 {
		out.Version = CharacterCardLifecycleVersion
	}
	if out.Version != CharacterCardLifecycleVersion {
		return CharacterCardLifecycle{}, fmt.Errorf("character card lifecycle version %d is unsupported", out.Version)
	}
	if out.Revision < 0 {
		return CharacterCardLifecycle{}, fmt.Errorf("character card lifecycle revision cannot be negative")
	}
	if out.Mode != CharacterCardProjectOriginal && out.Mode != CharacterCardProjectAdaptation {
		return CharacterCardLifecycle{}, fmt.Errorf("character card project mode %q is invalid", out.Mode)
	}
	out.Inputs = normalizeCharacterCardInputs(out.Inputs)
	if err := validateCharacterCardInputs(out.Inputs); err != nil {
		return CharacterCardLifecycle{}, err
	}
	inputDigest, err := jsonSignature(out.Inputs)
	if err != nil {
		return CharacterCardLifecycle{}, err
	}
	out.InputDigest = inputDigest
	out.AnalysisSummary = strings.TrimSpace(out.AnalysisSummary)
	out.ReviewedInputDigest = strings.TrimSpace(out.ReviewedInputDigest)
	out.ReviewSummary = strings.TrimSpace(out.ReviewSummary)
	out.RunID = strings.TrimSpace(out.RunID)
	out.IdempotencyKey = strings.TrimSpace(out.IdempotencyKey)
	out.SubmissionDigest = strings.TrimSpace(out.SubmissionDigest)
	out.CreatedAt = strings.TrimSpace(out.CreatedAt)
	out.UpdatedAt = strings.TrimSpace(out.UpdatedAt)
	normalizeCandidateReference(&out.Candidate)
	normalizeCandidateReference(&out.ReviewedCandidate)
	for i := range out.Findings {
		finding := &out.Findings[i]
		finding.ID = strings.TrimSpace(finding.ID)
		finding.CharacterID = strings.TrimSpace(finding.CharacterID)
		finding.Location = strings.TrimSpace(finding.Location)
		finding.IssueType = strings.TrimSpace(finding.IssueType)
		finding.Description = strings.TrimSpace(finding.Description)
		finding.EvidenceSummary = strings.TrimSpace(finding.EvidenceSummary)
		finding.Suggestion = strings.TrimSpace(finding.Suggestion)
	}
	sort.Slice(out.Findings, func(i, j int) bool { return out.Findings[i].ID < out.Findings[j].ID })
	for i := range out.SourceMappings {
		normalizeCharacterSourceMapping(&out.SourceMappings[i])
	}
	sort.Slice(out.SourceMappings, func(i, j int) bool { return out.SourceMappings[i].ID < out.SourceMappings[j].ID })
	if out.Findings == nil {
		out.Findings = []CharacterCardReviewFinding{}
	}
	if out.Completeness == nil {
		out.Completeness = []CharacterCardCompletenessResult{}
	}
	if out.SourceMappings == nil {
		out.SourceMappings = []CharacterSourceMapping{}
	}
	if out.Coverage != nil {
		sort.Slice(out.Coverage.Decisions, func(i, j int) bool {
			return out.Coverage.Decisions[i].SourceCharacterID < out.Coverage.Decisions[j].SourceCharacterID
		})
	}
	if out.Error != nil {
		out.Error.Class = strings.TrimSpace(out.Error.Class)
		out.Error.Message = strings.TrimSpace(out.Error.Message)
	}
	if err := validateCharacterCardLifecycle(out); err != nil {
		return CharacterCardLifecycle{}, err
	}
	return out, nil
}

// ReconcileCharacterCardLifecycle deterministically invalidates evidence when
// any canonical character content or binding input changes.
func ReconcileCharacterCardLifecycle(
	value CharacterCardLifecycle,
	current CharacterCardBinding,
) (CharacterCardLifecycle, error) {
	out, err := NormalizeCharacterCardLifecycle(value)
	if err != nil {
		return CharacterCardLifecycle{}, err
	}
	current.Inputs = normalizeCharacterCardInputs(current.Inputs)
	if err := validateCharacterCardInputs(current.Inputs); err != nil {
		return CharacterCardLifecycle{}, err
	}
	if err := validateCharacterCardCandidate(current.Candidate); err != nil {
		return CharacterCardLifecycle{}, fmt.Errorf("current character card binding: %w", err)
	}
	current.InputDigest, err = jsonSignature(current.Inputs)
	if err != nil {
		return CharacterCardLifecycle{}, err
	}
	analysisExists := out.AnalysisStatus != CharacterCardAnalysisNotGenerated
	if analysisExists && (!sameCharacterCardCandidate(out.Candidate, current.Candidate) || out.InputDigest != current.InputDigest) {
		out.AnalysisStatus = CharacterCardAnalysisStale
		if out.ReviewStatus != CharacterCardReviewNotReviewed {
			out.ReviewStatus = CharacterCardReviewStale
			if out.ConfirmationStatus == CharacterCardConfirmed {
				out.ConfirmationStatus = CharacterCardConfirmationStale
			}
		}
		return out, nil
	}
	reviewExists := out.ReviewStatus != CharacterCardReviewNotReviewed
	if reviewExists && (!sameCharacterCardCandidate(out.ReviewedCandidate, current.Candidate) ||
		out.ReviewedInputDigest != current.InputDigest) {
		out.ReviewStatus = CharacterCardReviewStale
		if out.ConfirmationStatus == CharacterCardConfirmed {
			out.ConfirmationStatus = CharacterCardConfirmationStale
		}
	}
	return out, nil
}

// ProjectCoreCastSourceMappings provides a migration/compatibility projection
// from existing core dispositions into the full-cast mapping contract.
func ProjectCoreCastSourceMappings(contract CoreCastContract) ([]CharacterSourceMapping, error) {
	normalized, err := NormalizeCoreCastContract(contract)
	if err != nil {
		return nil, err
	}
	grouped := make(map[string]*CharacterSourceMapping)
	for _, disposition := range normalized.SourceDispositions {
		action := CharacterSourceMappingAction(disposition.Action)
		key := string(action) + "\x00" + strings.Join(disposition.TargetCharacterIDs, "\x00")
		mapping := grouped[key]
		if mapping == nil {
			rationale := disposition.Rationale
			if rationale == "" {
				rationale = "projected from the confirmed CoreCast disposition"
			}
			mapping = &CharacterSourceMapping{
				Action:             action,
				TargetCharacterIDs: append([]string(nil), disposition.TargetCharacterIDs...),
				Rationale:          rationale,
				Evidence: []CharacterSourceEvidence{{
					Kind:      CharacterSourceAdaptationDecision,
					Reference: "core_cast.source_dispositions",
					Summary:   "projected from the confirmed CoreCast disposition",
				}},
			}
			grouped[key] = mapping
		}
		mapping.SourceCharacterIDs = append(mapping.SourceCharacterIDs, disposition.SourceCharacterID)
		if mapping.Rationale == "" {
			mapping.Rationale = disposition.Rationale
		}
	}
	out := make([]CharacterSourceMapping, 0, len(grouped))
	for _, mapping := range grouped {
		normalizeCharacterSourceMapping(mapping)
		mapping.ID = stableFoundationID(
			"char-map",
			string(mapping.Action)+"\x00"+strings.Join(mapping.SourceCharacterIDs, "\x00")+"\x00"+
				strings.Join(mapping.TargetCharacterIDs, "\x00"),
		)
		out = append(out, *mapping)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// ApplyCharacterSourceMappingsToCoreCast projects the reviewed full-cast
// mapping decisions back into the smaller CoreCast compatibility contract.
// formalSourceIDs must come from the reviewed SourceFoundation formal-card
// projection; evidence-only source identities intentionally remain outside
// CoreCast while still retaining their Character lifecycle decisions.
func ApplyCharacterSourceMappingsToCoreCast(
	contract CoreCastContract,
	mappings []CharacterSourceMapping,
	formalSourceIDs []string,
) (CoreCastContract, error) {
	out := cloneCoreCastContract(contract)
	allowedSources := make(map[string]struct{}, len(formalSourceIDs))
	for _, sourceID := range normalizedStrings(formalSourceIDs) {
		allowedSources[sourceID] = struct{}{}
	}
	memberIndexes := make(map[string]int, len(out.Members))
	for index, member := range out.Members {
		memberIndexes[member.Character.ID] = index
	}
	out.SourceDispositions = nil
	claimed := make(map[string]struct{}, len(allowedSources))
	for _, value := range mappings {
		mapping := value
		normalizeCharacterSourceMapping(&mapping)
		if err := validateCharacterSourceMapping(mapping); err != nil {
			return CoreCastContract{}, err
		}
		if mapping.Action == CharacterSourceTargetOriginal {
			continue
		}
		for _, sourceID := range mapping.SourceCharacterIDs {
			if _, formal := allowedSources[sourceID]; !formal {
				continue
			}
			if _, duplicate := claimed[sourceID]; duplicate {
				return CoreCastContract{}, fmt.Errorf("formal source character %q has conflicting mappings", sourceID)
			}
			claimed[sourceID] = struct{}{}
			disposition := SourceCharacterDisposition{
				SourceCharacterID:  sourceID,
				Action:             SourceDispositionAction(mapping.Action),
				TargetCharacterIDs: append([]string(nil), mapping.TargetCharacterIDs...),
				Rationale:          mapping.Rationale,
			}
			out.SourceDispositions = append(out.SourceDispositions, disposition)
			if disposition.Action == SourceDispositionExclude {
				continue
			}
			for _, targetID := range disposition.TargetCharacterIDs {
				index, exists := memberIndexes[targetID]
				if !exists {
					return CoreCastContract{}, fmt.Errorf(
						"formal source character %q maps to non-core target %q",
						sourceID,
						targetID,
					)
				}
				out.Members[index].Origin = CoreCastOriginSource
				out.Members[index].SourceCharacterIDs = append(
					out.Members[index].SourceCharacterIDs,
					sourceID,
				)
			}
		}
	}
	for sourceID := range allowedSources {
		if _, exists := claimed[sourceID]; !exists {
			return CoreCastContract{}, fmt.Errorf("formal source character %q requires a mapping", sourceID)
		}
	}
	return NormalizeCoreCastContract(out)
}

// ValidateCharacterSourceCoverage verifies that every relevant source
// character has one explicit disposition and every target character is either
// mapped from source material or declared target-original.
func ValidateCharacterSourceCoverage(
	mappings []CharacterSourceMapping,
	sourceCharacterIDs, targetCharacterIDs []string,
) error {
	allowedSources := make(map[string]struct{}, len(sourceCharacterIDs))
	for _, sourceID := range normalizedStrings(sourceCharacterIDs) {
		allowedSources[sourceID] = struct{}{}
	}
	allowedTargets := make(map[string]struct{}, len(targetCharacterIDs))
	for _, targetID := range normalizedStrings(targetCharacterIDs) {
		allowedTargets[targetID] = struct{}{}
	}
	sourceClaims := make(map[string]string)
	targetClaims := make(map[string]struct{})
	for _, mapping := range mappings {
		normalized := mapping
		normalizeCharacterSourceMapping(&normalized)
		if err := validateCharacterSourceMapping(normalized); err != nil {
			return err
		}
		for _, sourceID := range normalized.SourceCharacterIDs {
			if _, exists := allowedSources[sourceID]; !exists {
				return fmt.Errorf("mapping %q references unknown source character %q", normalized.ID, sourceID)
			}
			if owner, exists := sourceClaims[sourceID]; exists && owner != normalized.ID {
				return fmt.Errorf("source character %q has conflicting mappings %q and %q", sourceID, owner, normalized.ID)
			}
			sourceClaims[sourceID] = normalized.ID
		}
		for _, targetID := range normalized.TargetCharacterIDs {
			if _, exists := allowedTargets[targetID]; !exists {
				return fmt.Errorf("mapping %q references unknown target character %q", normalized.ID, targetID)
			}
			targetClaims[targetID] = struct{}{}
		}
	}
	for _, sourceID := range normalizedStrings(sourceCharacterIDs) {
		if _, exists := sourceClaims[sourceID]; !exists {
			return fmt.Errorf("source character %q requires an explicit mapping or exclusion", sourceID)
		}
	}
	for _, targetID := range normalizedStrings(targetCharacterIDs) {
		if _, exists := targetClaims[targetID]; !exists {
			return fmt.Errorf("target character %q requires a source mapping or target-original declaration", targetID)
		}
	}
	return nil
}

func normalizeCharacterCardInputs(inputs CharacterCardInputSignatures) CharacterCardInputSignatures {
	inputs.CreativeBrief = strings.TrimSpace(inputs.CreativeBrief)
	inputs.CoreCast = strings.TrimSpace(inputs.CoreCast)
	inputs.SourceFoundation = strings.TrimSpace(inputs.SourceFoundation)
	inputs.AdaptationIntent = strings.TrimSpace(inputs.AdaptationIntent)
	for i := range inputs.Additional {
		inputs.Additional[i].Name = strings.TrimSpace(inputs.Additional[i].Name)
		inputs.Additional[i].Signature = strings.TrimSpace(inputs.Additional[i].Signature)
	}
	sort.Slice(inputs.Additional, func(i, j int) bool {
		return inputs.Additional[i].Name+"\x00"+inputs.Additional[i].Signature <
			inputs.Additional[j].Name+"\x00"+inputs.Additional[j].Signature
	})
	return inputs
}

func validateCharacterCardInputs(inputs CharacterCardInputSignatures) error {
	names := make(map[string]struct{}, len(inputs.Additional))
	for _, input := range inputs.Additional {
		if input.Name == "" || input.Signature == "" {
			return fmt.Errorf("additional character card input name and signature are required")
		}
		if _, exists := names[input.Name]; exists {
			return fmt.Errorf("duplicate character card input %q", input.Name)
		}
		names[input.Name] = struct{}{}
	}
	return nil
}

func normalizeCandidateReference(value *CharacterCardCandidateReference) {
	value.FoundationAuditSignature = strings.TrimSpace(value.FoundationAuditSignature)
	value.CharacterContentDigest = strings.TrimSpace(value.CharacterContentDigest)
}

func normalizeCharacterSourceMapping(mapping *CharacterSourceMapping) {
	mapping.ID = strings.TrimSpace(mapping.ID)
	mapping.SourceCharacterIDs = normalizedStrings(mapping.SourceCharacterIDs)
	mapping.TargetCharacterIDs = normalizedStrings(mapping.TargetCharacterIDs)
	mapping.Rationale = strings.TrimSpace(mapping.Rationale)
	for i := range mapping.Evidence {
		mapping.Evidence[i].Reference = strings.TrimSpace(mapping.Evidence[i].Reference)
		mapping.Evidence[i].Summary = strings.TrimSpace(mapping.Evidence[i].Summary)
	}
	sort.Slice(mapping.Evidence, func(i, j int) bool {
		return string(mapping.Evidence[i].Kind)+"\x00"+mapping.Evidence[i].Reference <
			string(mapping.Evidence[j].Kind)+"\x00"+mapping.Evidence[j].Reference
	})
}

func validateCharacterCardLifecycle(value CharacterCardLifecycle) error {
	if !validCharacterCardAnalysisStatus(value.AnalysisStatus) {
		return fmt.Errorf("character card analysis status %q is invalid", value.AnalysisStatus)
	}
	if !validCharacterCardReviewStatus(value.ReviewStatus) {
		return fmt.Errorf("character card review status %q is invalid", value.ReviewStatus)
	}
	if !validCharacterCardConfirmationStatus(value.ConfirmationStatus) {
		return fmt.Errorf("character card confirmation status %q is invalid", value.ConfirmationStatus)
	}
	if value.AnalysisStatus == CharacterCardAnalysisCandidateReady {
		if err := validateCharacterCardCandidate(value.Candidate); err != nil {
			return err
		}
	}
	if value.ReviewStatus != CharacterCardReviewNotReviewed {
		if err := validateCharacterCardCandidate(value.ReviewedCandidate); err != nil {
			return fmt.Errorf("reviewed character card candidate: %w", err)
		}
		if len(value.ReviewedInputDigest) != 64 {
			return fmt.Errorf("reviewed character card input digest is invalid")
		}
	}
	if value.ReviewStatus == CharacterCardReviewPassed {
		for _, finding := range value.Findings {
			if finding.Blocking {
				return fmt.Errorf("passed character card review contains blocking finding %q", finding.ID)
			}
		}
	}
	if value.ConfirmationStatus == CharacterCardConfirmed && value.ReviewStatus != CharacterCardReviewPassed {
		return fmt.Errorf("character card confirmation requires a passed review")
	}
	if value.Error != nil && (value.Error.Class == "" || value.Error.Message == "") {
		return fmt.Errorf("character card error class and message are required")
	}
	if value.SubmissionDigest != "" && len(value.SubmissionDigest) != 64 {
		return fmt.Errorf("character card submission digest is invalid")
	}
	if value.Coverage != nil {
		if value.Mode != CharacterCardProjectAdaptation {
			return fmt.Errorf("only adaptation character cards may store source coverage")
		}
		if value.Coverage.SourceTotal < 0 || value.Coverage.DecisionRequired < 0 ||
			value.Coverage.Mapped < 0 || value.Coverage.ExplicitlyExcluded < 0 ||
			value.Coverage.Pending < 0 || value.Coverage.BlockingGaps < 0 ||
			value.Coverage.SourceTotal != len(value.Coverage.Decisions) {
			return fmt.Errorf("adaptation character coverage counts are invalid")
		}
	}
	findingIDs := make(map[string]struct{}, len(value.Findings))
	for _, finding := range value.Findings {
		if finding.ID == "" || finding.IssueType == "" || finding.Description == "" ||
			(finding.Scope != CharacterCardFindingGlobal && finding.Scope != CharacterCardFindingCharacter) ||
			(finding.Severity != CharacterCardSeverityWarning && finding.Severity != CharacterCardSeverityBlocking) {
			return fmt.Errorf("character card finding is incomplete")
		}
		if len(finding.EvidenceSummary) > characterCardEvidenceSummaryLimit {
			return fmt.Errorf("character card finding %q evidence exceeds compact metadata limits", finding.ID)
		}
		if finding.Scope == CharacterCardFindingCharacter && finding.CharacterID == "" {
			return fmt.Errorf("character-scoped finding %q requires a character id", finding.ID)
		}
		if _, exists := findingIDs[finding.ID]; exists {
			return fmt.Errorf("duplicate character card finding id %q", finding.ID)
		}
		findingIDs[finding.ID] = struct{}{}
	}
	mappingIDs := make(map[string]struct{}, len(value.SourceMappings))
	sourceMappingClaims := make(map[string]string)
	for _, mapping := range value.SourceMappings {
		if err := validateCharacterSourceMapping(mapping); err != nil {
			return err
		}
		if _, exists := mappingIDs[mapping.ID]; exists {
			return fmt.Errorf("duplicate character source mapping id %q", mapping.ID)
		}
		mappingIDs[mapping.ID] = struct{}{}
		for _, sourceID := range mapping.SourceCharacterIDs {
			if owner, exists := sourceMappingClaims[sourceID]; exists {
				return fmt.Errorf("source character %q has conflicting mappings %q and %q", sourceID, owner, mapping.ID)
			}
			sourceMappingClaims[sourceID] = mapping.ID
		}
	}
	return nil
}

func validateCharacterCardCandidate(value CharacterCardCandidateReference) error {
	if value.FoundationRevision < 0 || len(value.FoundationAuditSignature) != 64 ||
		len(value.CharacterContentDigest) != 64 {
		return fmt.Errorf("character card candidate binding is incomplete")
	}
	return nil
}

func validateCharacterSourceMapping(mapping CharacterSourceMapping) error {
	if mapping.ID == "" || mapping.Rationale == "" {
		return fmt.Errorf("character source mapping id and rationale are required")
	}
	switch mapping.Action {
	case CharacterSourceKeep, CharacterSourceRename:
		if len(mapping.SourceCharacterIDs) != 1 || len(mapping.TargetCharacterIDs) != 1 {
			return fmt.Errorf("%s mapping requires one source and one target", mapping.Action)
		}
	case CharacterSourceMerge:
		if len(mapping.SourceCharacterIDs) == 0 || len(mapping.TargetCharacterIDs) != 1 {
			return fmt.Errorf("merge mapping requires source characters and one target")
		}
	case CharacterSourceSplit:
		if len(mapping.SourceCharacterIDs) != 1 || len(mapping.TargetCharacterIDs) < 2 {
			return fmt.Errorf("split mapping requires one source and multiple targets")
		}
	case CharacterSourceExclude:
		if len(mapping.SourceCharacterIDs) != 1 || len(mapping.TargetCharacterIDs) != 0 {
			return fmt.Errorf("exclude mapping requires one source and no targets")
		}
	case CharacterSourceTargetOriginal:
		if len(mapping.SourceCharacterIDs) != 0 || len(mapping.TargetCharacterIDs) != 1 {
			return fmt.Errorf("target-original mapping requires no source and one target")
		}
	default:
		return fmt.Errorf("character source mapping action %q is invalid", mapping.Action)
	}
	hasDecisionEvidence := false
	hasOriginalAdditionEvidence := false
	for _, evidence := range mapping.Evidence {
		if evidence.Reference == "" {
			return fmt.Errorf("character source mapping %q evidence reference is required", mapping.ID)
		}
		if len(evidence.Reference) > characterCardEvidenceReferenceLimit ||
			len(evidence.Summary) > characterCardEvidenceSummaryLimit {
			return fmt.Errorf("character source mapping %q evidence exceeds compact metadata limits", mapping.ID)
		}
		switch evidence.Kind {
		case CharacterSourceOriginalFact, CharacterSourceAdaptationDecision, CharacterSourceOriginalAddition:
		default:
			return fmt.Errorf("character source mapping %q evidence kind %q is invalid", mapping.ID, evidence.Kind)
		}
		hasDecisionEvidence = hasDecisionEvidence || evidence.Kind == CharacterSourceAdaptationDecision
		hasOriginalAdditionEvidence = hasOriginalAdditionEvidence || evidence.Kind == CharacterSourceOriginalAddition
	}
	if mapping.Action == CharacterSourceTargetOriginal {
		if !hasOriginalAdditionEvidence {
			return fmt.Errorf("target-original mapping %q requires target-original evidence", mapping.ID)
		}
	} else if !hasDecisionEvidence {
		return fmt.Errorf("character source mapping %q requires adaptation-decision evidence", mapping.ID)
	}
	return nil
}

func sameCharacterCardCandidate(left, right CharacterCardCandidateReference) bool {
	return left.FoundationRevision == right.FoundationRevision &&
		left.FoundationAuditSignature == right.FoundationAuditSignature &&
		left.CharacterContentDigest == right.CharacterContentDigest
}

func validCharacterCardAnalysisStatus(value CharacterCardAnalysisStatus) bool {
	switch value {
	case CharacterCardAnalysisNotGenerated, CharacterCardAnalysisCandidateReady,
		CharacterCardAnalysisFailed, CharacterCardAnalysisStale:
		return true
	default:
		return false
	}
}

func validCharacterCardReviewStatus(value CharacterCardReviewStatus) bool {
	switch value {
	case CharacterCardReviewNotReviewed, CharacterCardReviewInProgress, CharacterCardReviewPassed,
		CharacterCardReviewNeedsRevision, CharacterCardReviewFailed, CharacterCardReviewStale:
		return true
	default:
		return false
	}
}

func validCharacterCardConfirmationStatus(value CharacterCardConfirmationStatus) bool {
	return value == CharacterCardUnconfirmed || value == CharacterCardConfirmed ||
		value == CharacterCardConfirmationStale
}

func cloneCharacterCardLifecycle(in CharacterCardLifecycle) CharacterCardLifecycle {
	out := in
	out.Inputs.Additional = append([]CharacterCardNamedSignature(nil), in.Inputs.Additional...)
	out.Completeness = append([]CharacterCardCompletenessResult(nil), in.Completeness...)
	for i := range out.Completeness {
		out.Completeness[i].Missing = append([]CharacterCardMissingItem(nil), in.Completeness[i].Missing...)
	}
	out.Findings = append([]CharacterCardReviewFinding(nil), in.Findings...)
	out.SourceMappings = append([]CharacterSourceMapping(nil), in.SourceMappings...)
	for i := range out.SourceMappings {
		out.SourceMappings[i].SourceCharacterIDs = append([]string(nil), in.SourceMappings[i].SourceCharacterIDs...)
		out.SourceMappings[i].TargetCharacterIDs = append([]string(nil), in.SourceMappings[i].TargetCharacterIDs...)
		out.SourceMappings[i].Evidence = append([]CharacterSourceEvidence(nil), in.SourceMappings[i].Evidence...)
	}
	if in.Error != nil {
		value := *in.Error
		out.Error = &value
	}
	if in.Coverage != nil {
		value := *in.Coverage
		value.Decisions = append([]AdaptationCharacterCoverageDecision(nil), in.Coverage.Decisions...)
		for i := range value.Decisions {
			value.Decisions[i].Reasons = append([]string(nil), in.Coverage.Decisions[i].Reasons...)
		}
		out.Coverage = &value
	}
	return out
}
