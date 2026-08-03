package store

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

const (
	expansionPublicationReceiptVersion = 1
	expansionPublicationReceiptFile    = "meta/revisions/expansion-publication-receipt.json"
	expansionPublicationTrustFile      = "meta/revisions/expansion-publication-trust.json"
	expansionPublicationPrivateKeyFile = "meta/runtime/expansion-publication-authority.json"
	expansionPublicationLineageFile    = "meta/revisions/expansion-publication-lineage.json"
)

// ExpansionPublicationTrust is public verification material. Validation
// clones copy this file, but never the corresponding private signing key.
type ExpansionPublicationTrust struct {
	Version         int    `json:"version"`
	Algorithm       string `json:"algorithm"`
	ProjectID       string `json:"project_id"`
	ProjectInstance string `json:"project_instance"`
	ProjectRootHash string `json:"project_root_hash"`
	KeyID           string `json:"key_id"`
	Epoch           uint64 `json:"epoch"`
	RootKeyID       string `json:"root_key_id"`
	PublicKey       string `json:"public_key"`
	Certificate     string `json:"certificate"`
}

type ExpansionPublicationArtifactBinding struct {
	ArtifactID       string `json:"artifact_id"`
	ArtifactKind     string `json:"artifact_kind"`
	VersionID        string `json:"version_id"`
	ContentSignature string `json:"content_signature"`
}

type ExpansionPublicationChapterBinding struct {
	StableID          string                          `json:"stable_id"`
	Origin            domain.ExpansionOrigin          `json:"origin"`
	Facts             domain.ExpansionDramaticFactSet `json:"facts"`
	FactsSignature    string                          `json:"facts_signature"`
	ContractSignature string                          `json:"contract_signature"`
}

// ExpansionPublicationReceipt is the signed, clone-safe publication lineage
// for the exact current formal structure. It intentionally contains no lease,
// fence, token, idempotency key, or other live capability.
type ExpansionPublicationReceipt struct {
	Version                  int                                   `json:"version"`
	ProjectID                string                                `json:"project_id"`
	ProjectInstance          string                                `json:"project_instance"`
	KeyID                    string                                `json:"key_id"`
	KeyEpoch                 uint64                                `json:"key_epoch"`
	Mode                     domain.RevisionMode                   `json:"mode"`
	PolicyID                 string                                `json:"policy_id"`
	PolicyVersion            string                                `json:"policy_version"`
	SessionID                string                                `json:"session_id"`
	SessionRevision          int                                   `json:"session_revision"`
	PublicationGeneration    uint64                                `json:"publication_generation"`
	AcceptedRevisionID       string                                `json:"accepted_revision_id"`
	AcceptedVersionSignature string                                `json:"accepted_version_signature"`
	AcceptedVersionIDs       []string                              `json:"accepted_version_ids"`
	CurrentArtifacts         []ExpansionPublicationArtifactBinding `json:"current_artifacts"`
	StructureArtifactKind    string                                `json:"structure_artifact_kind"`
	StructureSchemaVersion   int                                   `json:"structure_schema_version"`
	StructureSignature       string                                `json:"structure_signature"`
	PreviewSignature         string                                `json:"preview_signature"`
	AdaptationServiceBinding string                                `json:"adaptation_service_binding,omitempty"`
	Chapters                 []ExpansionPublicationChapterBinding  `json:"chapters"`
	PublishedAt              string                                `json:"published_at"`
	Signature                string                                `json:"signature"`
}

type ExpansionPublicationCloneLineage struct {
	Version               int    `json:"version"`
	Purpose               string `json:"purpose"`
	SourceProjectID       string `json:"source_project_id"`
	SourceProjectInstance string `json:"source_project_instance"`
	SourceRootHash        string `json:"source_root_hash"`
	SourceReceiptDigest   string `json:"source_receipt_digest"`
	SourceKeyID           string `json:"source_key_id"`
	SourceKeyEpoch        uint64 `json:"source_key_epoch"`
	CloneAnonymousID      string `json:"clone_anonymous_id"`
	CloneManifestDigest   string `json:"clone_manifest_digest"`
	IssuedAt              string `json:"issued_at"`
	Nonce                 string `json:"nonce"`
	Signature             string `json:"signature"`
}

type publicationProjectManifest struct {
	Version        int        `json:"version"`
	ID             string     `json:"id"`
	Name           string     `json:"name"`
	RootDir        string     `json:"root_dir"`
	OutputDir      string     `json:"output_dir"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastAccessedAt time.Time  `json:"last_accessed_at"`
	DeletedAt      *time.Time `json:"deleted_at,omitempty"`
	ClonedFromID   string     `json:"cloned_from_id,omitempty"`
	ClonedAt       *time.Time `json:"cloned_at,omitempty"`
}

func (s *RevisionStore) writeExpansionPublicationReceipt(state *revisionState, session domain.RevisionSession) error {
	return s.withRevisionTransaction(func() error {
		return s.writeExpansionPublicationReceiptOwned(state, session)
	})
}

func (s *RevisionStore) writeExpansionPublicationReceiptOwned(state *revisionState, session domain.RevisionSession) error {
	if state == nil || session.Stage != domain.RevisionStageCompleted || !validExpansionPublicationPolicy(session) {
		return fmt.Errorf("expansion publication receipt requires a completed supported revision")
	}
	volumes, artifactKind, err := loadPublicationFormalStructure(s.io, session.Mode)
	if err != nil {
		return err
	}
	projectIdentity, err := capturePublicationProjectIdentity(s.io.dir)
	if err != nil {
		return err
	}
	projectID := projectIdentity.Manifest.ID
	checkpoint := currentPublicationCheckpoint(s.io)
	return withExpansionAuthorityRootOperation(func() error {
		trust, privateKey, err := s.loadOrCreateExpansionPublicationAuthorityLocked(projectID)
		if err != nil {
			return err
		}
		receipt := ExpansionPublicationReceipt{
			Version: expansionPublicationReceiptVersion, ProjectID: trust.ProjectID,
			ProjectInstance: trust.ProjectInstance, KeyID: trust.KeyID, KeyEpoch: trust.Epoch,
			Mode: session.Mode, PolicyID: session.PolicyID, PolicyVersion: session.PolicyVersion,
			SessionID: session.ID, SessionRevision: session.Revision,
			PublicationGeneration: state.Generation, AcceptedRevisionID: session.ID,
			AcceptedVersionIDs:    append(append([]string(nil), session.AcceptedVersionIDs...), session.CandidateVersionIDs...),
			StructureArtifactKind: artifactKind, StructureSchemaVersion: validationStructureSchemaVersion,
			StructureSignature: domain.StructureSignature(volumes), PreviewSignature: session.PreviewSignature,
			PublishedAt: session.CompletedAt,
		}
		if checkpoint != nil && checkpoint.Mode == session.Mode && checkpoint.AcceptedRevisionID == session.ID {
			receipt.AcceptedVersionSignature = checkpoint.AcceptedVersionSignature
		}
		receipt.CurrentArtifacts, err = publicationArtifactBindings(state)
		if err != nil {
			return err
		}
		receipt.Chapters, err = publicationChapterBindings(volumes)
		if err != nil {
			return err
		}
		if session.Mode == domain.RevisionModeAdaptation {
			receipt.AdaptationServiceBinding, err = adaptationPublicationServiceBindingForCommit(state, session, receipt)
			if err != nil {
				return err
			}
		}
		payload, err := expansionPublicationSigningPayload(receipt)
		if err != nil {
			return err
		}
		receipt.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
		if err := projectIdentity.Verify(); err != nil {
			return fmt.Errorf("publication project identity changed before signing: %w", err)
		}
		if err := s.io.WriteJSON(expansionPublicationReceiptFile, receipt); err != nil {
			return err
		}
		if err := projectIdentity.Verify(); err != nil {
			_ = s.io.RemoveFile(expansionPublicationReceiptFile)
			return fmt.Errorf("publication project identity changed while signing: %w", err)
		}
		return nil
	})
}

const validationStructureSchemaVersion = 1

func loadPublicationFormalStructure(io *IO, mode domain.RevisionMode) ([]domain.VolumeOutline, string, error) {
	if mode == domain.RevisionModeAdaptation {
		var plan domain.AdaptationPlan
		if err := io.ReadJSON(adaptationPlanFile, &plan); err != nil {
			return nil, "", fmt.Errorf("load formal adaptation plan for publication receipt: %w", err)
		}
		volumes := adaptationPublicationStructure(plan)
		if len(volumes) == 0 || len(domain.FlattenOutline(volumes)) == 0 {
			return nil, "", fmt.Errorf("formal adaptation plan is empty")
		}
		return volumes, "adaptation_plan", nil
	}
	var volumes []domain.VolumeOutline
	if err := io.ReadJSON("layered_outline.json", &volumes); err != nil {
		return nil, "", fmt.Errorf("load formal structure for publication receipt: %w", err)
	}
	if err := domain.ValidateStructureSnapshot(volumes); err != nil {
		return nil, "", fmt.Errorf("validate formal structure for publication receipt: %w", err)
	}
	return volumes, "layered_outline", nil
}

func adaptationPublicationStructure(plan domain.AdaptationPlan) []domain.VolumeOutline {
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
		result = append(result, domain.VolumeOutline{
			ID: volume.ID, Index: volume.Index, Title: volume.Title, Theme: volume.Theme,
			Arcs: []domain.ArcOutline{{ID: volume.ID + ":dramatic-bindings", Index: 1, Chapters: byVolume[volume.ID]}},
		})
	}
	return result
}

func currentPublicationCheckpoint(io *IO) *domain.CompletionRevalidationCheckpoint {
	var progress domain.Progress
	if err := io.ReadJSON("meta/progress.json", &progress); err != nil || progress.CompletionRevalidation == nil {
		return nil
	}
	checkpoint := *progress.CompletionRevalidation
	return &checkpoint
}

func publicationArtifactBindings(state *revisionState) ([]ExpansionPublicationArtifactBinding, error) {
	ids := make([]string, 0, len(state.CurrentArtifacts))
	for artifactID := range state.CurrentArtifacts {
		ids = append(ids, artifactID)
	}
	sort.Strings(ids)
	bindings := make([]ExpansionPublicationArtifactBinding, 0, len(ids))
	for _, artifactID := range ids {
		versionID := state.CurrentArtifacts[artifactID]
		version, ok := state.Versions[versionID]
		if !ok || version.ArtifactID != artifactID || len(version.ContentSignature) != 64 {
			return nil, fmt.Errorf("current publication artifact %q is invalid", artifactID)
		}
		bindings = append(bindings, ExpansionPublicationArtifactBinding{
			ArtifactID: artifactID, ArtifactKind: version.ArtifactKind,
			VersionID: version.ID, ContentSignature: version.ContentSignature,
		})
	}
	return bindings, nil
}

func publicationChapterBindings(volumes []domain.VolumeOutline) ([]ExpansionPublicationChapterBinding, error) {
	bindings := make([]ExpansionPublicationChapterBinding, 0)
	for _, chapter := range domain.FlattenOutline(volumes) {
		if chapter.ExpansionOrigin == nil {
			continue
		}
		if chapter.DramaticFacts == nil || chapter.ExpansionOrigin.Validate(chapter.DramaticFacts) != nil {
			return nil, fmt.Errorf("formal expansion chapter %q is incomplete", chapter.ID)
		}
		bindings = append(bindings, ExpansionPublicationChapterBinding{
			StableID: chapter.ID, Origin: *chapter.ExpansionOrigin, Facts: *chapter.DramaticFacts,
			FactsSignature:    domain.ExpansionDramaticFactsSignature(*chapter.DramaticFacts),
			ContractSignature: chapter.ExpansionOrigin.DramaticContractSignature,
		})
	}
	return bindings, nil
}

func publicationProjectID(outputDir string) (string, error) {
	identity, err := capturePublicationProjectIdentity(outputDir)
	if err != nil {
		return "", err
	}
	return identity.Manifest.ID, identity.Verify()
}

type publicationProjectIdentity struct {
	Manifest    publicationProjectManifest
	Path        string
	Info        os.FileInfo
	FileID      string
	ChangeStamp string
	Digest      string
}

func capturePublicationProjectIdentity(outputDir string) (publicationProjectIdentity, error) {
	path := filepath.Join(filepath.Dir(filepath.Clean(outputDir)), "project.json")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return publicationProjectIdentity{}, fmt.Errorf("publication project manifest is unavailable or unsafe: %w", err)
	}
	fileID, links, err := validationCloneFileIdentity(path, info)
	if err != nil || links != 1 {
		return publicationProjectIdentity{}, fmt.Errorf("publication project manifest identity is unsafe: %w", err)
	}
	changeStamp, err := authoritativeFileChangeStamp(path, info)
	if err != nil {
		return publicationProjectIdentity{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return publicationProjectIdentity{}, err
	}
	var manifest publicationProjectManifest
	if err := decodeExactJSON(data, &manifest); err != nil {
		return publicationProjectIdentity{}, err
	}
	if err := validatePublicationProjectManifest(outputDir, manifest); err != nil {
		return publicationProjectIdentity{}, err
	}
	digest := sha256.Sum256(data)
	identity := publicationProjectIdentity{Manifest: manifest, Path: path, Info: info, FileID: fileID, ChangeStamp: changeStamp, Digest: hex.EncodeToString(digest[:])}
	if err := identity.Verify(); err != nil {
		return publicationProjectIdentity{}, err
	}
	return identity, nil
}

func (identity publicationProjectIdentity) Verify() error {
	info, err := os.Lstat(identity.Path)
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(identity.Info, info) {
		return fmt.Errorf("publication project manifest was replaced")
	}
	fileID, links, identityErr := validationCloneFileIdentity(identity.Path, info)
	changeStamp, stampErr := authoritativeFileChangeStamp(identity.Path, info)
	data, readErr := os.ReadFile(identity.Path)
	digest := sha256.Sum256(data)
	if identityErr != nil || stampErr != nil || readErr != nil || links != 1 || fileID != identity.FileID || changeStamp != identity.ChangeStamp || hex.EncodeToString(digest[:]) != identity.Digest {
		return fmt.Errorf("publication project manifest changed")
	}
	return nil
}

func readPublicationProjectManifest(outputDir string) (publicationProjectManifest, error) {
	identity, err := capturePublicationProjectIdentity(outputDir)
	return identity.Manifest, err
}

func validatePublicationProjectManifest(outputDir string, manifest publicationProjectManifest) error {
	if manifest.Version != 1 || strings.TrimSpace(manifest.ID) == "" || strings.TrimSpace(manifest.Name) == "" ||
		strings.TrimSpace(manifest.RootDir) == "" || strings.TrimSpace(manifest.OutputDir) == "" || manifest.CreatedAt.IsZero() || manifest.DeletedAt != nil {
		return fmt.Errorf("publication project manifest is incomplete")
	}
	actualOutput, err := canonicalPublicationPath(outputDir)
	if err != nil {
		return err
	}
	manifestOutput, err := canonicalPublicationPath(manifest.OutputDir)
	if err != nil {
		return err
	}
	actualRoot, err := canonicalPublicationPath(filepath.Dir(filepath.Clean(outputDir)))
	if err != nil {
		return err
	}
	manifestRoot, err := canonicalPublicationPath(manifest.RootDir)
	if err != nil {
		return err
	}
	if !samePublicationPath(actualOutput, manifestOutput) || !samePublicationPath(actualRoot, manifestRoot) {
		return fmt.Errorf("publication project manifest root/output binding is invalid")
	}
	return nil
}

func canonicalPublicationPath(path string) (string, error) {
	abs, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(resolved), nil
}

func samePublicationPath(left, right string) bool {
	if runtime.GOOS == "windows" {
		return strings.EqualFold(left, right)
	}
	return left == right
}

func (s *RevisionStore) loadOrCreateExpansionPublicationAuthority(projectID string) (ExpansionPublicationTrust, ed25519.PrivateKey, error) {
	var trust ExpansionPublicationTrust
	var privateKey ed25519.PrivateKey
	err := withExpansionAuthorityRootOperation(func() error {
		var err error
		trust, privateKey, err = s.loadOrCreateExpansionPublicationAuthorityLocked(projectID)
		return err
	})
	return trust, privateKey, err
}

func (s *RevisionStore) loadOrCreateExpansionPublicationAuthorityLocked(projectID string) (ExpansionPublicationTrust, ed25519.PrivateKey, error) {
	var existing ExpansionPublicationTrust
	if err := s.io.ReadJSON(expansionPublicationTrustFile, &existing); err == nil && existing.ProjectID == projectID {
		if err := verifyExpansionPublicationTrustLocked(existing); err == nil {
			privateKey, loadErr := loadExpansionProjectPrivate(existing)
			return existing, privateKey, loadErr
		}
	}
	if err := recoverExpansionAuthorityCreationForOutputLocked(s.io.dir); err != nil {
		return ExpansionPublicationTrust{}, nil, err
	}
	trust, privateKey, err := createExpansionProjectAuthorityLocked(projectID, s.io.dir)
	if err != nil {
		return trust, nil, err
	}
	if err := s.io.WriteJSON(expansionPublicationTrustFile, trust); err != nil {
		_ = recoverExpansionAuthorityCreationForOutputLocked(s.io.dir)
		return ExpansionPublicationTrust{}, nil, err
	}
	// Legacy project-local private material is never trusted after migration.
	if err := s.io.RemoveFile(expansionPublicationPrivateKeyFile); err != nil {
		_ = recoverExpansionAuthorityCreationForOutputLocked(s.io.dir)
		return ExpansionPublicationTrust{}, nil, err
	}
	return trust, privateKey, nil
}

func createExpansionProjectAuthority(projectID, outputDir string) (ExpansionPublicationTrust, ed25519.PrivateKey, error) {
	if err := recoverExpansionAuthorityCreationForOutput(outputDir); err != nil {
		return ExpansionPublicationTrust{}, nil, err
	}
	var trust ExpansionPublicationTrust
	var privateKey ed25519.PrivateKey
	err := withExpansionAuthorityRootOperation(func() error {
		var err error
		trust, privateKey, err = createExpansionProjectAuthorityLocked(projectID, outputDir)
		return err
	})
	return trust, privateKey, err
}

// createExpansionProjectAuthorityLocked is called only after the caller owns
// the project revision transaction and the release-root OS operation lock.
func createExpansionProjectAuthorityLocked(projectID, outputDir string) (ExpansionPublicationTrust, ed25519.PrivateKey, error) {
	rootSecret, err := loadOrCreateExpansionAuthorityRootSecretLocked()
	if err != nil {
		return ExpansionPublicationTrust{}, nil, err
	}
	rootPrivate, err := decodeExpansionAuthorityRootSecret(rootSecret)
	if err != nil {
		return ExpansionPublicationTrust{}, nil, err
	}
	root := expansionAuthorityRootRecord{Version: rootSecret.Version, KeyID: rootSecret.KeyID, PublicKey: rootSecret.PublicKey}
	instanceBytes := make([]byte, 32)
	if _, err := rand.Read(instanceBytes); err != nil {
		return ExpansionPublicationTrust{}, nil, err
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return ExpansionPublicationTrust{}, nil, err
	}
	keyDigest := sha256.Sum256(publicKey)
	rootHash, err := expansionPublicationRootHash(outputDir)
	if err != nil {
		return ExpansionPublicationTrust{}, nil, err
	}
	trust := ExpansionPublicationTrust{
		Version: expansionPublicationReceiptVersion, Algorithm: expansionAuthorityAlgorithm,
		ProjectID: projectID, ProjectInstance: hex.EncodeToString(instanceBytes),
		ProjectRootHash: rootHash,
		KeyID:           "project-" + hex.EncodeToString(keyDigest[:12]), Epoch: 1,
		RootKeyID: root.KeyID, PublicKey: base64.StdEncoding.EncodeToString(publicKey),
	}
	payload, err := expansionPublicationTrustPayload(trust)
	if err != nil {
		return trust, nil, err
	}
	trust.Certificate = base64.StdEncoding.EncodeToString(ed25519.Sign(rootPrivate, payload))
	record := expansionAuthorityProjectRecord{
		Version: expansionAuthorityRootVersion, ProjectID: projectID, ProjectInstance: trust.ProjectInstance,
		KeyID: trust.KeyID, Epoch: trust.Epoch, PrivateKey: base64.StdEncoding.EncodeToString(privateKey),
		PublicKey: trust.PublicKey,
	}
	creationID, err := newAuthorityCreationID()
	if err != nil {
		return trust, nil, err
	}
	record.CreationID = creationID
	path, err := authorityProjectRecordPath(trust.ProjectInstance)
	if err != nil {
		return trust, nil, err
	}
	before, err := capturePublicationAuthoritySnapshot(newIO(outputDir))
	if err != nil {
		return trust, nil, err
	}
	journalPath, err := authorityCreationJournalPath(rootHash)
	if err != nil {
		return trust, nil, err
	}
	canonicalOutput, err := canonicalPublicationPath(outputDir)
	if err != nil {
		return trust, nil, err
	}
	journal := expansionAuthorityCreationJournal{
		Version: authorityCreationJournalVersion, OutputDir: canonicalOutput, ProjectRootHash: rootHash, ProjectID: projectID,
		ProjectInstance: trust.ProjectInstance, Before: before, NewRecord: record,
		NewRecordDigest: authorityProjectRecordDigest(record),
		JournalID:       creationID, State: authorityCreationPending, CreatedAtUTC: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
		return trust, nil, err
	}
	if _, err := os.Lstat(path); err == nil {
		_ = os.Remove(journalPath)
		return trust, nil, fmt.Errorf("publication project authority already exists")
	} else if !os.IsNotExist(err) {
		_ = os.Remove(journalPath)
		return trust, nil, err
	}
	if err := writeProtectedAuthorityJSON(path, record); err != nil {
		_ = recoverExpansionAuthorityCreationForOutputLocked(outputDir)
		return trust, nil, err
	}
	return trust, privateKey, nil
}

func expansionPublicationRootHash(outputDir string) (string, error) {
	absolute, err := filepath.Abs(outputDir)
	if err != nil {
		return "", fmt.Errorf("resolve publication root: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve publication root aliases: %w", err)
	}
	canonical, err = filepath.Abs(canonical)
	if err != nil {
		return "", fmt.Errorf("canonicalize publication root: %w", err)
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", fmt.Errorf("inspect publication root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("publication root is not a directory")
	}
	identity, err := publicationRootFileIdentity(canonical, info)
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "windows" {
		canonical = strings.ToLower(canonical)
	}
	return domain.ContentSignature([]byte(filepath.Clean(canonical) + "\x00" + identity)), nil
}

func loadExpansionProjectPrivate(trust ExpansionPublicationTrust) (ed25519.PrivateKey, error) {
	path, err := authorityProjectRecordPath(trust.ProjectInstance)
	if err != nil {
		return nil, err
	}
	var record expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSON(path, &record); err != nil {
		return nil, fmt.Errorf("publication authority private key is unavailable: %w", err)
	}
	privateKey, err := base64.StdEncoding.DecodeString(record.PrivateKey)
	if err != nil || record.Version != expansionAuthorityRootVersion || record.ProjectID != trust.ProjectID ||
		record.ProjectInstance != trust.ProjectInstance || record.KeyID != trust.KeyID || record.Epoch != trust.Epoch ||
		record.PublicKey != trust.PublicKey || record.RevokedBefore > trust.Epoch || len(privateKey) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("publication authority key/certificate binding is invalid")
	}
	return ed25519.PrivateKey(privateKey), nil
}

func expansionPublicationTrustPayload(trust ExpansionPublicationTrust) ([]byte, error) {
	trust.Certificate = ""
	return json.Marshal(trust)
}

func verifyExpansionPublicationTrust(trust ExpansionPublicationTrust) error {
	return withExpansionAuthorityRootLifecycleOperation(false, func(string) error {
		return verifyExpansionPublicationTrustLocked(trust)
	})
}

func verifyExpansionPublicationTrustLocked(trust ExpansionPublicationTrust) error {
	if err := verifyExpansionPublicationCertificateLocked(trust); err != nil {
		return err
	}
	if err := validateExpansionPublicationTrust(trust); err != nil {
		return err
	}
	path, err := authorityProjectRecordPath(trust.ProjectInstance)
	if err != nil {
		return err
	}
	var record expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSON(path, &record); err != nil {
		return fmt.Errorf("trusted project authority registry is unavailable: %w", err)
	}
	if record.ProjectID != trust.ProjectID || record.ProjectInstance != trust.ProjectInstance || record.KeyID != trust.KeyID ||
		record.Epoch != trust.Epoch || record.PublicKey != trust.PublicKey || record.RevokedBefore > trust.Epoch {
		return fmt.Errorf("publication authority key is stale, revoked, or mismatched")
	}
	return nil
}

// VerifyExpansionPublicationCertificate is the public-only verification
// boundary used by packaged runtimes and independent auditors. It never reads
// or decrypts the application root signing secret.
func VerifyExpansionPublicationCertificate(trust ExpansionPublicationTrust) error {
	return withExpansionAuthorityRootLifecycleOperation(false, func(string) error {
		return verifyExpansionPublicationCertificateLocked(trust)
	})
}

func verifyExpansionPublicationCertificateLocked(trust ExpansionPublicationTrust) error {
	root, rootPublic, err := loadExpansionAuthorityRootPublicKeyLocked(trust.RootKeyID)
	if err != nil {
		return err
	}
	certificate, certificateErr := base64.StdEncoding.DecodeString(trust.Certificate)
	payload, payloadErr := expansionPublicationTrustPayload(trust)
	if root.KeyID != trust.RootKeyID || certificateErr != nil || payloadErr != nil || len(certificate) != ed25519.SignatureSize ||
		!ed25519.Verify(rootPublic, payload, certificate) {
		return fmt.Errorf("publication authority certificate is not rooted in the trusted application authority")
	}
	if err := validateExpansionPublicationTrust(trust); err != nil {
		return err
	}
	return nil
}

// RotateExpansionPublicationAuthority atomically re-signs the current public
// receipt with a new project key while the revision journal is quiescent. A
// protected external journal lets startup either finish the exact new pair or
// restore the previous registry and public bytes after interruption.
func (s *RevisionStore) RotateExpansionPublicationAuthority() error {
	if s == nil || s.io == nil {
		return fmt.Errorf("revision store is required")
	}
	return s.withRevisionTransaction(func() error {
		return withExpansionAuthorityRootOperation(func() error {
			state, err := s.loadUnlocked()
			if err != nil {
				return err
			}
			if state.ActiveSessionID != "" || state.Publication != nil || state.CommandFence != nil || state.NormalLease != nil {
				return ErrRevisionCommandInProgress
			}
			var trust ExpansionPublicationTrust
			var receipt ExpansionPublicationReceipt
			if err := s.io.ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
				return err
			}
			if err := s.io.ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
				return err
			}
			if err := verifyExpansionPublicationTrustLocked(trust); err != nil || !verifyExpansionPublicationSignature(trust, receipt) {
				return fmt.Errorf("current publication authority cannot be rotated")
			}
			oldPath, err := authorityProjectRecordPath(trust.ProjectInstance)
			if err != nil {
				return err
			}
			var oldRecord expansionAuthorityProjectRecord
			if err := readProtectedAuthorityJSON(oldPath, &oldRecord); err != nil {
				return err
			}
			publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				return err
			}
			keyDigest := sha256.Sum256(publicKey)
			newTrust := trust
			newTrust.KeyID = "project-" + hex.EncodeToString(keyDigest[:12])
			newTrust.Epoch++
			newTrust.PublicKey = base64.StdEncoding.EncodeToString(publicKey)
			rootSecret, err := loadOrCreateExpansionAuthorityRootSecretLocked()
			if err != nil {
				return err
			}
			rootPrivate, err := decodeExpansionAuthorityRootSecret(rootSecret)
			if err != nil {
				return err
			}
			root := expansionAuthorityRootRecord{Version: rootSecret.Version, KeyID: rootSecret.KeyID, PublicKey: rootSecret.PublicKey}
			newTrust.RootKeyID = root.KeyID
			newTrust.Certificate = ""
			trustPayload, _ := expansionPublicationTrustPayload(newTrust)
			newTrust.Certificate = base64.StdEncoding.EncodeToString(ed25519.Sign(rootPrivate, trustPayload))
			newReceipt := receipt
			newReceipt.KeyID, newReceipt.KeyEpoch, newReceipt.Signature = newTrust.KeyID, newTrust.Epoch, ""
			receiptPayload, _ := expansionPublicationSigningPayload(newReceipt)
			newReceipt.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, receiptPayload))
			newRecord := oldRecord
			newRecord.KeyID, newRecord.Epoch, newRecord.RevokedBefore = newTrust.KeyID, newTrust.Epoch, newTrust.Epoch
			newRecord.PublicKey, newRecord.PrivateKey = newTrust.PublicKey, base64.StdEncoding.EncodeToString(privateKey)
			snapshot, err := capturePublicationAuthoritySnapshot(s.io)
			if err != nil {
				return err
			}
			newTrustData, _ := json.MarshalIndent(newTrust, "", "  ")
			newReceiptData, _ := json.MarshalIndent(newReceipt, "", "  ")
			journal := expansionAuthorityRotationJournal{Version: 1, OutputDir: s.io.dir, OldRecord: oldRecord, NewRecord: newRecord, PublicSnapshot: snapshot, NewTrust: newTrustData, NewReceipt: newReceiptData}
			journalPath, err := authorityRotationJournalPath(trust.ProjectInstance)
			if err != nil {
				return err
			}
			if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
				return err
			}
			rollback := func(cause error) error {
				if recoverErr := recoverExpansionAuthorityRotationLocked(s.io.dir, trust.ProjectInstance); recoverErr != nil {
					return errors.Join(cause, recoverErr)
				}
				return cause
			}
			if err := writeProtectedAuthorityJSON(oldPath, newRecord); err != nil {
				return rollback(err)
			}
			if err := s.io.WriteJSON(expansionPublicationTrustFile, newTrust); err != nil {
				return rollback(err)
			}
			if err := s.io.WriteJSON(expansionPublicationReceiptFile, newReceipt); err != nil {
				return rollback(err)
			}
			if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
				return err
			}
			return nil
		})
	})
}

func recoverExpansionAuthorityRotation(outputDir, instanceID string) error {
	return withExpansionAuthorityRootOperation(func() error {
		return recoverExpansionAuthorityRotationLocked(outputDir, instanceID)
	})
}

func recoverExpansionAuthorityRotationLocked(outputDir, instanceID string) error {
	journalPath, err := authorityRotationJournalPath(instanceID)
	if err != nil {
		return err
	}
	var journal expansionAuthorityRotationJournal
	if err := readProtectedAuthorityJSON(journalPath, &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if journal.Version != 1 || filepath.Clean(journal.OutputDir) != filepath.Clean(outputDir) || journal.OldRecord.ProjectInstance != instanceID || journal.NewRecord.ProjectInstance != instanceID {
		return fmt.Errorf("publication authority rotation journal is invalid")
	}
	io := newIO(outputDir)
	trustData, trustErr := io.ReadFile(expansionPublicationTrustFile)
	receiptData, receiptErr := io.ReadFile(expansionPublicationReceiptFile)
	recordPath, err := authorityProjectRecordPath(instanceID)
	if err != nil {
		return err
	}
	if trustErr == nil && receiptErr == nil && slices.Equal(trustData, journal.NewTrust) && slices.Equal(receiptData, journal.NewReceipt) {
		if err := writeProtectedAuthorityJSON(recordPath, journal.NewRecord); err != nil {
			return err
		}
	} else {
		if err := writeProtectedAuthorityJSON(recordPath, journal.OldRecord); err != nil {
			return err
		}
		if err := restorePublicationAuthoritySnapshotLocked(io, journal.PublicSnapshot); err != nil {
			return err
		}
	}
	if err := os.Remove(journalPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

func recoverExpansionAuthorityRotationForOutput(outputDir string) error {
	return withExpansionAuthorityRootOperation(func() error {
		return recoverExpansionAuthorityRotationForOutputLocked(outputDir)
	})
}

func recoverExpansionAuthorityRotationForOutputLocked(outputDir string) error {
	var trust ExpansionPublicationTrust
	if err := newIO(outputDir).ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if len(trust.ProjectInstance) != 64 {
		return nil
	}
	return recoverExpansionAuthorityRotationLocked(outputDir, trust.ProjectInstance)
}

func recoverExpansionAuthorityCreationForOutput(outputDir string) error {
	return withExpansionAuthorityRootOperation(func() error {
		return recoverExpansionAuthorityCreationForOutputLocked(outputDir)
	})
}

func recoverExpansionAuthorityCreationForOutputLocked(outputDir string) error {
	rootHash, err := expansionPublicationRootHash(outputDir)
	if err != nil {
		return err
	}
	journalPath, err := authorityCreationJournalPath(rootHash)
	if err != nil {
		return err
	}
	var journal expansionAuthorityCreationJournal
	if err := readProtectedAuthorityJSONStrict(journalPath, &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	actualOutput, err := canonicalPublicationPath(outputDir)
	if err != nil {
		return err
	}
	journalOutput, err := canonicalPublicationPath(journal.OutputDir)
	_, validationErr := validateAuthorityCreationJournal(journal, rootHash, false)
	if err != nil || validationErr != nil ||
		!samePublicationPath(actualOutput, journalOutput) {
		return errors.Join(fmt.Errorf("publication authority creation journal is invalid"), err, validationErr)
	}
	if expansionAuthorityCreationCommitted(outputDir, journal) {
		recordPath, err := authorityProjectRecordPath(journal.ProjectInstance)
		if err != nil {
			return err
		}
		var record expansionAuthorityProjectRecord
		if err := readProtectedAuthorityJSONStrict(recordPath, &record); err != nil {
			return err
		}
		if record != journal.NewRecord {
			return fmt.Errorf("committed publication authority record changed during recovery")
		}
		return removeAuthorityJournalAndOutcomeDurably(journalPath, journal)
	}
	newRecordPath, err := authorityProjectRecordPath(journal.ProjectInstance)
	if err != nil {
		return err
	}
	var record expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSONStrict(newRecordPath, &record); err == nil {
		if record != journal.NewRecord {
			return fmt.Errorf("uncommitted publication authority record changed during recovery")
		}
		if err := writeExpansionAuthorityAbortEvidenceLocked(journal); err != nil {
			return fmt.Errorf("persist uncommitted publication authority outcome: %w", err)
		}
		if err := os.Remove(newRecordPath); err != nil {
			return fmt.Errorf("remove uncommitted publication authority record: %w", err)
		}
		if err := syncAuthorityDirectory(filepath.Dir(newRecordPath)); err != nil {
			return fmt.Errorf("persist uncommitted publication authority removal: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := restorePublicationAuthoritySnapshotLocked(newIO(outputDir), journal.Before); err != nil {
		return err
	}
	return cleanupExpansionAuthorityCreationForOutputLocked(outputDir)
}

func expansionAuthorityCreationCommitted(outputDir string, journal expansionAuthorityCreationJournal) bool {
	io := newIO(outputDir)
	var trust ExpansionPublicationTrust
	var receipt ExpansionPublicationReceipt
	var state revisionState
	if io.ReadJSON(expansionPublicationTrustFile, &trust) != nil || io.ReadJSON(expansionPublicationReceiptFile, &receipt) != nil ||
		io.ReadJSON(revisionStateFile, &state) != nil {
		return false
	}
	if trust.ProjectID != journal.ProjectID || trust.ProjectInstance != journal.ProjectInstance ||
		receipt.ProjectID != trust.ProjectID || receipt.ProjectInstance != trust.ProjectInstance ||
		receipt.KeyID != trust.KeyID || receipt.KeyEpoch != trust.Epoch ||
		!verifyExpansionPublicationSignature(trust, receipt) || state.Generation != receipt.PublicationGeneration {
		return false
	}
	session, ok := state.Sessions[receipt.SessionID]
	if !ok || session.ID != receipt.AcceptedRevisionID || session.Revision != receipt.SessionRevision ||
		session.Generation != receipt.PublicationGeneration || session.Stage != domain.RevisionStageCompleted {
		return false
	}
	for _, persisted := range state.Receipts {
		if persisted.Result.ID == session.ID && persisted.Result.Revision == session.Revision &&
			persisted.Result.Generation == session.Generation && persisted.Result.Stage == session.Stage {
			return true
		}
	}
	return false
}

func finalizeExpansionAuthorityCreationForOutput(outputDir string) error {
	return withExpansionAuthorityRootOperation(func() error {
		return finalizeExpansionAuthorityCreationForOutputLocked(outputDir)
	})
}

func expansionAuthorityCreationNeedsFinalize(outputDir string) (bool, error) {
	needed := false
	err := withExpansionAuthorityRootOperation(func() error {
		rootHash, err := expansionPublicationRootHash(outputDir)
		if err != nil {
			return err
		}
		path, err := authorityCreationJournalPath(rootHash)
		if err != nil {
			return err
		}
		var journal expansionAuthorityCreationJournal
		if err := readProtectedAuthorityJSONStrict(path, &journal); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if _, err := validateAuthorityCreationJournal(journal, rootHash, false); err != nil {
			return err
		}
		needed = true
		return nil
	})
	return needed, err
}

func finalizeExpansionAuthorityCreationForOutputLocked(outputDir string) error {
	rootHash, err := expansionPublicationRootHash(outputDir)
	if err != nil {
		return err
	}
	path, err := authorityCreationJournalPath(rootHash)
	if err != nil {
		return err
	}
	var journal expansionAuthorityCreationJournal
	if err := readProtectedAuthorityJSONStrict(path, &journal); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !expansionAuthorityCreationCommitted(outputDir, journal) {
		return fmt.Errorf("publication authority creation is not durably committed")
	}
	if journal.Version == authorityCreationJournalVersion {
		// This independently signed, release-root-resident evidence is written
		// before the journal transition. A crash or finalize error can therefore
		// never make GC confuse a committed record with an uncommitted orphan.
		if err := writeExpansionAuthorityAcceptanceEvidenceLocked(outputDir, journal); err != nil {
			return err
		}
		journal.State = authorityCreationAccepted
		if err := writeProtectedAuthorityJSON(path, journal); err != nil {
			return err
		}
	}
	return removeAuthorityJournalAndOutcomeDurably(path, journal)
}

func cleanupExpansionAuthorityCreationForOutput(outputDir string) error {
	return withExpansionAuthorityRootOperation(func() error {
		return cleanupExpansionAuthorityCreationForOutputLocked(outputDir)
	})
}

func cleanupExpansionAuthorityCreationForOutputLocked(outputDir string) error {
	rootHash, err := expansionPublicationRootHash(outputDir)
	if err != nil {
		return err
	}
	path, err := authorityCreationJournalPath(rootHash)
	if err != nil {
		return err
	}
	return removeAuthorityJournalDurably(path)
}

func expansionPublicationSigningPayload(receipt ExpansionPublicationReceipt) ([]byte, error) {
	receipt.Signature = ""
	return json.Marshal(receipt)
}

func verifyExpansionPublicationSignature(trust ExpansionPublicationTrust, receipt ExpansionPublicationReceipt) bool {
	publicKey, keyErr := base64.StdEncoding.DecodeString(trust.PublicKey)
	signature, signatureErr := base64.StdEncoding.DecodeString(receipt.Signature)
	payload, payloadErr := expansionPublicationSigningPayload(receipt)
	return keyErr == nil && signatureErr == nil && payloadErr == nil && len(publicKey) == ed25519.PublicKeySize &&
		len(signature) == ed25519.SignatureSize && ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature)
}

func (s *Store) acceptedExpansionSources(checkpoint *domain.CompletionRevalidationCheckpoint, volumes []domain.VolumeOutline) (map[string][]acceptedExpansionSource, error) {
	sources, verifier, err := s.acceptedExpansionSourcesForAudit(checkpoint, volumes)
	if err != nil {
		return nil, err
	}
	if verifier != nil {
		if err := verifier(); err != nil {
			return nil, fmt.Errorf("validation clone changed during fresh audit: %w", err)
		}
	}
	return sources, nil
}

func (s *Store) acceptedExpansionSourcesForAudit(checkpoint *domain.CompletionRevalidationCheckpoint, volumes []domain.VolumeOutline) (map[string][]acceptedExpansionSource, func() error, error) {
	if checkpoint == nil || checkpoint.Mode == "" || strings.TrimSpace(checkpoint.AcceptedRevisionID) == "" || len(checkpoint.AcceptedVersionSignature) != 64 {
		return nil, nil, fmt.Errorf("accepted expansion publication checkpoint is incomplete")
	}
	var trust ExpansionPublicationTrust
	var receipt ExpansionPublicationReceipt
	if err := newIO(s.dir).ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
		return nil, nil, fmt.Errorf("load expansion publication trust: %w", err)
	}
	if err := newIO(s.dir).ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
		return nil, nil, fmt.Errorf("load expansion publication receipt: %w", err)
	}
	if err := verifyExpansionPublicationTrust(trust); err != nil || validateExpansionPublicationReceipt(receipt) != nil ||
		trust.ProjectID != receipt.ProjectID || trust.ProjectInstance != receipt.ProjectInstance || trust.KeyID != receipt.KeyID || trust.Epoch != receipt.KeyEpoch || !verifyExpansionPublicationSignature(trust, receipt) {
		return nil, nil, fmt.Errorf("expansion publication receipt signature is invalid")
	}
	projectMatches, projectErr := publicationProjectMatches(s.dir, receipt.ProjectID)
	if projectErr != nil {
		return nil, nil, fmt.Errorf("load expansion publication project identity: %w", projectErr)
	}
	if !projectMatches {
		return nil, nil, fmt.Errorf("expansion publication receipt belongs to another project")
	}
	currentRootHash, hashErr := expansionPublicationRootHash(s.dir)
	if hashErr != nil {
		return nil, nil, hashErr
	}
	var cloneSnapshotVerifier func() error
	if trust.ProjectRootHash != currentRootHash {
		verifyCloneSnapshot, err := validateExpansionCloneLineage(s.dir, trust, receipt)
		if err != nil {
			return nil, nil, fmt.Errorf("expansion publication authority belongs to another project instance: %w", err)
		}
		cloneSnapshotVerifier = verifyCloneSnapshot
	}
	if receipt.Mode != checkpoint.Mode || receipt.SessionID != checkpoint.AcceptedRevisionID ||
		receipt.AcceptedRevisionID != checkpoint.AcceptedRevisionID || receipt.AcceptedVersionSignature != checkpoint.AcceptedVersionSignature ||
		receipt.StructureSignature != checkpoint.CurrentStructureSignature || receipt.StructureSignature != domain.StructureSignature(volumes) {
		return nil, nil, fmt.Errorf("expansion publication receipt is stale or cross-mode (receipt mode=%s revision=%s version=%s structure=%s; checkpoint mode=%s revision=%s version=%s structure=%s; formal=%s)", receipt.Mode, receipt.SessionID, receipt.AcceptedVersionSignature, receipt.StructureSignature, checkpoint.Mode, checkpoint.AcceptedRevisionID, checkpoint.AcceptedVersionSignature, checkpoint.CurrentStructureSignature, domain.StructureSignature(volumes))
	}
	state, err := s.Revisions.loadUnlocked()
	if err != nil {
		return nil, nil, err
	}
	session, ok := state.Sessions[receipt.SessionID]
	if !ok || session.Stage != domain.RevisionStageCompleted || session.Mode != receipt.Mode || session.PolicyID != receipt.PolicyID ||
		session.PolicyVersion != receipt.PolicyVersion || session.Revision != receipt.SessionRevision || session.Generation != receipt.PublicationGeneration ||
		session.PreviewSignature != receipt.PreviewSignature || !slices.Equal(append(append([]string(nil), session.AcceptedVersionIDs...), session.CandidateVersionIDs...), receipt.AcceptedVersionIDs) {
		return nil, nil, fmt.Errorf("expansion publication receipt revision binding is invalid")
	}
	current, err := publicationArtifactBindings(state)
	if err != nil || !slices.Equal(current, receipt.CurrentArtifacts) {
		return nil, nil, fmt.Errorf("expansion publication receipt current-artifact binding is stale")
	}
	formalBindings, err := publicationChapterBindings(volumes)
	if err != nil || !slices.Equal(formalBindings, receipt.Chapters) {
		return nil, nil, fmt.Errorf("expansion publication receipt formal provenance binding is stale")
	}
	sources := make(map[string][]acceptedExpansionSource, len(receipt.Chapters))
	for _, chapter := range receipt.Chapters {
		sources[chapter.StableID] = []acceptedExpansionSource{{
			RevisionID: receipt.SessionID, Mode: receipt.Mode, PreviewSignature: receipt.PreviewSignature,
			Origin: chapter.Origin, Facts: chapter.Facts,
		}}
	}
	return sources, cloneSnapshotVerifier, nil
}

func publicationProjectMatches(outputDir, receiptProjectID string) (bool, error) {
	manifest, err := readPublicationProjectManifest(outputDir)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(manifest.ID) == "" {
		return false, fmt.Errorf("publication project identity is empty")
	}
	return strings.TrimSpace(manifest.ID) == receiptProjectID || strings.TrimSpace(manifest.ClonedFromID) == receiptProjectID, nil
}

func validateExpansionPublicationTrust(trust ExpansionPublicationTrust) error {
	key, err := base64.StdEncoding.DecodeString(trust.PublicKey)
	if trust.Version != expansionPublicationReceiptVersion || trust.Algorithm != expansionAuthorityAlgorithm || strings.TrimSpace(trust.ProjectID) == "" ||
		len(trust.ProjectInstance) != 64 || len(trust.ProjectRootHash) != 64 || strings.TrimSpace(trust.KeyID) == "" || trust.Epoch == 0 || strings.TrimSpace(trust.RootKeyID) == "" ||
		strings.TrimSpace(trust.Certificate) == "" || err != nil || len(key) != ed25519.PublicKeySize {
		return fmt.Errorf("expansion publication trust is invalid")
	}
	return nil
}

func validateExpansionPublicationReceipt(receipt ExpansionPublicationReceipt) error {
	if receipt.Version != expansionPublicationReceiptVersion || strings.TrimSpace(receipt.ProjectID) == "" || len(receipt.ProjectInstance) != 64 ||
		strings.TrimSpace(receipt.KeyID) == "" || receipt.KeyEpoch == 0 || receipt.Mode == "" ||
		strings.TrimSpace(receipt.PolicyID) == "" || strings.TrimSpace(receipt.PolicyVersion) == "" || strings.TrimSpace(receipt.SessionID) == "" ||
		receipt.SessionRevision <= 0 || receipt.PublicationGeneration == 0 || receipt.AcceptedRevisionID != receipt.SessionID ||
		(receipt.StructureArtifactKind != "layered_outline" && receipt.StructureArtifactKind != "adaptation_plan") || receipt.StructureSchemaVersion != validationStructureSchemaVersion ||
		len(receipt.StructureSignature) != 64 || strings.TrimSpace(receipt.PublishedAt) == "" || strings.TrimSpace(receipt.Signature) == "" ||
		receipt.AcceptedVersionIDs == nil || receipt.CurrentArtifacts == nil || receipt.Chapters == nil {
		return fmt.Errorf("expansion publication receipt is incomplete")
	}
	for _, binding := range receipt.CurrentArtifacts {
		if binding.ArtifactID == "" || binding.ArtifactKind == "" || binding.VersionID == "" || len(binding.ContentSignature) != 64 {
			return fmt.Errorf("expansion publication artifact binding is invalid")
		}
	}
	for _, chapter := range receipt.Chapters {
		if chapter.StableID == "" || chapter.Origin.Validate(&chapter.Facts) != nil || chapter.FactsSignature != domain.ExpansionDramaticFactsSignature(chapter.Facts) || chapter.ContractSignature != chapter.Origin.DramaticContractSignature {
			return fmt.Errorf("expansion publication chapter binding is invalid")
		}
	}
	return nil
}

// ValidateExpansionPublicationTrustForClone enforces an exact public-trust
// schema without requiring or accepting private signing material.
func ValidateExpansionPublicationTrustForClone(data []byte) error {
	var trust ExpansionPublicationTrust
	if err := decodeExactJSON(data, &trust); err != nil {
		return err
	}
	return validateExpansionPublicationTrust(trust)
}

// ValidateExpansionPublicationReceiptForClone enforces an exact, capability-
// free public receipt schema. Fresh completion audit performs cross-file
// signature and current-artifact verification after cloning.
func ValidateExpansionPublicationReceiptForClone(data []byte) error {
	var receipt ExpansionPublicationReceipt
	if err := decodeExactJSON(data, &receipt); err != nil {
		return err
	}
	return validateExpansionPublicationReceipt(receipt)
}

// ValidateExpansionPublicationAuthorityForClone verifies that the two public
// files form one project-bound signature chain before either is copied.
func ValidateExpansionPublicationAuthorityForClone(trustData, receiptData []byte, expectedProjectID, expectedOutputDir string) error {
	var trust ExpansionPublicationTrust
	var receipt ExpansionPublicationReceipt
	if err := decodeExactJSON(trustData, &trust); err != nil {
		return err
	}
	if err := decodeExactJSON(receiptData, &receipt); err != nil {
		return err
	}
	if err := verifyExpansionPublicationTrust(trust); err != nil {
		return err
	}
	if err := validateExpansionPublicationReceipt(receipt); err != nil {
		return err
	}
	expectedRootHash, err := expansionPublicationRootHash(expectedOutputDir)
	if err != nil {
		return err
	}
	if trust.ProjectID != receipt.ProjectID || trust.ProjectInstance != receipt.ProjectInstance || trust.KeyID != receipt.KeyID || trust.Epoch != receipt.KeyEpoch ||
		strings.TrimSpace(expectedProjectID) != receipt.ProjectID || trust.ProjectRootHash != expectedRootHash || !verifyExpansionPublicationSignature(trust, receipt) {
		return fmt.Errorf("expansion publication public authority binding is invalid")
	}
	return nil
}

func decodeExactJSON(data []byte, target any) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return ensureSingleJSONValue(decoder)
}

func CreateExpansionPublicationCloneLineage(sourceOutputDir, anonymousID string, manifestData []byte) ([]byte, error) {
	io := newIO(sourceOutputDir)
	var trust ExpansionPublicationTrust
	var receipt ExpansionPublicationReceipt
	if err := io.ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
		return nil, err
	}
	if err := io.ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
		return nil, err
	}
	sourceRootHash, hashErr := expansionPublicationRootHash(sourceOutputDir)
	if hashErr != nil {
		return nil, hashErr
	}
	if err := verifyExpansionPublicationTrust(trust); err != nil || trust.ProjectRootHash != sourceRootHash ||
		trust.ProjectID != receipt.ProjectID || trust.ProjectInstance != receipt.ProjectInstance || trust.KeyID != receipt.KeyID ||
		trust.Epoch != receipt.KeyEpoch || !verifyExpansionPublicationSignature(trust, receipt) {
		return nil, fmt.Errorf("source publication authority is invalid")
	}
	privateKey, err := loadExpansionProjectPrivate(trust)
	if err != nil {
		return nil, err
	}
	receiptPayload, err := json.Marshal(receipt)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, 24)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	lineage := ExpansionPublicationCloneLineage{
		Version: 1, Purpose: "isolated_validation_audit", SourceProjectID: trust.ProjectID,
		SourceProjectInstance: trust.ProjectInstance, SourceRootHash: trust.ProjectRootHash,
		SourceReceiptDigest: domain.ContentSignature(receiptPayload), SourceKeyID: trust.KeyID, SourceKeyEpoch: trust.Epoch,
		CloneAnonymousID: strings.TrimSpace(anonymousID), CloneManifestDigest: domain.ContentSignature(manifestData),
		IssuedAt: domain.RevisionTimestamp(), Nonce: hex.EncodeToString(nonce),
	}
	payload, err := expansionCloneLineagePayload(lineage)
	if err != nil {
		return nil, err
	}
	lineage.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
	return json.MarshalIndent(lineage, "", "  ")
}

func expansionCloneLineagePayload(lineage ExpansionPublicationCloneLineage) ([]byte, error) {
	lineage.Signature = ""
	return json.Marshal(lineage)
}

func validateExpansionCloneLineage(outputDir string, trust ExpansionPublicationTrust, receipt ExpansionPublicationReceipt) (func() error, error) {
	data, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(expansionPublicationLineageFile)))
	if err != nil {
		return nil, fmt.Errorf("signed clone lineage is unavailable: %w", err)
	}
	var lineage ExpansionPublicationCloneLineage
	if err := decodeExactJSON(data, &lineage); err != nil {
		return nil, err
	}
	manifest, err := readPublicationProjectManifest(outputDir)
	if err != nil {
		return nil, err
	}
	receiptPayload, _ := json.Marshal(receipt)
	signature, signatureErr := base64.StdEncoding.DecodeString(lineage.Signature)
	publicKey, keyErr := base64.StdEncoding.DecodeString(trust.PublicKey)
	payload, payloadErr := expansionCloneLineagePayload(lineage)
	if lineage.Version != 1 || lineage.Purpose != "isolated_validation_audit" || lineage.SourceProjectID != trust.ProjectID ||
		lineage.SourceProjectInstance != trust.ProjectInstance || lineage.SourceRootHash != trust.ProjectRootHash ||
		lineage.SourceReceiptDigest != domain.ContentSignature(receiptPayload) || lineage.SourceKeyID != trust.KeyID || lineage.SourceKeyEpoch != trust.Epoch ||
		lineage.CloneAnonymousID != strings.TrimSpace(manifest.ID) || strings.TrimSpace(manifest.ClonedFromID) != trust.ProjectID ||
		len(lineage.CloneManifestDigest) != 64 || len(lineage.Nonce) != 48 || strings.TrimSpace(lineage.IssuedAt) == "" ||
		signatureErr != nil || keyErr != nil || payloadErr != nil || len(signature) != ed25519.SignatureSize || len(publicKey) != ed25519.PublicKeySize ||
		!ed25519.Verify(ed25519.PublicKey(publicKey), payload, signature) {
		return nil, fmt.Errorf("signed clone lineage is invalid")
	}
	return validateExpansionValidationCloneManifestAt(outputDir, lineage, receipt)
}

func ValidateExpansionPublicationLineageForClone(data []byte) error {
	var lineage ExpansionPublicationCloneLineage
	if err := decodeExactJSON(data, &lineage); err != nil {
		return err
	}
	if lineage.Version != 1 || lineage.Purpose != "isolated_validation_audit" || len(lineage.SourceProjectInstance) != 64 ||
		len(lineage.SourceRootHash) != 64 || len(lineage.SourceReceiptDigest) != 64 || lineage.SourceKeyID == "" || lineage.SourceKeyEpoch == 0 ||
		lineage.CloneAnonymousID == "" || len(lineage.CloneManifestDigest) != 64 || lineage.IssuedAt == "" || len(lineage.Nonce) != 48 || lineage.Signature == "" {
		return fmt.Errorf("expansion publication clone lineage is invalid")
	}
	return nil
}

func capturePublicationAuthoritySnapshot(io *IO) (publicationAuthoritySnapshot, error) {
	snapshot := publicationAuthoritySnapshot{Version: 2}
	files := []struct {
		rel    string
		target *publicationAuthorityFileSnapshot
	}{
		{expansionPublicationTrustFile, &snapshot.Trust},
		{expansionPublicationReceiptFile, &snapshot.Receipt},
	}
	for _, file := range files {
		path := io.path(file.rel)
		info, err := os.Stat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return snapshot, err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return snapshot, err
		}
		*file.target = publicationAuthorityFileSnapshot{Exists: true, Data: data, Mode: info.Mode().Perm()}
	}
	if snapshot.Trust.Exists {
		var trust ExpansionPublicationTrust
		if err := json.Unmarshal(snapshot.Trust.Data, &trust); err != nil || len(trust.ProjectInstance) != 64 {
			return snapshot, fmt.Errorf("snapshot publication authority trust is invalid")
		}
		path, err := authorityProjectRecordPath(trust.ProjectInstance)
		if err != nil {
			return snapshot, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return snapshot, fmt.Errorf("snapshot publication authority record: %w", err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return snapshot, err
		}
		snapshot.ExternalRecord = publicationAuthorityExternalRecordSnapshot{
			Exists: true, ProjectInstance: trust.ProjectInstance, Data: data,
			Mode: info.Mode().Perm(), Digest: domain.ContentSignature(data),
		}
	}
	return snapshot, nil
}

func restorePublicationAuthoritySnapshot(io *IO, snapshot publicationAuthoritySnapshot) error {
	if snapshot.Version == 0 {
		return nil
	}
	if !snapshot.ExternalRecord.Exists {
		if _, err := io.ReadFile(expansionPublicationTrustFile); os.IsNotExist(err) {
			return restorePublicationAuthoritySnapshotLocked(io, snapshot)
		} else if err != nil {
			return err
		}
	}
	return withExpansionAuthorityRootOperation(func() error {
		return restorePublicationAuthoritySnapshotLocked(io, snapshot)
	})
}

func restorePublicationAuthoritySnapshotLocked(io *IO, snapshot publicationAuthoritySnapshot) error {
	if snapshot.Version == 0 {
		return nil
	}
	if snapshot.Version != 1 && snapshot.Version != 2 {
		return fmt.Errorf("unsupported publication authority snapshot version %d", snapshot.Version)
	}
	var currentTrust ExpansionPublicationTrust
	currentTrustData, currentTrustErr := io.ReadFile(expansionPublicationTrustFile)
	if currentTrustErr == nil {
		if err := json.Unmarshal(currentTrustData, &currentTrust); err != nil || len(currentTrust.ProjectInstance) != 64 {
			if !snapshot.ExternalRecord.Exists {
				return fmt.Errorf("current publication authority trust is invalid during restore")
			}
			currentTrust = ExpansionPublicationTrust{}
		}
	} else if !os.IsNotExist(currentTrustErr) {
		return currentTrustErr
	}
	if currentTrust.ProjectInstance != "" && currentTrust.ProjectInstance != snapshot.ExternalRecord.ProjectInstance {
		path, err := authorityProjectRecordPath(currentTrust.ProjectInstance)
		if err != nil {
			return err
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove rolled-back publication authority record: %w", err)
		}
	}
	if snapshot.ExternalRecord.Exists {
		if len(snapshot.ExternalRecord.ProjectInstance) != 64 || len(snapshot.ExternalRecord.Data) == 0 ||
			snapshot.ExternalRecord.Digest != domain.ContentSignature(snapshot.ExternalRecord.Data) {
			return fmt.Errorf("publication authority external record snapshot is invalid")
		}
		path, err := authorityProjectRecordPath(snapshot.ExternalRecord.ProjectInstance)
		if err != nil {
			return err
		}
		if err := writeRestrictedAtomic(path, snapshot.ExternalRecord.Data); err != nil {
			return err
		}
		mode := snapshot.ExternalRecord.Mode
		if mode == 0 {
			mode = 0o600
		}
		if err := os.Chmod(path, mode); err != nil {
			return err
		}
	}
	files := []struct {
		rel  string
		file publicationAuthorityFileSnapshot
	}{
		{expansionPublicationTrustFile, snapshot.Trust},
		{expansionPublicationReceiptFile, snapshot.Receipt},
	}
	for _, item := range files {
		if !item.file.Exists {
			if err := io.RemoveFile(item.rel); err != nil {
				return err
			}
			continue
		}
		mode := item.file.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := writeFileAtomicWithMode(io, item.rel, item.file.Data, mode); err != nil {
			return err
		}
	}
	return cleanupExpansionAuthorityCreationForOutputLocked(io.dir)
}

func writeFileAtomicWithMode(io *IO, rel string, data []byte, mode os.FileMode) error {
	path, err := io.safeRelPath(rel)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.OpenFile(path+".authority-restore", os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if os.IsExist(err) {
		_ = os.Remove(path + ".authority-restore")
		tmp, err = os.OpenFile(path+".authority-restore", os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	}
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return io.replaceFile(rel, tmpPath, path)
}
