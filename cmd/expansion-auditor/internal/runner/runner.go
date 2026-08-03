// Package runner is the private execution boundary for expansion
// audits. It is imported by cmd/expansion-auditor only; product host code owns
// neither the private identity nor a signing constructor.
package runner

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const (
	identityName = "independent-expansion-auditor"
	identityFile = ".ai/revisions/expansion-auditor-private/identity.json"
)

type privateIdentity struct {
	PrivateKeyHex string `json:"private_key_hex"`
}

// Runner is constructed only by the independent auditor executable. The
// product process talks to that executable through expansionauditorclient.
type Runner struct {
	store      *storepkg.Store
	privateKey ed25519.PrivateKey
	identity   string
}

func New(projectRoot string, st *storepkg.Store) (*Runner, error) {
	if st == nil || strings.TrimSpace(projectRoot) == "" {
		return nil, fmt.Errorf("private expansion auditor project boundary is unavailable")
	}
	privateKey, err := loadOrCreatePrivateKey(projectRoot)
	if err != nil {
		return nil, err
	}
	runner := &Runner{store: st, privateKey: privateKey, identity: identityName}
	if err := runner.persistPublicTrust(); err != nil {
		return nil, err
	}
	return runner, nil
}

func (runner *Runner) ReviewerIdentity() string { return runner.identity }

func (runner *Runner) ReviewerPublicKey() ed25519.PublicKey {
	if runner == nil || len(runner.privateKey) != ed25519.PrivateKeySize {
		return nil
	}
	return slices.Clone(runner.privateKey.Public().(ed25519.PublicKey))
}

func (runner *Runner) PublicKey() ed25519.PublicKey { return runner.ReviewerPublicKey() }

func (runner *Runner) persistPublicTrust() error {
	return runner.store.SaveExpansionAuditorTrust(storepkg.ExpansionAuditorTrust{
		PublicKeyHex: hex.EncodeToString(runner.ReviewerPublicKey()),
	})
}

func (runner *Runner) ReviewRevision(ctx context.Context, task host.ExpansionAuditTask) (host.ExpansionAuditArtifact, error) {
	artifact, err := host.EvaluateExpansionRevision(ctx, task, runner)
	if err != nil {
		return host.ExpansionAuditArtifact{}, err
	}
	artifact.Signature = signArtifact(runner.privateKey, artifact)
	return artifact, nil
}

func (runner *Runner) ReviewDependency(_ context.Context, task host.ExpansionDependencyAuditTask) (domain.ExpansionDependencyReview, error) {
	publicKey := runner.ReviewerPublicKey()
	review, err := host.EvaluateExpansionDependency(
		task,
		runner.identity,
		base64.RawStdEncoding.EncodeToString(publicKey),
		publicKey,
	)
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	review.ArtifactSignature = signDependencyReview(runner.privateKey, review)
	return review, nil
}

func (runner *Runner) LoadAdaptationContract(_ context.Context) (*domain.AdaptationPlan, *domain.AdaptationSourceManifest, error) {
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

// ProcessRevisionTask reloads the durable task rather than trusting IPC input.
func (runner *Runner) ProcessRevisionTask(ctx context.Context, revisionID string) (host.ExpansionAuditArtifact, error) {
	runtime, err := runner.store.LoadExpansionRuntime()
	if err != nil {
		return host.ExpansionAuditArtifact{}, err
	}
	var newest host.ExpansionAuditTask
	found := false
	for _, payload := range runtime.PendingAudits {
		var task host.ExpansionAuditTask
		if json.Unmarshal(payload, &task) == nil && task.RevisionID == strings.TrimSpace(revisionID) && task.Status == "pending" && (!found || task.Revision > newest.Revision) {
			newest, found = task, true
		}
	}
	if !found {
		return host.ExpansionAuditArtifact{}, fmt.Errorf("pending expansion audit task not found")
	}
	if len(newest.CheckpointTaskIDs) > 0 {
		reviews := make([]domain.ExpansionDependencyReview, 0, len(newest.CheckpointTaskIDs))
		allReviews := make([]domain.ExpansionDependencyReview, 0)
		for _, taskID := range newest.CheckpointTaskIDs {
			review, processErr := runner.processDependencyGraph(ctx, taskID, map[string]bool{})
			if processErr != nil {
				return host.ExpansionAuditArtifact{}, processErr
			}
			reviews = append(reviews, review)
			graphReviews, collectErr := runner.collectDependencyGraphReviews(ctx, taskID, map[string]bool{})
			if collectErr != nil {
				return host.ExpansionAuditArtifact{}, collectErr
			}
			allReviews = append(allReviews, graphReviews...)
		}
		rootRefs := host.ExpansionDependencyRefs(reviews)
		if len(newest.CheckpointArtifacts) > 0 && !slices.Equal(newest.CheckpointArtifacts, rootRefs) {
			return host.ExpansionAuditArtifact{}, fmt.Errorf("durable checkpoint root artifact bindings changed")
		}
		newest.CheckpointArtifacts = rootRefs
		payload, marshalErr := json.Marshal(newest)
		if marshalErr != nil {
			return host.ExpansionAuditArtifact{}, marshalErr
		}
		runtime.PendingAudits[newest.ID] = payload
		if err := runner.store.SaveExpansionRuntime(runtime); err != nil {
			return host.ExpansionAuditArtifact{}, err
		}
		newest.CheckpointReviews = allReviews
	} else if len(newest.CheckpointArtifacts) > 0 {
		return host.ExpansionAuditArtifact{}, fmt.Errorf("durable checkpoint root task bindings are missing")
	}
	return runner.ReviewRevision(ctx, newest)
}

func (runner *Runner) collectDependencyGraphReviews(ctx context.Context, taskID string, visiting map[string]bool) ([]domain.ExpansionDependencyReview, error) {
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
	var task host.ExpansionDependencyAuditTask
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

func (runner *Runner) processDependencyGraph(ctx context.Context, taskID string, visiting map[string]bool) (domain.ExpansionDependencyReview, error) {
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
	var task host.ExpansionDependencyAuditTask
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
		bound := host.BindExpansionDependencyChildren(task, children)
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
	task = host.BindExpansionDependencyChildren(task, children)
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

func (runner *Runner) reloadCompletedDependencyChildren(ctx context.Context, task host.ExpansionDependencyAuditTask, visiting map[string]bool) ([]domain.ExpansionDependencyReview, error) {
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

// ProcessDependencyTask reloads every child from the durable signed-artifact
// repository. Embedded child summaries are never authoritative.
func (runner *Runner) ProcessDependencyTask(ctx context.Context, taskID string) (domain.ExpansionDependencyReview, error) {
	runtime, err := runner.store.LoadExpansionRuntime()
	if err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	payload, ok := runtime.PendingDependencyAudits[strings.TrimSpace(taskID)]
	if !ok {
		return domain.ExpansionDependencyReview{}, fmt.Errorf("pending dependency audit task not found")
	}
	var task host.ExpansionDependencyAuditTask
	if err := json.Unmarshal(payload, &task); err != nil {
		return domain.ExpansionDependencyReview{}, err
	}
	if task.RootAuditTaskID != "" && strings.Contains(task.ID, ":checkpoint:") {
		return runner.processDependencyGraph(ctx, task.ID, map[string]bool{})
	}
	if len(task.ChildArtifacts) > 0 {
		children := make([]domain.ExpansionDependencyReview, 0, len(task.ChildArtifacts))
		for _, ref := range task.ChildArtifacts {
			loaded, ok := runtime.DependencyReviews[ref.ArtifactID]
			if !ok || loaded.ArtifactSignature != ref.ArtifactSignature {
				return domain.ExpansionDependencyReview{}, fmt.Errorf("durable child dependency artifact %s is missing", ref.ArtifactID)
			}
			children = append(children, loaded)
		}
		task.ChildReviews = children
	}
	return runner.ReviewDependency(ctx, task)
}

func loadOrCreatePrivateKey(projectRoot string) (ed25519.PrivateKey, error) {
	path := filepath.Join(projectRoot, filepath.FromSlash(identityFile))
	payload, err := os.ReadFile(path)
	if err == nil {
		var identity privateIdentity
		if json.Unmarshal(payload, &identity) != nil {
			return nil, fmt.Errorf("load private expansion auditor identity: malformed payload")
		}
		key, decodeErr := hex.DecodeString(identity.PrivateKeyHex)
		if decodeErr != nil || len(key) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("load private expansion auditor identity: invalid private key")
		}
		return ed25519.PrivateKey(key), nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	payload, err = json.Marshal(privateIdentity{PrivateKeyHex: hex.EncodeToString(privateKey)})
	if err != nil {
		return nil, err
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return nil, err
	}
	if err := os.Rename(temporary, path); err != nil {
		_ = os.Remove(temporary)
		return nil, err
	}
	return privateKey, nil
}

func signArtifact(privateKey ed25519.PrivateKey, artifact host.ExpansionAuditArtifact) string {
	artifact.Signature = ""
	payload, _ := json.Marshal(artifact)
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
}

func signDependencyReview(privateKey ed25519.PrivateKey, review domain.ExpansionDependencyReview) string {
	review.ArtifactSignature = ""
	payload, _ := json.Marshal(review)
	return base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
}
