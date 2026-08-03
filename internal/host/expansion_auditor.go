package host

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	expansionAuditorPolicyVersion    = "expansion-content-auditor/v2"
	expansionDependencyPolicyVersion = "expansion-dependency-auditor/v2"
)

// ExpansionAuditTask is the durable hand-off from the orchestrating planner to
// an independent auditor. Creating this task never advances a revision.
type ExpansionAuditTask struct {
	ID                        string                             `json:"id"`
	RevisionID                string                             `json:"revision_id"`
	Revision                  int                                `json:"revision"`
	Stage                     domain.RevisionStage               `json:"stage"`
	Scope                     string                             `json:"scope"`
	ScopeID                   string                             `json:"scope_id"`
	CandidateSignature        string                             `json:"candidate_signature"`
	StructureSignature        string                             `json:"structure_signature"`
	Candidate                 []domain.VolumeOutline             `json:"candidate"`
	CandidateVersions         []domain.ArtifactVersion           `json:"candidate_versions"`
	DramaticVersions          []domain.ArtifactVersion           `json:"dramatic_versions,omitempty"`
	ExpectationSet            []domain.RevisionAuditExpectation  `json:"expectation_set"`
	DependencyReviews         []domain.ExpansionDependencyReview `json:"dependency_reviews,omitempty"`
	CheckpointScopes          []ExpansionAuditCheckpoint         `json:"checkpoint_scopes,omitempty"`
	CheckpointTaskIDs         []string                           `json:"checkpoint_task_ids,omitempty"`
	CheckpointArtifacts       []ExpansionDependencyArtifactRef   `json:"checkpoint_artifacts,omitempty"`
	CheckpointReviews         []domain.ExpansionDependencyReview `json:"-"`
	Mode                      domain.RevisionMode                `json:"mode"`
	Impact                    domain.RevisionImpact              `json:"impact"`
	DramaticAssessment        domain.ExpansionDramaticAssessment `json:"dramatic_assessment"`
	DramaticContract          ExpansionDramaticContract          `json:"dramatic_contract"`
	ExpansionForm             domain.ExpansionForm               `json:"expansion_form"`
	AdaptationCandidate       *domain.AdaptationPlan             `json:"adaptation_candidate,omitempty"`
	AdaptationSourceSignature string                             `json:"adaptation_source_signature,omitempty"`
	PolicyVersion             string                             `json:"policy_version"`
	Status                    string                             `json:"status"`
	Findings                  []string                           `json:"findings,omitempty"`
	CreatedAt                 time.Time                          `json:"created_at"`
	CompletedAt               *time.Time                         `json:"completed_at,omitempty"`
}

// ExpansionDramaticContract binds each narrative assertion to stable candidate
// and impact identities so the auditor executes relationships, not labels.
type ExpansionDramaticContract struct {
	Claims                   []ExpansionDramaticClaimBinding `json:"claims"`
	GoalChapterID            string                          `json:"goal_chapter_id"`
	ConflictChapterID        string                          `json:"conflict_chapter_id"`
	ChoiceChapterID          string                          `json:"choice_chapter_id"`
	CostChapterID            string                          `json:"cost_chapter_id"`
	ResultChapterID          string                          `json:"result_chapter_id"`
	CharacterBefore          string                          `json:"character_before"`
	CharacterAfter           string                          `json:"character_after"`
	ClimaxTrigger            string                          `json:"climax_trigger"`
	ClimaxChapterID          string                          `json:"climax_chapter_id"`
	IrreversibleExit         string                          `json:"irreversible_exit"`
	ExitChapterID            string                          `json:"exit_chapter_id"`
	RequiredDependencyIDs    []string                        `json:"required_dependency_ids,omitempty"`
	RecommendedDependencyIDs []string                        `json:"recommended_dependency_ids,omitempty"`
}

// ExpansionDramaticClaimBinding is a deterministic typed edge into an
// authoritative candidate artifact. FieldSignature signs canonical
// kind/path/type/value; CandidateContentSignature binds the complete version.
type ExpansionDramaticClaimBinding struct {
	Kind                      string `json:"kind"`
	ArtifactVersionID         string `json:"artifact_version_id"`
	ArtifactID                string `json:"artifact_id"`
	ChapterID                 string `json:"chapter_id"`
	FieldPath                 string `json:"field_path"`
	FieldType                 string `json:"field_type"`
	Value                     string `json:"value"`
	ClaimValue                string `json:"claim_value"`
	ClaimState                string `json:"claim_state,omitempty"`
	CandidateState            string `json:"candidate_state,omitempty"`
	FieldSignature            string `json:"field_signature"`
	CandidateContentSignature string `json:"candidate_content_signature"`
	Sequence                  int    `json:"sequence"`
}

type ExpansionAuditCheckpoint struct {
	Stage         string                             `json:"stage"`
	ScopeID       string                             `json:"scope_id"`
	Signature     string                             `json:"signature"`
	Output        string                             `json:"output"`
	DependencyIDs []string                           `json:"dependency_ids,omitempty"`
	ChildReviews  []domain.ExpansionDependencyReview `json:"child_reviews,omitempty"`
}

// ExpansionDependencyArtifactRef is the immutable edge stored by a parent
// audit node.  A parent never trusts an embedded summary: the runner reloads
// the referenced signed artifact and verifies every binding before review.
type ExpansionDependencyArtifactRef struct {
	ArtifactID        string `json:"artifact_id"`
	Version           int    `json:"version"`
	Stage             string `json:"stage"`
	ScopeID           string `json:"scope_id"`
	InputSignature    string `json:"input_signature"`
	OutputSignature   string `json:"output_signature"`
	Decision          string `json:"decision"`
	ArtifactSignature string `json:"artifact_signature"`
}

type ExpansionDependencyAuditTask struct {
	ID                string                             `json:"id"`
	RootAuditTaskID   string                             `json:"root_audit_task_id"`
	Stage             string                             `json:"stage"`
	ScopeID           string                             `json:"scope_id"`
	InputSignature    string                             `json:"input_signature"`
	Output            string                             `json:"output"`
	DependencyIDs     []string                           `json:"dependency_ids,omitempty"`
	ChildTaskIDs      []string                           `json:"child_task_ids,omitempty"`
	ChildReviews      []domain.ExpansionDependencyReview `json:"child_reviews,omitempty"`
	ChildArtifacts    []ExpansionDependencyArtifactRef   `json:"child_artifacts,omitempty"`
	ContractSignature string                             `json:"contract_signature"`
	PolicyVersion     string                             `json:"policy_version"`
	Status            string                             `json:"status"`
	CreatedAt         time.Time                          `json:"created_at"`
	ArtifactID        string                             `json:"artifact_id,omitempty"`
}

// ExpansionAuditArtifact is accepted only when it is signed by the configured
// auditor identity, not by the planner seal or by a browser-provided key.
type ExpansionAuditArtifact struct {
	TaskID             string                             `json:"task_id"`
	RevisionID         string                             `json:"revision_id"`
	Revision           int                                `json:"revision"`
	Stage              domain.RevisionStage               `json:"stage"`
	Scope              string                             `json:"scope"`
	ScopeID            string                             `json:"scope_id"`
	CandidateSignature string                             `json:"candidate_signature"`
	StructureSignature string                             `json:"structure_signature"`
	CandidateVersions  []domain.ArtifactVersion           `json:"candidate_versions"`
	ExpectationSet     []domain.RevisionAuditExpectation  `json:"expectation_set"`
	Evidence           []domain.RevisionAuditEvidence     `json:"evidence"`
	DependencyReviews  []domain.ExpansionDependencyReview `json:"dependency_reviews,omitempty"`
	CheckpointReviews  []domain.ExpansionDependencyReview `json:"checkpoint_reviews,omitempty"`
	PolicyVersion      string                             `json:"policy_version"`
	ModelVersion       string                             `json:"model_version"`
	ReviewerIdentity   string                             `json:"reviewer_identity"`
	ReviewerPublicKey  string                             `json:"reviewer_public_key"`
	Decision           string                             `json:"decision"`
	Findings           []string                           `json:"findings,omitempty"`
	Signature          string                             `json:"signature"`
}

// ExpansionAuditAuthority is implemented only by the independently composed
// auditor process. Product host code can evaluate tasks and verify signed
// artifacts, but it has no private-key or signing entry point.
type ExpansionAuditAuthority interface {
	ReviewerIdentity() string
	ReviewerPublicKey() ed25519.PublicKey
	ReviewDependency(context.Context, ExpansionDependencyAuditTask) (domain.ExpansionDependencyReview, error)
	LoadAdaptationContract(context.Context) (*domain.AdaptationPlan, *domain.AdaptationSourceManifest, error)
}

// EvaluateExpansionRevision performs the deterministic semantic review. The
// returned top-level artifact is deliberately unsigned; only the independent
// auditor executable may seal it before returning it over IPC.
func EvaluateExpansionRevision(ctx context.Context, task ExpansionAuditTask, authority ExpansionAuditAuthority) (ExpansionAuditArtifact, error) {
	if authority == nil || len(authority.ReviewerPublicKey()) != ed25519.PublicKeySize {
		return ExpansionAuditArtifact{}, fmt.Errorf("independent expansion auditor is unavailable")
	}
	trustedKey := authority.ReviewerPublicKey()
	artifact := ExpansionAuditArtifact{
		TaskID: task.ID, RevisionID: task.RevisionID, Revision: task.Revision, Stage: task.Stage,
		Scope: task.Scope, ScopeID: task.ScopeID, CandidateSignature: task.CandidateSignature, StructureSignature: task.StructureSignature,
		ExpectationSet:    append([]domain.RevisionAuditExpectation(nil), task.ExpectationSet...),
		CandidateVersions: append([]domain.ArtifactVersion(nil), task.CandidateVersions...),
		DependencyReviews: append([]domain.ExpansionDependencyReview(nil), task.DependencyReviews...),
		PolicyVersion:     expansionAuditorPolicyVersion, ModelVersion: "deterministic-structure-policy/v2",
		ReviewerIdentity: authority.ReviewerIdentity(), ReviewerPublicKey: encodeExpansionPublicKey(trustedKey),
	}
	if task.PolicyVersion != expansionAuditorPolicyVersion {
		artifact.Findings = append(artifact.Findings, "audit task policy version is not supported")
	}
	if task.Stage != domain.RevisionStageCandidateAudit || task.Scope != "revision_candidate" || task.ScopeID != task.RevisionID {
		artifact.Findings = append(artifact.Findings, "audit task revision stage or scope is invalid")
	}
	if len(task.Candidate) == 0 || domain.StructureSignature(task.Candidate) != task.StructureSignature {
		artifact.Findings = append(artifact.Findings, "candidate content does not match its signed structure")
	} else if err := domain.ValidateStructureSnapshot(task.Candidate); err != nil {
		artifact.Findings = append(artifact.Findings, "candidate structure policy: "+err.Error())
	}
	if len(task.ExpectationSet) == 0 {
		artifact.Findings = append(artifact.Findings, "audit expectation set is empty")
	}
	if len(task.CandidateVersions) == 0 || domain.CandidateSignature(task.CandidateVersions) != task.CandidateSignature {
		artifact.Findings = append(artifact.Findings, "candidate version set does not match the revision signature")
	}
	for _, version := range task.CandidateVersions {
		if err := version.Validate(); err != nil {
			artifact.Findings = append(artifact.Findings, "candidate artifact "+version.ID+": "+err.Error())
		}
		if version.RevisionID != task.RevisionID {
			artifact.Findings = append(artifact.Findings, "candidate artifact "+version.ID+" belongs to another revision")
		}
	}
	semanticFindings := auditExpansionCandidateSemantics(task)
	artifact.Findings = append(artifact.Findings, semanticFindings...)
	if task.Mode == domain.RevisionModeAdaptation {
		base, manifest, loadErr := authority.LoadAdaptationContract(ctx)
		if loadErr != nil || base == nil || manifest == nil {
			artifact.Findings = append(artifact.Findings, "independent auditor could not load authoritative adaptation plan and source manifest: "+fmt.Sprint(loadErr))
		} else {
			candidate := task.AdaptationCandidate
			var snapshot domain.AdaptationPlanRevisionCandidate
			if decodeExpansionArtifactKind(task.CandidateVersions, domain.AdaptationRevisionArtifactPlanSnapshot, &snapshot) {
				candidate = &snapshot.Plan
			}
			if candidate == nil {
				artifact.Findings = append(artifact.Findings, "adaptation candidate lacks its contract-aware plan")
			} else if task.AdaptationSourceSignature != domain.AdaptationSourceManifestContractSignature(*manifest) {
				artifact.Findings = append(artifact.Findings, "adaptation candidate source manifest signature is stale")
			} else if contractErr := domain.ValidateAdaptationRevisionPlan(*base, *candidate, manifest); contractErr != nil {
				artifact.Findings = append(artifact.Findings, "adaptation coverage/protected contract: "+contractErr.Error())
			}
		}
	}
	for _, expected := range task.ExpectationSet {
		scopeFindings, actualSignature, rules := auditExpansionExpectation(task, expected)
		finding := strings.Join(scopeFindings, "; ")
		if err := expected.Validate(); err != nil {
			finding = err.Error()
			artifact.Findings = append(artifact.Findings, finding)
		}
		artifact.Findings = append(artifact.Findings, scopeFindings...)
		report := strings.Join(rules, "; ")
		if finding != "" {
			report = finding
		}
		artifact.Evidence = append(artifact.Evidence, domain.RevisionAuditEvidence{
			Scope: expected.Scope, ScopeID: expected.ScopeID, FromChapter: expected.FromChapter,
			ToChapter: expected.ToChapter, ContentSignature: actualSignature,
			Passed: finding == "", Report: report,
		})
	}
	for _, review := range task.DependencyReviews {
		if err := verifyExpansionDependencyReview(review, trustedKey); err != nil {
			artifact.Findings = append(artifact.Findings, err.Error())
		} else if review.Decision != "pass" {
			artifact.Findings = append(artifact.Findings, "dependency review needs fix: "+strings.Join(review.Findings, "; "))
		}
	}
	if len(task.CheckpointTaskIDs) != len(task.CheckpointArtifacts) {
		artifact.Findings = append(artifact.Findings, "durable checkpoint artifact graph is incomplete")
	}
	checkpointBySignature := make(map[string]domain.ExpansionDependencyReview, len(task.CheckpointReviews))
	for _, review := range task.CheckpointReviews {
		checkpointBySignature[review.ArtifactSignature] = review
		artifact.CheckpointReviews = append(artifact.CheckpointReviews, review)
		if err := verifyExpansionDependencyReview(review, trustedKey); err != nil {
			artifact.Findings = append(artifact.Findings, "durable checkpoint artifact is invalid: "+err.Error())
		} else if review.Decision != "pass" {
			artifact.Findings = append(artifact.Findings, "checkpoint "+review.Stage+"/"+review.ScopeID+" needs fix: "+strings.Join(review.Findings, "; "))
		}
	}
	for _, ref := range task.CheckpointArtifacts {
		review, ok := checkpointBySignature[ref.ArtifactID]
		if !ok || ref.ArtifactSignature != review.ArtifactSignature || ref.Stage != review.Stage || ref.ScopeID != review.ScopeID || ref.InputSignature != review.InputSignature || ref.OutputSignature != review.OutputSignature || ref.Decision != review.Decision {
			artifact.Findings = append(artifact.Findings, "durable checkpoint artifact binding mismatch for "+ref.ScopeID)
			continue
		}
	}
	artifact.Decision = "pass"
	if len(artifact.Findings) > 0 {
		artifact.Decision = "needs_fix"
		for index := range artifact.Evidence {
			artifact.Evidence[index].Passed = false
			artifact.Evidence[index].Report = strings.Join(artifact.Findings, "; ")
		}
	}
	return artifact, nil
}

// EvaluateExpansionDependency evaluates one durable dependency node without
// signing it. The private auditor signs the returned review after evaluation.
func EvaluateExpansionDependency(task ExpansionDependencyAuditTask, reviewerIdentity, reviewerPublicKey string, trustedKey ed25519.PublicKey) (domain.ExpansionDependencyReview, error) {
	if strings.TrimSpace(reviewerIdentity) == "" || len(trustedKey) != ed25519.PublicKeySize {
		return domain.ExpansionDependencyReview{}, fmt.Errorf("independent dependency auditor is unavailable")
	}
	findings := make([]string, 0)
	if len(task.ChildArtifacts) == 0 && len(task.ChildReviews) > 0 {
		task.ChildArtifacts = expansionDependencyRefs(task.ChildReviews)
	}
	if strings.TrimSpace(task.Stage) == "" || strings.TrimSpace(task.ScopeID) == "" {
		findings = append(findings, "missing stable dependency scope")
	}
	isCheckpointTask := strings.Contains(task.ID, ":checkpoint:")
	if isCheckpointTask && strings.TrimSpace(task.RootAuditTaskID) == "" {
		findings = append(findings, "dependency task is not bound to an immutable root audit identity")
	}
	if isCheckpointTask && len(task.ChildTaskIDs) != len(task.DependencyIDs) {
		findings = append(findings, "dependency child task identity bindings are incomplete")
	}
	if strings.TrimSpace(task.InputSignature) == "" {
		findings = append(findings, "missing dependency input signature")
	}
	if strings.TrimSpace(task.Output) == "" {
		findings = append(findings, "reviewed dependency output is empty")
	}
	if task.PolicyVersion != expansionDependencyPolicyVersion {
		findings = append(findings, "dependency audit policy version is not supported")
	}
	if task.ContractSignature == "" {
		// Direct runner callers receive the same canonical contract that the
		// durable queue writes. Persisted production tasks always carry it.
		task.ContractSignature = expansionDependencyContractSignature(task)
	}
	if task.ContractSignature != expansionDependencyContractSignature(task) {
		findings = append(findings, "dependency scope contract signature is missing or stale")
	}
	findings = append(findings, auditDependencySemantics(task)...)
	childByID := make(map[string]domain.ExpansionDependencyReview, len(task.ChildReviews))
	for _, child := range task.ChildReviews {
		if err := verifyExpansionDependencyReview(child, trustedKey); err != nil {
			findings = append(findings, "child dependency artifact is invalid: "+err.Error())
			continue
		}
		childByID[child.ScopeID] = child
		if child.Decision != "pass" {
			findings = append(findings, "child dependency "+child.ScopeID+" did not pass")
		}
	}
	if len(task.ChildArtifacts) != len(task.ChildReviews) {
		findings = append(findings, "dependency child artifact graph is incomplete")
	}
	for _, ref := range task.ChildArtifacts {
		child, ok := childByID[ref.ScopeID]
		if !ok || ref.Version != 1 || ref.ArtifactID != child.ArtifactSignature || ref.Stage != child.Stage ||
			ref.InputSignature != child.InputSignature || ref.OutputSignature != child.OutputSignature ||
			ref.Decision != child.Decision || ref.ArtifactSignature != child.ArtifactSignature {
			findings = append(findings, "signed child artifact binding mismatch for "+ref.ScopeID)
		}
	}
	if task.Stage != "local" && strings.HasPrefix(task.Stage, "adaptation_") == false {
		for _, dependencyID := range task.DependencyIDs {
			if _, ok := childByID[dependencyID]; !ok {
				findings = append(findings, "missing signed child dependency artifact "+dependencyID)
			}
		}
	}
	if strings.HasPrefix(task.Stage, "adaptation_") && task.Stage != "adaptation_chapter" {
		for _, dependencyID := range task.DependencyIDs {
			if _, ok := childByID[dependencyID]; !ok {
				findings = append(findings, "missing signed adaptation child artifact "+dependencyID)
			}
		}
	}
	decision := "pass"
	if len(findings) > 0 {
		decision = "needs_fix"
	}
	review := domain.ExpansionDependencyReview{
		Stage: task.Stage, ScopeID: task.ScopeID, InputSignature: task.InputSignature,
		OutputSignature: domain.JSONContentSignature([]byte(task.Output)), PolicyVersion: expansionDependencyPolicyVersion,
		ReviewerIdentity: reviewerIdentity + "/dependency", ReviewerPublicKey: reviewerPublicKey,
		Decision: decision, Findings: findings, DependencyIDs: append([]string(nil), task.DependencyIDs...),
	}
	return review, nil
}

func auditExpansionCandidateSemantics(task ExpansionAuditTask) []string {
	var findings []string
	if err := task.DramaticAssessment.Validate(task.Mode); err != nil {
		findings = append(findings, "structured dramatic contract: "+err.Error())
	}
	if (task.ExpansionForm == domain.ExpansionFormNewVolume) && (strings.TrimSpace(task.DramaticAssessment.IndependentClimax) == "" || strings.TrimSpace(task.DramaticAssessment.IrreversibleExit) == "") {
		findings = append(findings, "structured dramatic contract requires an independent climax and irreversible exit for a new volume")
	}
	findings = append(findings, auditExpansionDramaticContract(task)...)
	chapters := domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(task.Candidate))
	for index, chapter := range chapters {
		if chapter.Chapter != index+1 {
			findings = append(findings, fmt.Sprintf("chapter display order is discontinuous at %s", chapter.ID))
		}
		if strings.TrimSpace(chapter.Title) == "" || strings.TrimSpace(chapter.CoreEvent) == "" || strings.TrimSpace(chapter.Hook) == "" || len(chapter.Scenes) == 0 {
			findings = append(findings, fmt.Sprintf("chapter %s lacks title, dramatic event, hook, or scenes", chapter.ID))
		}
		core, hook := strings.ToLower(chapter.CoreEvent), strings.ToLower(chapter.Hook)
		if (strings.Contains(core, "dies") || strings.Contains(core, "死亡") || strings.Contains(core, "牺牲")) && (strings.Contains(hook, "continues") || strings.Contains(hook, "继续行动") || strings.Contains(hook, "安然出现")) {
			findings = append(findings, fmt.Sprintf("chapter %s has a terminal-outcome/hook continuity contradiction", chapter.ID))
		}
		findings = append(findings, auditTypedNarrativeContradictions(chapter)...)
	}
	for _, version := range task.CandidateVersions {
		text := strings.ToLower(string(version.Payload))
		if strings.Contains(text, "audit_fail") || strings.Contains(text, "[needs_fix]") || strings.Contains(text, "placeholder") || strings.Contains(text, "待补") {
			findings = append(findings, fmt.Sprintf("candidate artifact %s contains unresolved audit marker", version.ID))
		}
	}
	if err := task.Impact.Validate(); err != nil {
		findings = append(findings, "revision impact contract: "+err.Error())
	} else {
		for _, item := range task.Impact.Items {
			if (item.Requirement == domain.StructureImpactRequired || item.Requirement == domain.StructureImpactRecommended) && len(item.DependencyEvidence) == 0 {
				findings = append(findings, "impact "+item.ArtifactID+" lacks causal dependency evidence")
			}
			if task.Mode == domain.RevisionModeNormal && len(item.DependencySourceIDs) > 0 {
				findings = append(findings, "normal revision impact carries forbidden adaptation source dependencies")
			}
		}
	}
	return uniqueExpansionFindings(findings)
}

func auditTypedNarrativeContradictions(chapter domain.OutlineEntry) []string {
	fields := strings.ToLower(strings.Join(append([]string{chapter.CoreEvent, chapter.Hook}, chapter.Scenes...), "\n"))
	pairs := []struct{ left, right, label string }{
		{"rescue fails", "rescue succeeds", "goal/result outcome"},
		{"remains passive", "becomes active", "character state"},
		{"climax does not occur", "climax occurs", "climax state"},
		{"permanently leaves", "returns immediately", "irreversible exit"},
	}
	findings := make([]string, 0)
	for _, pair := range pairs {
		if strings.Contains(fields, pair.left) && strings.Contains(fields, pair.right) {
			findings = append(findings, fmt.Sprintf("chapter %s has contradictory typed %s facts", chapter.ID, pair.label))
		}
	}
	return findings
}

func auditExpansionDramaticContract(task ExpansionAuditTask) []string {
	contract := task.DramaticContract
	chapters := domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(task.Candidate))
	position := make(map[string]int, len(chapters))
	for index, chapter := range chapters {
		position[chapter.ID] = index
	}
	causalIDs := []string{contract.GoalChapterID, contract.ConflictChapterID, contract.ChoiceChapterID, contract.CostChapterID, contract.ResultChapterID}
	causalNames := []string{"goal", "conflict", "choice", "cost", "result"}
	findings := make([]string, 0)
	dramaticVersions := task.DramaticVersions
	if len(dramaticVersions) == 0 {
		dramaticVersions = task.CandidateVersions
	}
	for _, version := range dramaticVersions {
		if err := version.Validate(); err != nil || version.RevisionID != task.RevisionID {
			findings = append(findings, "dramatic evidence version is invalid or belongs to another revision: "+version.ID)
		}
	}
	bindings := authoritativeExpansionClaimBindings(dramaticVersions, task.DramaticAssessment, task.Impact)
	byKind := make(map[string]ExpansionDramaticClaimBinding, len(bindings))
	for _, binding := range bindings {
		byKind[binding.Kind] = binding
	}
	provided := make(map[string]ExpansionDramaticClaimBinding, len(contract.Claims))
	for _, binding := range contract.Claims {
		provided[binding.Kind] = binding
		expected, ok := byKind[binding.Kind]
		if !ok || binding != expected {
			findings = append(findings, "dramatic "+binding.Kind+" is not exactly bound to its authoritative candidate field")
		}
		if binding.ClaimState == "" || binding.CandidateState == "" {
			findings = append(findings, "dramatic "+binding.Kind+" lacks an executable typed state")
		} else if binding.ClaimState != binding.CandidateState {
			findings = append(findings, "dramatic "+binding.Kind+" claim contradicts its authoritative typed candidate fact")
		}
	}
	for _, kind := range expansionDramaticClaimKinds {
		if _, ok := provided[kind]; !ok {
			findings = append(findings, "dramatic "+kind+" lacks an authoritative typed candidate binding")
		}
	}
	previous := -1
	for index, id := range causalIDs {
		current, ok := position[id]
		if !ok {
			findings = append(findings, "dramatic "+causalNames[index]+" is not bound to a candidate chapter")
			continue
		}
		if current < previous {
			findings = append(findings, "goal-conflict-choice-cost-result causal order is inverted")
		}
		previous = current
	}
	if strings.TrimSpace(contract.CharacterBefore) == "" || strings.TrimSpace(contract.CharacterAfter) == "" || strings.EqualFold(strings.TrimSpace(contract.CharacterBefore), strings.TrimSpace(contract.CharacterAfter)) {
		findings = append(findings, "character before/after stage transition is missing or unchanged")
	}
	if strings.TrimSpace(contract.ClimaxTrigger) == "" {
		findings = append(findings, "independent climax trigger is missing")
	}
	if _, ok := position[contract.ClimaxChapterID]; !ok {
		findings = append(findings, "independent climax is not bound to a candidate chapter")
	}
	if strings.TrimSpace(contract.IrreversibleExit) == "" {
		findings = append(findings, "irreversible exit is missing")
	}
	if _, ok := position[contract.ExitChapterID]; !ok {
		findings = append(findings, "irreversible exit is not bound to a candidate chapter")
	}
	required, recommended := make([]string, 0), make([]string, 0)
	for _, item := range task.Impact.Items {
		switch item.Requirement {
		case domain.StructureImpactRequired:
			required = append(required, item.ArtifactID)
		case domain.StructureImpactRecommended:
			recommended = append(recommended, item.ArtifactID)
		}
	}
	slices.Sort(required)
	slices.Sort(recommended)
	boundRequired := append([]string(nil), contract.RequiredDependencyIDs...)
	boundRecommended := append([]string(nil), contract.RecommendedDependencyIDs...)
	slices.Sort(boundRequired)
	slices.Sort(boundRecommended)
	if !slices.Equal(required, boundRequired) || !slices.Equal(recommended, boundRecommended) {
		findings = append(findings, "required/recommended impact dependency bindings are incomplete or stale")
	}
	return uniqueExpansionFindings(findings)
}

var expansionDramaticClaimKinds = []string{"goal", "conflict", "choice", "cost", "result", "character_before", "character_after", "climax", "irreversible_exit", "impact"}

type expansionAuthoritativeChapter struct {
	version domain.ArtifactVersion
	outline domain.OutlineEntry
	path    string
}

func authoritativeExpansionClaimBindings(versions []domain.ArtifactVersion, assessment domain.ExpansionDramaticAssessment, impact domain.RevisionImpact) []ExpansionDramaticClaimBinding {
	chapters := make([]expansionAuthoritativeChapter, 0)
	for _, version := range versions {
		if version.ArtifactKind != domain.StructureKindChapter || version.ContentSignature != domain.JSONContentSignature(version.Payload) {
			continue
		}
		var outline domain.OutlineEntry
		var normal domain.NormalDetailedOutlineCandidate
		if json.Unmarshal(version.Payload, &normal) == nil && normal.ChapterID != "" {
			outline = normal.Outline
		} else {
			var adaptation domain.AdaptationDetailedOutlineCandidate
			if json.Unmarshal(version.Payload, &adaptation) != nil || adaptation.ChapterID == "" {
				continue
			}
			outline = adaptation.Outline.OutlineEntry
			outline.Chapter, outline.Title = adaptation.Outline.Chapter, adaptation.Outline.Title
		}
		if outline.ID == "" {
			outline.ID = version.ArtifactID
		}
		chapters = append(chapters, expansionAuthoritativeChapter{version: version, outline: outline, path: "outline"})
	}
	if len(chapters) == 0 {
		for _, version := range versions {
			var structure []domain.VolumeOutline
			switch version.ArtifactKind {
			case domain.NormalArtifactStructureSnapshot:
				_ = json.Unmarshal(version.Payload, &structure)
			case domain.AdaptationRevisionArtifactPlanSnapshot:
				var snapshot domain.AdaptationPlanRevisionCandidate
				if json.Unmarshal(version.Payload, &snapshot) == nil {
					structure = adaptationPlanStructureForDramaticBindings(snapshot.Plan)
				}
			}
			for _, outline := range domain.FlattenOutline(domain.ProjectLayeredOutlineOrder(structure)) {
				chapters = append(chapters, expansionAuthoritativeChapter{version: version, outline: outline, path: "structure.chapters[" + outline.ID + "]"})
			}
		}
	}
	requiredChapterIDs := make(map[string]struct{})
	for _, item := range impact.Items {
		if item.ArtifactKind == domain.StructureKindChapter && item.Requirement == domain.StructureImpactRequired {
			requiredChapterIDs[item.ArtifactID] = struct{}{}
		}
	}
	if len(requiredChapterIDs) > 0 {
		affected := make([]expansionAuthoritativeChapter, 0, len(requiredChapterIDs))
		for _, chapter := range chapters {
			if _, ok := requiredChapterIDs[chapter.outline.ID]; ok {
				affected = append(affected, chapter)
			}
		}
		if len(affected) > 0 {
			chapters = affected
		}
	}
	slices.SortFunc(chapters, func(left, right expansionAuthoritativeChapter) int {
		return left.outline.Chapter - right.outline.Chapter
	})
	if len(chapters) == 0 {
		return nil
	}
	first, last := chapters[0], chapters[len(chapters)-1]
	firstScene, lastScene := "", ""
	if len(first.outline.Scenes) > 0 {
		firstScene = first.outline.Scenes[0]
	}
	if len(last.outline.Scenes) > 0 {
		lastScene = last.outline.Scenes[len(last.outline.Scenes)-1]
	}
	specs := []struct {
		kind, path, value, candidateState string
		chapter                           expansionAuthoritativeChapter
	}{
		{"goal", first.path + ".dramatic_facts.goal_state", first.outline.CoreEvent, expansionCandidateDramaticState(first.outline, "goal"), first},
		{"conflict", first.path + ".dramatic_facts.conflict_state", first.outline.Hook, expansionCandidateDramaticState(first.outline, "conflict"), first},
		{"choice", first.path + ".dramatic_facts.choice_state", firstScene, expansionCandidateDramaticState(first.outline, "choice"), first},
		{"cost", last.path + ".dramatic_facts.cost_state", last.outline.CoreEvent, expansionCandidateDramaticState(last.outline, "cost"), last},
		{"result", last.path + ".dramatic_facts.result_state", last.outline.Hook, expansionCandidateDramaticState(last.outline, "result"), last},
		{"character_before", first.path + ".dramatic_facts.character_before", first.outline.CoreEvent, expansionCandidateDramaticState(first.outline, "character_before"), first},
		{"character_after", last.path + ".dramatic_facts.character_after", last.outline.Hook, expansionCandidateDramaticState(last.outline, "character_after"), last},
		{"climax", last.path + ".dramatic_facts.climax_state", lastScene, expansionCandidateDramaticState(last.outline, "climax"), last},
		{"irreversible_exit", last.path + ".dramatic_facts.exit_state", last.outline.Hook, expansionCandidateDramaticState(last.outline, "irreversible_exit"), last},
		{"impact", last.path + ".dramatic_facts.impact_state", last.outline.ID, expansionCandidateDramaticState(last.outline, "impact"), last},
	}
	result := make([]ExpansionDramaticClaimBinding, 0, len(specs))
	for sequence, spec := range specs {
		claimValue := expansionDramaticAssessmentClaim(assessment, spec.kind)
		claimState := expansionAssessmentDramaticState(assessment.TypedClaims, spec.kind)
		candidateState := spec.candidateState
		canonical, _ := json.Marshal(struct{ Kind, Path, Type, Value, Claim, ClaimState, CandidateState string }{spec.kind, spec.path, "enum", spec.value, claimValue, claimState, candidateState})
		result = append(result, ExpansionDramaticClaimBinding{
			Kind: spec.kind, ArtifactVersionID: spec.chapter.version.ID, ArtifactID: spec.chapter.version.ArtifactID,
			ChapterID: spec.chapter.outline.ID, FieldPath: spec.path, FieldType: "enum", Value: spec.value, ClaimValue: claimValue, ClaimState: claimState, CandidateState: candidateState,
			FieldSignature: domain.JSONContentSignature(canonical), CandidateContentSignature: spec.chapter.version.ContentSignature, Sequence: sequence + 1,
		})
	}
	return result
}

func expansionCandidateDramaticState(chapter domain.OutlineEntry, kind string) string {
	if chapter.DramaticFacts == nil || chapter.DramaticFacts.Validate() != nil {
		return ""
	}
	return expansionDramaticFactState(*chapter.DramaticFacts, kind)
}

func expansionAssessmentDramaticState(claims *domain.ExpansionDramaticFactSet, kind string) string {
	if claims == nil || claims.Validate() != nil {
		return ""
	}
	return expansionDramaticFactState(*claims, kind)
}

func expansionDramaticFactState(facts domain.ExpansionDramaticFactSet, kind string) string {
	switch kind {
	case "goal":
		return facts.GoalState
	case "conflict":
		return facts.ConflictState
	case "choice":
		return facts.ChoiceState
	case "cost":
		return facts.CostState
	case "result":
		return facts.ResultState
	case "character_before":
		return facts.CharacterBefore
	case "character_after":
		return facts.CharacterAfter
	case "climax":
		return facts.ClimaxState
	case "irreversible_exit":
		return facts.ExitState
	case "impact":
		return facts.ImpactState
	default:
		return ""
	}
}

func expansionDramaticAssessmentClaim(assessment domain.ExpansionDramaticAssessment, kind string) string {
	switch kind {
	case "goal":
		return assessment.Goal
	case "conflict":
		return assessment.Conflict
	case "choice":
		return assessment.Choice
	case "cost":
		return assessment.Cost
	case "result":
		return assessment.Result
	case "character_before":
		return assessment.CharacterBeforeStage
	case "character_after":
		return assessment.CharacterAfterStage
	case "climax":
		return assessment.IndependentClimax
	case "irreversible_exit":
		return assessment.IrreversibleExit
	case "impact":
		return assessment.VolumePacingEffect
	default:
		return ""
	}
}

func adaptationPlanStructureForDramaticBindings(plan domain.AdaptationPlan) []domain.VolumeOutline {
	byVolume := make(map[string][]domain.OutlineEntry)
	for _, chapter := range plan.Chapters {
		outline := chapter.OutlineEntry
		outline.Chapter, outline.Title = chapter.Chapter, chapter.Title
		for _, volume := range plan.Volumes {
			if chapter.Chapter >= volume.TargetFrom && chapter.Chapter <= volume.TargetTo {
				byVolume[volume.ID] = append(byVolume[volume.ID], outline)
				break
			}
		}
	}
	result := make([]domain.VolumeOutline, 0, len(plan.Volumes))
	for _, volume := range plan.Volumes {
		result = append(result, domain.VolumeOutline{ID: volume.ID, Index: volume.Index, Title: volume.Title, Theme: volume.Theme, Arcs: []domain.ArcOutline{{ID: volume.ID + ":dramatic-bindings", Index: 1, Chapters: byVolume[volume.ID]}}})
	}
	return result
}

func auditExpansionExpectation(task ExpansionAuditTask, expected domain.RevisionAuditExpectation) ([]string, string, []string) {
	actual, ok := expansionExpectationSignature(task, expected)
	if !ok {
		return []string{"scope " + expected.Scope + "/" + expected.ScopeID + " has no bound candidate payload"}, "", nil
	}
	if actual != expected.ContentSignature {
		return []string{"scope " + expected.Scope + "/" + expected.ScopeID + " content signature does not match its bound payload (expected " + expected.ContentSignature + ", actual " + actual + ")"}, actual, nil
	}
	findings, rules := auditExpansionScopeSemantics(task, expected)
	return findings, actual, rules
}

func auditExpansionScopeSemantics(task ExpansionAuditTask, expected domain.RevisionAuditExpectation) ([]string, []string) {
	versions := expansionVersionsForExpectation(task, expected)
	if len(versions) == 0 {
		return []string{"scope semantic audit could not load authoritative candidate versions"}, nil
	}
	findings := make([]string, 0)
	rules := []string{"payload-signature-bound", "stable-scope-bound"}
	impactByID := make(map[string]domain.RevisionImpactItem, len(task.Impact.Items))
	for _, item := range task.Impact.Items {
		impactByID[item.ArtifactID] = item
	}
	for _, version := range versions {
		var generic any
		if json.Unmarshal(version.Payload, &generic) != nil {
			findings = append(findings, "candidate payload is not valid JSON for "+version.ArtifactID)
			continue
		}
		if task.Mode == domain.RevisionModeNormal {
			if path := forbiddenExpansionModePath(generic, "$"); path != "" {
				findings = append(findings, "normal scope contains forbidden source/adaptation field at "+path)
			}
			rules = append(rules, "normal-recursive-source-firewall")
		}
		if version.ArtifactKind == domain.StructureKindChapter {
			if task.Mode == domain.RevisionModeNormal {
				var detail domain.NormalDetailedOutlineCandidate
				if err := json.Unmarshal(version.Payload, &detail); err != nil || detail.ChapterID == "" || detail.VolumeID == "" || detail.ArcID == "" || detail.CurrentNumber <= 0 {
					findings = append(findings, "normal chapter scope lacks stable chapter/arc/volume identity")
				} else {
					findings = append(findings, auditOutlineEntrySemantics(detail.ChapterID, detail.CurrentNumber, detail.Outline, impactByID)...)
				}
				rules = append(rules, "chapter-identity-and-adjacency-continuity", "structured-goal-conflict-choice-cost-result", "character-stage-and-climax-exit", "required-recommended-impact-causality")
			} else {
				var detail domain.AdaptationDetailedOutlineCandidate
				if err := json.Unmarshal(version.Payload, &detail); err != nil || detail.ChapterID == "" || detail.VolumeID == "" || detail.ArcID == "" || detail.CurrentNumber <= 0 {
					findings = append(findings, "adaptation chapter scope lacks stable target identity")
				} else {
					outline := detail.Outline.OutlineEntry
					outline.Chapter, outline.Title = detail.Outline.Chapter, detail.Outline.Title
					findings = append(findings, auditOutlineEntrySemantics(detail.ChapterID, detail.CurrentNumber, outline, impactByID)...)
					if detail.Outline.IsAdded && (len(detail.Outline.SourceChapters) != 0 || detail.Outline.SourceRange.From != 0 || detail.Outline.SourceRange.To != 0) {
						findings = append(findings, "added adaptation chapter illegally claims source coverage")
					}
					if !detail.Outline.IsAdded && len(detail.Outline.SourceChapters) == 0 && detail.Outline.SourceRange.From <= 0 {
						findings = append(findings, "adaptation chapter has no authoritative source mapping")
					}
				}
				rules = append(rules, "adaptation-source-mapping", "authoritative-adaptation-coverage-and-ownership", "authoritative-preserve-required-forbidden-relationship-state-granularity-rune-contract")
			}
		}
		if strings.Contains(expected.Scope, "rework_intent") {
			var intent struct {
				ChapterID     string `json:"chapter_id"`
				CurrentNumber int    `json:"current_number"`
				Reason        string `json:"reason"`
			}
			if json.Unmarshal(version.Payload, &intent) != nil || intent.ChapterID == "" || intent.CurrentNumber <= 0 || strings.TrimSpace(intent.Reason) == "" {
				findings = append(findings, "prose rework intent lacks stable target, display projection, or causal reason")
			}
			rules = append(rules, "prose-impact-required")
		}
	}
	if strings.Contains(expected.Scope, "book") || strings.Contains(expected.Scope, "volume") || strings.Contains(expected.Scope, "arc") || strings.Contains(expected.Scope, "global") || strings.Contains(expected.Scope, "parent") {
		if expected.FromChapter > 0 && expected.ToChapter > 0 && expected.ToChapter < expected.FromChapter {
			findings = append(findings, "aggregate scope has an inverted chapter range")
		}
		rules = append(rules, "signed-child-artifact-coverage", "display-order-continuity")
	}
	return uniqueExpansionFindings(findings), uniqueExpansionFindings(rules)
}

func expansionVersionsForExpectation(task ExpansionAuditTask, expected domain.RevisionAuditExpectation) []domain.ArtifactVersion {
	ids := make(map[string]struct{})
	for _, id := range strings.Split(expected.ScopeID, "+") {
		ids[id] = struct{}{}
	}
	result := make([]domain.ArtifactVersion, 0)
	for _, version := range task.CandidateVersions {
		if _, ok := ids[version.ArtifactID]; ok {
			result = append(result, version)
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(version.Payload, &fields) != nil {
			continue
		}
		for _, field := range []string{"chapter_id", "arc_id", "volume_id"} {
			var id string
			_ = json.Unmarshal(fields[field], &id)
			if id == expected.ScopeID {
				result = append(result, version)
				break
			}
		}
	}
	if len(result) == 0 && (strings.Contains(expected.Scope, "book") || strings.Contains(expected.Scope, "global") || strings.Contains(expected.Scope, "batch") || strings.Contains(expected.Scope, "volume") || strings.Contains(expected.Scope, "arc") || strings.Contains(expected.Scope, "parent")) {
		return append([]domain.ArtifactVersion(nil), task.CandidateVersions...)
	}
	return result
}

func forbiddenExpansionModePath(value any, path string) string {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			lower := strings.ToLower(key)
			if strings.Contains(lower, "source") || strings.Contains(lower, "adaptation") || strings.Contains(lower, "coverage") {
				return path + "." + key
			}
			if found := forbiddenExpansionModePath(child, path+"."+key); found != "" {
				return found
			}
		}
	case []any:
		for index, child := range typed {
			if found := forbiddenExpansionModePath(child, fmt.Sprintf("%s[%d]", path, index)); found != "" {
				return found
			}
		}
	}
	return ""
}

func auditOutlineEntrySemantics(id string, number int, outline domain.OutlineEntry, impact map[string]domain.RevisionImpactItem) []string {
	var findings []string
	if outline.ID != id || outline.Chapter != number {
		findings = append(findings, "chapter stable identity/display projection mismatch for "+id)
	}
	if strings.TrimSpace(outline.Title) == "" || strings.TrimSpace(outline.CoreEvent) == "" || strings.TrimSpace(outline.Hook) == "" || len(outline.Scenes) == 0 {
		findings = append(findings, "chapter dramatic contract is incomplete for "+id)
	}
	core, hook := strings.ToLower(outline.CoreEvent), strings.ToLower(outline.Hook)
	if (strings.Contains(core, "dies") || strings.Contains(core, "死亡") || strings.Contains(core, "牺牲")) && (strings.Contains(hook, "continues") || strings.Contains(hook, "继续行动") || strings.Contains(hook, "安然出现")) {
		findings = append(findings, "chapter continuity contradiction: terminal outcome conflicts with hook for "+id)
	}
	findings = append(findings, auditTypedNarrativeContradictions(outline)...)
	item, ok := impact[id]
	if ok && (item.Requirement == domain.StructureImpactRequired || item.Requirement == domain.StructureImpactRecommended) && len(item.DependencyEvidence) == 0 {
		findings = append(findings, "chapter lacks required causal impact contract for "+id)
	}
	return findings
}

func expansionDependencyRefs(children []domain.ExpansionDependencyReview) []ExpansionDependencyArtifactRef {
	if len(children) == 0 {
		return nil
	}
	refs := make([]ExpansionDependencyArtifactRef, 0, len(children))
	for _, child := range children {
		refs = append(refs, ExpansionDependencyArtifactRef{ArtifactID: child.ArtifactSignature, Version: 1, Stage: child.Stage, ScopeID: child.ScopeID, InputSignature: child.InputSignature, OutputSignature: child.OutputSignature, Decision: child.Decision, ArtifactSignature: child.ArtifactSignature})
	}
	return refs
}

// BindExpansionDependencyChildren creates the canonical parent edge contract.
// The caller must have reloaded the signed children from the durable repository.
func BindExpansionDependencyChildren(task ExpansionDependencyAuditTask, children []domain.ExpansionDependencyReview) ExpansionDependencyAuditTask {
	task.ChildReviews = append([]domain.ExpansionDependencyReview(nil), children...)
	task.ChildArtifacts = expansionDependencyRefs(children)
	task.ContractSignature = expansionDependencyContractSignature(task)
	return task
}

// ExpansionDependencyRefs exposes immutable artifact edges for the independent
// runner after it has recursively reloaded and verified child artifacts.
func ExpansionDependencyRefs(children []domain.ExpansionDependencyReview) []ExpansionDependencyArtifactRef {
	return expansionDependencyRefs(children)
}

func expansionDependencyContractSignature(task ExpansionDependencyAuditTask) string {
	payload, _ := json.Marshal(struct {
		ID, RootAuditTaskID, Stage, ScopeID, InputSignature, OutputSignature string
		Dependencies                                                         []string
		ChildTaskIDs                                                         []string
		Children                                                             []ExpansionDependencyArtifactRef
	}{task.ID, task.RootAuditTaskID, task.Stage, task.ScopeID, task.InputSignature, domain.JSONContentSignature([]byte(task.Output)), task.DependencyIDs, task.ChildTaskIDs, task.ChildArtifacts})
	return domain.JSONContentSignature(payload)
}

func expansionExpectationSignature(task ExpansionAuditTask, expected domain.RevisionAuditExpectation) (string, bool) {
	if signature, ok := expansionDerivedAggregateSignature(task, expected); ok {
		return signature, true
	}
	byID := make(map[string]domain.ArtifactVersion, len(task.CandidateVersions))
	for _, version := range task.CandidateVersions {
		byID[version.ArtifactID] = version
		if version.ArtifactID == expected.ScopeID {
			return domain.JSONContentSignature(version.Payload), true
		}
	}
	ids := strings.Split(expected.ScopeID, "+")
	if len(ids) > 1 {
		versions := make([]domain.ArtifactVersion, 0, len(ids))
		for _, id := range ids {
			version, ok := byID[id]
			if !ok {
				return "", false
			}
			versions = append(versions, version)
		}
		return expansionCompositeSignature(versions), true
	}
	matched := make([]domain.ArtifactVersion, 0)
	for _, version := range task.CandidateVersions {
		var fields map[string]json.RawMessage
		if json.Unmarshal(version.Payload, &fields) != nil {
			continue
		}
		for _, field := range []string{"chapter_id", "arc_id", "volume_id"} {
			var id string
			if json.Unmarshal(fields[field], &id) == nil && id == expected.ScopeID {
				if strings.Contains(expected.Scope, "chapter") {
					if strings.Contains(expected.Scope, "deterministic") || strings.Contains(expected.Scope, "semantic") {
						var detail domain.AdaptationDetailedOutlineCandidate
						if json.Unmarshal(version.Payload, &detail) == nil {
							payload, _ := json.Marshal(detail.Outline)
							return domain.JSONContentSignature(payload), true
						}
					}
					return domain.JSONContentSignature(version.Payload), true
				}
				if strings.Contains(expected.Scope, "intent") {
					return domain.JSONContentSignature(version.Payload), true
				}
				matched = append(matched, version)
			}
		}
	}
	if len(matched) > 0 {
		return expansionCompositeSignature(matched), true
	}
	return "", false
}

func expansionDerivedAggregateSignature(task ExpansionAuditTask, expected domain.RevisionAuditExpectation) (string, bool) {
	if slices.Contains([]string{"book", "skeleton_book", "skeleton_book_batch", "structure_global", "structure_parent_batch"}, expected.Scope) {
		byID := make(map[string]domain.ArtifactVersion, len(task.CandidateVersions))
		for _, version := range task.CandidateVersions {
			byID[version.ArtifactID] = version
		}
		versions := make([]domain.ArtifactVersion, 0)
		for _, id := range strings.Split(expected.ScopeID, "+") {
			if version, ok := byID[id]; ok {
				versions = append(versions, version)
			}
		}
		if len(versions) > 0 {
			return expansionCompositeSignature(versions), true
		}
	}
	if expected.Scope == "book_batch" {
		var plan domain.BatchPlan
		if !decodeExpansionArtifactKind(task.CandidateVersions, domain.NormalArtifactBatchPlan, &plan) {
			return "", false
		}
		for _, batch := range plan.Batches {
			if batch.ID == expected.ScopeID {
				return expansionCompositeSignature(expansionVersionsByChapterIDs(task.CandidateVersions, batch.ChapterIDs)), true
			}
		}
	}
	if expected.Scope != "parent_batch" && expected.Scope != "volume" && expected.Scope != "global" && expected.Scope != "chapter_deterministic" && expected.Scope != "chapter_semantic" {
		return "", false
	}
	var snapshot domain.AdaptationPlanRevisionCandidate
	if !decodeExpansionArtifactKind(task.CandidateVersions, domain.AdaptationRevisionArtifactPlanSnapshot, &snapshot) {
		return "", false
	}
	plan := snapshot.Plan
	for _, version := range task.CandidateVersions {
		if version.ArtifactKind != domain.StructureKindChapter {
			continue
		}
		var detail domain.AdaptationDetailedOutlineCandidate
		if json.Unmarshal(version.Payload, &detail) != nil {
			continue
		}
		for index := range plan.Chapters {
			if plan.Chapters[index].ID == detail.ChapterID {
				sealed := plan.Chapters[index]
				sealed.Title = detail.Outline.Title
				sealed.OutlineEntry.Title = detail.Outline.OutlineEntry.Title
				sealed.OutlineEntry.CoreEvent = detail.Outline.OutlineEntry.CoreEvent
				sealed.OutlineEntry.Hook = detail.Outline.OutlineEntry.Hook
				sealed.OutlineEntry.Scenes = append([]string(nil), detail.Outline.OutlineEntry.Scenes...)
				plan.Chapters[index] = sealed
			}
		}
	}
	if expected.Scope == "chapter_deterministic" || expected.Scope == "chapter_semantic" {
		for _, chapter := range plan.Chapters {
			if chapter.ID == expected.ScopeID {
				payload, _ := json.Marshal(chapter)
				return domain.JSONContentSignature(payload), true
			}
		}
	}
	if expected.Scope == "global" {
		payload, _ := json.Marshal(plan)
		return domain.JSONContentSignature(payload), true
	}
	chapterVersions := make(map[string]domain.ArtifactVersion, len(plan.Chapters))
	for _, chapter := range plan.Chapters {
		payload, _ := json.Marshal(chapter)
		chapterVersions[chapter.ID] = domain.ArtifactVersion{ArtifactID: chapter.ID, Payload: payload, ContentSignature: domain.JSONContentSignature(payload)}
	}
	if expected.Scope == "volume" {
		for _, volume := range plan.Volumes {
			if volume.ID != expected.ScopeID {
				continue
			}
			versions := make([]domain.ArtifactVersion, 0)
			for _, chapter := range plan.Chapters {
				if chapter.Chapter >= volume.TargetFrom && chapter.Chapter <= volume.TargetTo {
					versions = append(versions, chapterVersions[chapter.ID])
				}
			}
			return expansionCompositeSignature(versions), true
		}
	}
	var batches domain.BatchPlan
	if decodeExpansionArtifactKind(task.CandidateVersions, domain.AdaptationRevisionArtifactBatchPlan, &batches) {
		for _, batch := range batches.Batches {
			if batch.ID != expected.ScopeID {
				continue
			}
			versions := make([]domain.ArtifactVersion, 0, len(batch.ChapterIDs))
			for _, id := range batch.ChapterIDs {
				versions = append(versions, chapterVersions[id])
			}
			return expansionCompositeSignature(versions), true
		}
	}
	return "", false
}

func decodeExpansionArtifactKind(versions []domain.ArtifactVersion, kind string, target any) bool {
	for _, version := range versions {
		if version.ArtifactKind == kind && json.Unmarshal(version.Payload, target) == nil {
			return true
		}
	}
	return false
}

func expansionVersionsByChapterIDs(versions []domain.ArtifactVersion, chapterIDs []string) []domain.ArtifactVersion {
	byChapter := make(map[string]domain.ArtifactVersion)
	for _, version := range versions {
		var detail domain.NormalDetailedOutlineCandidate
		if json.Unmarshal(version.Payload, &detail) == nil && detail.ChapterID != "" {
			byChapter[detail.ChapterID] = version
		}
	}
	result := make([]domain.ArtifactVersion, 0, len(chapterIDs))
	for _, id := range chapterIDs {
		if version, ok := byChapter[id]; ok {
			result = append(result, version)
		}
	}
	return result
}

func expansionCompositeSignature(versions []domain.ArtifactVersion) string {
	identities := make([]string, 0, len(versions))
	for _, version := range versions {
		identities = append(identities, version.ArtifactID+":"+domain.JSONContentSignature(version.Payload))
	}
	slices.Sort(identities)
	payload, _ := json.Marshal(identities)
	return domain.ContentSignature(payload)
}

func auditDependencySemantics(task ExpansionDependencyAuditTask) []string {
	output := strings.TrimSpace(task.Output)
	lower := strings.ToLower(output)
	var findings []string
	if len([]rune(output)) < 8 {
		findings = append(findings, "dependency summary is too small for semantic review")
	}
	for _, marker := range []string{"audit_fail", "[needs_fix]", "placeholder", "todo", "待补"} {
		if strings.Contains(lower, marker) {
			findings = append(findings, "dependency content contains unresolved marker: "+marker)
		}
	}
	if (task.Stage == "batch" || task.Stage == "skeleton") && len(task.DependencyIDs) == 0 {
		findings = append(findings, "aggregate dependency audit has no child artifacts")
	}
	if strings.HasPrefix(task.Stage, "adaptation_") && domain.JSONContentSignature([]byte(task.Output)) != task.InputSignature {
		findings = append(findings, "adaptation checkpoint signature does not bind its reviewed content")
	}
	switch task.Stage {
	case "local":
		if len(task.DependencyIDs) == 0 {
			findings = append(findings, "local scope has no stable chapter coverage")
		}
	case "volume", "batch", "skeleton":
		if len(task.ChildArtifacts) != len(task.DependencyIDs) {
			findings = append(findings, "aggregate scope does not bind every declared signed child")
		}
	case "adaptation_chapter":
		var detail domain.AdaptationDetailedOutlineCandidate
		if json.Unmarshal([]byte(task.Output), &detail) != nil || detail.ChapterID != task.ScopeID {
			findings = append(findings, "adaptation chapter leaf does not bind its stable candidate payload")
		} else if detail.Outline.IsAdded && (len(detail.Outline.SourceChapters) > 0 || detail.Outline.SourceRange.From > 0 || detail.Outline.SourceRange.To > 0) {
			findings = append(findings, "added adaptation chapter claims forbidden source coverage")
		} else if !detail.Outline.IsAdded && len(detail.Outline.SourceChapters) == 0 && detail.Outline.SourceRange.From <= 0 {
			findings = append(findings, "adaptation chapter is missing source ownership")
		}
	case "adaptation_batch":
		var batch domain.BatchWork
		if json.Unmarshal([]byte(task.Output), &batch) != nil || batch.ID != task.ScopeID {
			findings = append(findings, "adaptation batch contract is invalid")
		} else if !slices.Equal(batch.ChapterIDs, task.DependencyIDs) {
			findings = append(findings, "adaptation batch coverage differs from signed chapter children")
		}
	case "adaptation_volume", "adaptation_whole_book":
		if len(task.DependencyIDs) == 0 || len(task.ChildArtifacts) != len(task.DependencyIDs) {
			findings = append(findings, "adaptation aggregate does not recursively bind all signed children")
		}
	}
	return uniqueExpansionFindings(findings)
}

func uniqueExpansionFindings(values []string) []string {
	seen := map[string]struct{}{}
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func encodeExpansionPublicKey(key ed25519.PublicKey) string {
	return base64.RawStdEncoding.EncodeToString(key)
}

func decodeExpansionPublicKey(value string) (ed25519.PublicKey, error) {
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("invalid expansion auditor public key")
	}
	return ed25519.PublicKey(key), nil
}

func verifyExpansionDependencyReview(review domain.ExpansionDependencyReview, trustedKeys ...ed25519.PublicKey) error {
	if err := review.Validate(); err != nil {
		return err
	}
	key, err := decodeExpansionPublicKey(review.ReviewerPublicKey)
	if err != nil {
		return err
	}
	trusted := key
	if len(trustedKeys) > 0 {
		trusted = trustedKeys[0]
	}
	if len(trusted) != ed25519.PublicKeySize || !slices.Equal(key, trusted) {
		return fmt.Errorf("dependency review %s/%s was not signed by the trusted auditor", review.Stage, review.ScopeID)
	}
	signature, err := base64.RawStdEncoding.DecodeString(review.ArtifactSignature)
	if err != nil {
		return fmt.Errorf("dependency review %s/%s signature is malformed", review.Stage, review.ScopeID)
	}
	copy := review
	copy.ArtifactSignature = ""
	payload, _ := json.Marshal(copy)
	if !ed25519.Verify(key, payload, signature) {
		return fmt.Errorf("dependency review %s/%s signature mismatch", review.Stage, review.ScopeID)
	}
	return nil
}

func (planner *ExpansionPlanner) validateExpansionAuditArtifact(artifact ExpansionAuditArtifact, task ExpansionAuditTask, session *domain.RevisionSession) error {
	if session == nil || artifact.TaskID != task.ID || artifact.RevisionID != session.ID || artifact.Revision != session.Revision ||
		artifact.Stage != session.Stage || artifact.Scope != task.Scope || artifact.ScopeID != task.ScopeID ||
		artifact.CandidateSignature != session.CandidateSignature || artifact.StructureSignature != task.StructureSignature || artifact.PolicyVersion != expansionAuditorPolicyVersion {
		return fmt.Errorf("signed expansion audit artifact does not match task, revision, stage, scope, or policy")
	}
	key, err := decodeExpansionPublicKey(artifact.ReviewerPublicKey)
	if err != nil || len(planner.auditorPublicKey) != ed25519.PublicKeySize || !slices.Equal(key, planner.auditorPublicKey) {
		return fmt.Errorf("signed expansion audit artifact has an untrusted reviewer identity")
	}
	signature, err := base64.RawStdEncoding.DecodeString(artifact.Signature)
	if err != nil {
		return fmt.Errorf("signed expansion audit artifact signature is malformed")
	}
	copy := artifact
	copy.Signature = ""
	payload, _ := json.Marshal(copy)
	if !ed25519.Verify(key, payload, signature) {
		return fmt.Errorf("signed expansion audit artifact signature is invalid")
	}
	expectedPayload, _ := json.Marshal(session.AuditExpectations)
	actualPayload, _ := json.Marshal(artifact.ExpectationSet)
	if !slices.Equal(expectedPayload, actualPayload) {
		return fmt.Errorf("signed expansion audit expectation set does not match the revision")
	}
	if domain.CandidateSignature(artifact.CandidateVersions) != session.CandidateSignature {
		return fmt.Errorf("signed expansion audit candidate version set does not match the revision")
	}
	for _, review := range artifact.DependencyReviews {
		if err := verifyExpansionDependencyReview(review, planner.auditorPublicKey); err != nil {
			return err
		}
	}
	return nil
}

func expansionCheckpointReview(artifact ExpansionAuditArtifact, stage, scopeID string) (domain.ExpansionDependencyReview, error) {
	for _, review := range artifact.CheckpointReviews {
		if review.Stage == stage && review.ScopeID == scopeID {
			return review, nil
		}
	}
	return domain.ExpansionDependencyReview{}, fmt.Errorf("missing independently signed %s checkpoint %s", stage, scopeID)
}

func completeAdaptationAuditCheckpoints(service *AdaptationRevisionService, artifact ExpansionAuditArtifact, trusted ed25519.PublicKey) error {
	if service == nil || artifact.Decision != "pass" {
		return fmt.Errorf("adaptation dependency audit did not pass")
	}
	runtime, err := service.store.Adaptation.LoadRevisionRuntime()
	if err != nil {
		return err
	}
	for _, batch := range runtime.BatchPlan.Batches {
		if batch.Status == domain.BatchStatusCompleted {
			continue
		}
		review, err := expansionCheckpointReview(artifact, "adaptation_batch", batch.ID)
		if err != nil {
			return err
		}
		if err := verifyExpansionDependencyReview(review, trusted); err != nil {
			return err
		}
		if review.Decision != "pass" {
			return fmt.Errorf("adaptation batch review needs fix: %s", strings.Join(review.Findings, "; "))
		}
		if _, err = service.RunBatchCommand(artifact.RevisionID, domain.AdaptationRevisionBatchStart, batch.ID, ""); err != nil {
			return err
		}
		if _, err = service.RunBatchCommand(artifact.RevisionID, domain.AdaptationRevisionBatchGenerated, batch.ID, ""); err != nil {
			return err
		}
		if _, err = service.RunBatchCommand(artifact.RevisionID, domain.AdaptationRevisionBatchAuditPass, batch.ID, review.ArtifactSignature); err != nil {
			return err
		}
	}
	for _, volume := range runtime.BatchPlan.VolumeReviews {
		if volume.Status == domain.BatchReviewCompleted {
			continue
		}
		review, err := expansionCheckpointReview(artifact, "adaptation_volume", volume.ScopeID)
		if err != nil {
			return err
		}
		if err := verifyExpansionDependencyReview(review, trusted); err != nil {
			return err
		}
		if review.Decision != "pass" {
			return fmt.Errorf("adaptation volume review needs fix: %s", strings.Join(review.Findings, "; "))
		}
		if _, err = service.RunBatchCommand(artifact.RevisionID, domain.AdaptationRevisionVolumeReviewStart, volume.ScopeID, ""); err != nil {
			return err
		}
		if _, err = service.RunBatchCommand(artifact.RevisionID, domain.AdaptationRevisionVolumeReviewPass, volume.ScopeID, review.ArtifactSignature); err != nil {
			return err
		}
	}
	if runtime.BatchPlan.WholeBookReview.Status != domain.BatchReviewCompleted {
		review, err := expansionCheckpointReview(artifact, "adaptation_whole_book", "whole-book")
		if err != nil {
			return err
		}
		if err := verifyExpansionDependencyReview(review, trusted); err != nil {
			return err
		}
		if review.Decision != "pass" {
			return fmt.Errorf("adaptation whole-book review needs fix: %s", strings.Join(review.Findings, "; "))
		}
		if _, err = service.RunBatchCommand(artifact.RevisionID, domain.AdaptationRevisionGlobalReviewStart, "", ""); err != nil {
			return err
		}
		if _, err = service.RunBatchCommand(artifact.RevisionID, domain.AdaptationRevisionGlobalReviewPass, "", review.ArtifactSignature); err != nil {
			return err
		}
	}
	return nil
}
