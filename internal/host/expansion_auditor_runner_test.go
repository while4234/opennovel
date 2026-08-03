package host

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/voocel/ainovel-cli/internal/domain"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

// ExpansionAuditRunner is a test-only in-process authority. Production owns
// no such constructor; cmd/expansion-auditor composes the private runner.
type ExpansionAuditRunner struct {
	store      *storepkg.Store
	privateKey ed25519.PrivateKey
}

var testExpansionAuditorIdentities = struct {
	sync.Mutex
	keys map[string]ed25519.PrivateKey
}{keys: make(map[string]ed25519.PrivateKey)}

func NewExpansionAuditRunner(st *storepkg.Store) (*ExpansionAuditRunner, error) {
	testExpansionAuditorIdentities.Lock()
	privateKey := testExpansionAuditorIdentities.keys[st.Dir()]
	if len(privateKey) == 0 {
		_, generated, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			testExpansionAuditorIdentities.Unlock()
			return nil, err
		}
		privateKey = generated
		testExpansionAuditorIdentities.keys[st.Dir()] = privateKey
	}
	testExpansionAuditorIdentities.Unlock()
	runner := &ExpansionAuditRunner{store: st, privateKey: privateKey}
	if err := st.SaveExpansionAuditorTrust(storepkg.ExpansionAuditorTrust{PublicKeyHex: hex.EncodeToString(runner.PublicKey())}); err != nil {
		return nil, err
	}
	return runner, nil
}

func (runner *ExpansionAuditRunner) ReviewerIdentity() string             { return "test-expansion-auditor" }
func (runner *ExpansionAuditRunner) ReviewerPublicKey() ed25519.PublicKey { return runner.PublicKey() }
func (runner *ExpansionAuditRunner) PublicKey() ed25519.PublicKey {
	return runner.privateKey.Public().(ed25519.PublicKey)
}

func (runner *ExpansionAuditRunner) ReviewRevision(ctx context.Context, task ExpansionAuditTask) (ExpansionAuditArtifact, error) {
	artifact, err := EvaluateExpansionRevision(ctx, task, runner)
	if err != nil {
		return ExpansionAuditArtifact{}, err
	}
	artifact.Signature = testSignExpansionArtifact(runner.privateKey, artifact)
	return artifact, nil
}

func (runner *ExpansionAuditRunner) ReviewDependency(_ context.Context, task ExpansionDependencyAuditTask) (domain.ExpansionDependencyReview, error) {
	key := runner.PublicKey()
	review, err := EvaluateExpansionDependency(task, runner.ReviewerIdentity(), base64.RawStdEncoding.EncodeToString(key), key)
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	review.ArtifactSignature = testSignExpansionDependencyReview(runner.privateKey, review)
	return review, nil
}

func (runner *ExpansionAuditRunner) LoadAdaptationContract(_ context.Context) (*domain.AdaptationPlan, *domain.AdaptationSourceManifest, error) {
	base, err := runner.store.Adaptation.LoadPlan()
	if err != nil {
		return nil, nil, err
	}
	manifest, err := runner.store.Adaptation.LoadSourceManifest()
	if err != nil {
		return nil, nil, err
	}
	return base, manifest, nil
}

func (runner *ExpansionAuditRunner) ProcessRevisionTask(ctx context.Context, revisionID string) (ExpansionAuditArtifact, error) {
	runtime, err := runner.store.LoadExpansionRuntime()
	if err != nil {
		return ExpansionAuditArtifact{}, err
	}
	var newest ExpansionAuditTask
	found := false
	for _, payload := range runtime.PendingAudits {
		var task ExpansionAuditTask
		if json.Unmarshal(payload, &task) == nil && task.RevisionID == strings.TrimSpace(revisionID) && task.Status == "pending" && (!found || task.Revision > newest.Revision) {
			newest, found = task, true
		}
	}
	if !found {
		return ExpansionAuditArtifact{}, fmt.Errorf("pending expansion audit task not found")
	}
	if len(newest.CheckpointTaskIDs) > 0 {
		reviews := make([]domain.ExpansionDependencyReview, 0, len(newest.CheckpointTaskIDs))
		allReviews := make([]domain.ExpansionDependencyReview, 0)
		for _, taskID := range newest.CheckpointTaskIDs {
			review, processErr := runner.processDependencyGraph(ctx, taskID, map[string]bool{})
			if processErr != nil {
				return ExpansionAuditArtifact{}, processErr
			}
			reviews = append(reviews, review)
			graphReviews, collectErr := runner.collectDependencyGraphReviews(ctx, taskID, map[string]bool{})
			if collectErr != nil {
				return ExpansionAuditArtifact{}, collectErr
			}
			allReviews = append(allReviews, graphReviews...)
		}
		rootRefs := ExpansionDependencyRefs(reviews)
		if len(newest.CheckpointArtifacts) > 0 && !slices.Equal(newest.CheckpointArtifacts, rootRefs) {
			return ExpansionAuditArtifact{}, fmt.Errorf("durable checkpoint root artifact bindings changed")
		}
		newest.CheckpointArtifacts = rootRefs
		payload, marshalErr := json.Marshal(newest)
		if marshalErr != nil {
			return ExpansionAuditArtifact{}, marshalErr
		}
		runtime.PendingAudits[newest.ID] = payload
		if err := runner.store.SaveExpansionRuntime(runtime); err != nil {
			return ExpansionAuditArtifact{}, err
		}
		newest.CheckpointReviews = allReviews
	} else if len(newest.CheckpointArtifacts) > 0 {
		return ExpansionAuditArtifact{}, fmt.Errorf("durable checkpoint root task bindings are missing")
	}
	return runner.ReviewRevision(ctx, newest)
}

func (runner *ExpansionAuditRunner) collectDependencyGraphReviews(ctx context.Context, taskID string, visiting map[string]bool) ([]domain.ExpansionDependencyReview, error) {
	if visiting[taskID] {
		return nil, fmt.Errorf("dependency audit graph contains a cycle at %s", taskID)
	}
	visiting[taskID] = true
	defer delete(visiting, taskID)
	runtime, err := runner.store.LoadExpansionRuntime()
	if err != nil {
		return nil, err
	}
	payload, ok := runtime.PendingDependencyAudits[taskID]
	if !ok {
		return nil, fmt.Errorf("durable dependency task %s is missing", taskID)
	}
	var task ExpansionDependencyAuditTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return nil, err
	}
	reviews := make([]domain.ExpansionDependencyReview, 0)
	for _, childID := range task.ChildTaskIDs {
		children, childErr := runner.collectDependencyGraphReviews(ctx, childID, visiting)
		if childErr != nil {
			return nil, childErr
		}
		reviews = append(reviews, children...)
	}
	review, err := runner.processDependencyGraph(ctx, taskID, map[string]bool{})
	if err != nil {
		return nil, err
	}
	return append(reviews, review), nil
}

func (runner *ExpansionAuditRunner) processDependencyGraph(ctx context.Context, taskID string, visiting map[string]bool) (domain.ExpansionDependencyReview, error) {
	if visiting[taskID] {
		return domain.ExpansionDependencyReview{}, fmt.Errorf("dependency audit graph contains a cycle at %s", taskID)
	}
	visiting[taskID] = true
	defer delete(visiting, taskID)
	runtime, err := runner.store.LoadExpansionRuntime()
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	payload, ok := runtime.PendingDependencyAudits[taskID]
	if !ok {
		return domain.ExpansionDependencyReview{}, fmt.Errorf("durable dependency task %s is missing", taskID)
	}
	var task ExpansionDependencyAuditTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	if task.ID != taskID || task.RootAuditTaskID == "" || !strings.HasPrefix(task.ID, task.RootAuditTaskID+":checkpoint:") {
		return domain.ExpansionDependencyReview{}, fmt.Errorf("durable dependency task identity is invalid for %s", taskID)
	}
	if task.Status == "completed" {
		stored, ok := runtime.DependencyReviews[task.ArtifactID]
		if !ok || stored.ArtifactSignature != task.ArtifactID {
			return domain.ExpansionDependencyReview{}, fmt.Errorf("durable dependency artifact %s is missing", task.ArtifactID)
		}
		children, childErr := runner.reloadCompletedDependencyChildren(ctx, task, visiting)
		if childErr != nil {
			return domain.ExpansionDependencyReview{}, childErr
		}
		bound := BindExpansionDependencyChildren(task, children)
		if bound.ContractSignature != task.ContractSignature {
			return domain.ExpansionDependencyReview{}, fmt.Errorf("durable dependency contract for %s is stale", task.ID)
		}
		recomputed, reviewErr := runner.ReviewDependency(ctx, bound)
		if reviewErr != nil || recomputed.ArtifactSignature != stored.ArtifactSignature {
			return domain.ExpansionDependencyReview{}, fmt.Errorf("durable dependency artifact %s cannot be immutably replayed", task.ArtifactID)
		}
		return stored, nil
	}
	if len(task.ChildTaskIDs) != len(task.DependencyIDs) {
		return domain.ExpansionDependencyReview{}, fmt.Errorf("durable dependency child task bindings are incomplete for %s", taskID)
	}
	children := make([]domain.ExpansionDependencyReview, 0, len(task.ChildTaskIDs))
	for index, childID := range task.ChildTaskIDs {
		if !strings.HasPrefix(childID, task.RootAuditTaskID+":checkpoint:") {
			return domain.ExpansionDependencyReview{}, fmt.Errorf("durable child dependency task %s belongs to another root", childID)
		}
		child, childErr := runner.processDependencyGraph(ctx, childID, visiting)
		if childErr != nil {
			return domain.ExpansionDependencyReview{}, childErr
		}
		if child.ScopeID != task.DependencyIDs[index] {
			return domain.ExpansionDependencyReview{}, fmt.Errorf("durable child dependency task %s scope binding is stale", childID)
		}
		children = append(children, child)
	}
	task = BindExpansionDependencyChildren(task, children)
	review, err := runner.ReviewDependency(ctx, task)
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	runtime, err = runner.store.LoadExpansionRuntime()
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	task.Status = "completed"
	task.ArtifactID = review.ArtifactSignature
	task.ChildReviews = nil
	payload, err = json.Marshal(task)
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	runtime.PendingDependencyAudits[taskID] = payload
	runtime.DependencyReviews[review.ArtifactSignature] = review
	runtime.DependencyReviewIndex[task.Stage+"\x00"+task.ScopeID] = review.ArtifactSignature
	if err := runner.store.SaveExpansionRuntime(runtime); err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	return review, nil
}

func (runner *ExpansionAuditRunner) reloadCompletedDependencyChildren(ctx context.Context, task ExpansionDependencyAuditTask, visiting map[string]bool) ([]domain.ExpansionDependencyReview, error) {
	if len(task.ChildTaskIDs) != len(task.DependencyIDs) || len(task.ChildArtifacts) != len(task.ChildTaskIDs) {
		if len(task.ChildTaskIDs) == 0 && len(task.DependencyIDs) == 0 && len(task.ChildArtifacts) == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("durable completed dependency graph bindings are incomplete for %s", task.ID)
	}
	children := make([]domain.ExpansionDependencyReview, 0, len(task.ChildTaskIDs))
	for index, childTaskID := range task.ChildTaskIDs {
		if !strings.HasPrefix(childTaskID, task.RootAuditTaskID+":checkpoint:") {
			return nil, fmt.Errorf("durable completed child task %s belongs to another root", childTaskID)
		}
		child, err := runner.processDependencyGraph(ctx, childTaskID, visiting)
		if err != nil {
			return nil, err
		}
		ref := task.ChildArtifacts[index]
		if child.ScopeID != task.DependencyIDs[index] || child.ArtifactSignature != ref.ArtifactID || child.ArtifactSignature != ref.ArtifactSignature {
			return nil, fmt.Errorf("durable completed child artifact %s is missing or replaced", ref.ArtifactID)
		}
		children = append(children, child)
	}
	return children, nil
}

func (runner *ExpansionAuditRunner) ProcessDependencyTask(ctx context.Context, taskID string) (domain.ExpansionDependencyReview, error) {
	runtime, err := runner.store.LoadExpansionRuntime()
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	payload, ok := runtime.PendingDependencyAudits[strings.TrimSpace(taskID)]
	if !ok {
		return domain.ExpansionDependencyReview{}, fmt.Errorf("pending dependency audit task not found")
	}
	var task ExpansionDependencyAuditTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	if task.RootAuditTaskID != "" && strings.Contains(task.ID, ":checkpoint:") {
		return runner.processDependencyGraph(ctx, task.ID, map[string]bool{})
	}
	if len(task.ChildArtifacts) > 0 {
		task.ChildReviews = nil
		for _, ref := range task.ChildArtifacts {
			candidate, found := runtime.DependencyReviews[ref.ArtifactID]
			if !found || candidate.ArtifactSignature != ref.ArtifactSignature {
				return domain.ExpansionDependencyReview{}, fmt.Errorf("durable child dependency artifact %s is missing", ref.ArtifactID)
			}
			task.ChildReviews = append(task.ChildReviews, candidate)
		}
	}
	return runner.ReviewDependency(ctx, task)
}

func testSignExpansionArtifact(privateKey ed25519.PrivateKey, artifact ExpansionAuditArtifact) string {
	artifact.Signature = ""
	payload, _ := json.Marshal(artifact)
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
}

func testSignExpansionDependencyReview(privateKey ed25519.PrivateKey, review domain.ExpansionDependencyReview) string {
	review.ArtifactSignature = ""
	payload, _ := json.Marshal(review)
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
}
