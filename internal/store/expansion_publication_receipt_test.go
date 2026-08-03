package store

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/voocel/ainovel-cli/internal/domain"
)

func TestExpansionPublicationReceiptRejectsStaleCrossModeAndForgedAuthority(t *testing.T) {
	t.Run("cross mode checkpoint", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		checkpoint.Mode = domain.RevisionModeAdaptation
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("normal publication receipt authorized an adaptation checkpoint")
		}
	})

	t.Run("stale checkpoint revision", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		checkpoint.AcceptedRevisionID = "revision-newer"
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("old completed source authorized a newer checkpoint")
		}
	})

	t.Run("stale checkpoint signature", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		checkpoint.AcceptedVersionSignature = strings.Repeat("f", 64)
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("publication receipt authorized a different accepted signature")
		}
	})

	t.Run("unsigned hand-built state", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		if err := newIO(st.dir).RemoveFile(expansionPublicationReceiptFile); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("internally coherent completed state manufactured publication authority")
		}
	})

	t.Run("receipt signature drift", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		var receipt ExpansionPublicationReceipt
		if err := newIO(st.dir).ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
			t.Fatal(err)
		}
		receipt.PreviewSignature = strings.Repeat("d", 64)
		if err := newIO(st.dir).WriteJSON(expansionPublicationReceiptFile, receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("modified receipt retained publication authority")
		}
	})

	t.Run("current artifact replacement", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		state, err := st.Revisions.loadUnlocked()
		if err != nil {
			t.Fatal(err)
		}
		for artifactID, previousID := range state.CurrentArtifacts {
			previous := state.Versions[previousID]
			previous.ID = "artifact-version-forged-current"
			previous.Sequence++
			previous.Payload = json.RawMessage(`{"unrelated":"payload"}`)
			previous.ContentSignature = domain.JSONContentSignature(previous.Payload)
			state.Versions[previous.ID] = previous
			state.CurrentArtifacts[artifactID] = previous.ID
			break
		}
		if err := st.Revisions.io.WriteJSON(revisionStateFile, state); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("receipt authorized a replacement current artifact")
		}
	})

	t.Run("arbitrary payload injection", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		state, err := st.Revisions.loadUnlocked()
		if err != nil {
			t.Fatal(err)
		}
		injected, err := json.Marshal(map[string]any{"nested": map[string]any{"expansion_origin": volumes[0].Arcs[0].Chapters[0].ExpansionOrigin, "dramatic_facts": volumes[0].Arcs[0].Chapters[0].DramaticFacts}})
		if err != nil {
			t.Fatal(err)
		}
		state.Versions["payload-injection"] = domain.ArtifactVersion{
			ID: "payload-injection", ArtifactID: "unrelated-artifact", ArtifactKind: "unrelated-kind",
			RevisionID: "revision-test", Sequence: 1, Round: 1, Payload: injected,
			ContentSignature: domain.JSONContentSignature(injected), CreatedAt: "2026-07-17T00:00:00Z",
		}
		if err := st.Revisions.io.WriteJSON(revisionStateFile, state); err != nil {
			t.Fatal(err)
		}
		volumes[0].Arcs[0].Chapters[0].ExpansionOrigin = nil
		volumes[0].Arcs[0].Chapters[0].DramaticFacts = nil
		if err := st.Outline.io.WriteJSON("layered_outline.json", volumes); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("origin hidden in an arbitrary version payload manufactured authority")
		}
	})

	t.Run("formal facts rollback", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		volumes[0].Arcs[0].Chapters[0].DramaticFacts.ResultState = "failed"
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("receipt authorized rolled-back formal dramatic facts")
		}
	})

	t.Run("cross project receipt replay", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		manifest := publicationProjectManifest{ID: "different-project"}
		if err := newIO(filepath.Dir(st.dir)).WriteJSON("project.json", manifest); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("publication receipt replayed into another project")
		}
	})
}

func TestExpansionPublicationAuthorityRejectsWholeReplacementAndCrossInstanceReplay(t *testing.T) {
	useExpansionAuthorityRootForTest(t, filepath.Join(t.TempDir(), "authority"))
	t.Run("whole project authority replacement", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		var trust ExpansionPublicationTrust
		var receipt ExpansionPublicationReceipt
		if err := newIO(st.dir).ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
			t.Fatal(err)
		}
		if err := newIO(st.dir).ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
			t.Fatal(err)
		}
		trust.PublicKey = base64.StdEncoding.EncodeToString(publicKey)
		trust.KeyID = "project-attacker"
		trust.Certificate = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, []byte("self-signed")))
		receipt.KeyID = trust.KeyID
		payload, _ := expansionPublicationSigningPayload(receipt)
		receipt.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, payload))
		if err := newIO(st.dir).WriteJSON(expansionPublicationTrustFile, trust); err != nil {
			t.Fatal(err)
		}
		if err := newIO(st.dir).WriteJSON(expansionPublicationReceiptFile, receipt); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("self-consistent replacement trust and receipt bypassed the external root")
		}
	})

	t.Run("same project id at another instance", func(t *testing.T) {
		source, _, _ := expansionAuthorityFixture(t)
		var receipt ExpansionPublicationReceipt
		if err := newIO(source.dir).ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
			t.Fatal(err)
		}
		cloneRoot := t.TempDir()
		cloneOutput := filepath.Join(cloneRoot, "output")
		if err := os.CopyFS(cloneOutput, os.DirFS(source.dir)); err != nil {
			t.Fatal(err)
		}
		if err := newIO(cloneRoot).WriteJSON("project.json", publicationProjectManifest{ID: receipt.ProjectID}); err != nil {
			t.Fatal(err)
		}
		if _, err := NewStore(cloneOutput).RunNormalCompletionAudit(); err == nil {
			t.Fatal("a same-ID different filesystem instance replayed source authority")
		}
	})
}

func TestExpansionPublicationRootIdentityUsesCanonicalPhysicalInstance(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "Project", "output")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	original, err := expansionPublicationRootHash(root)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(parent, "output-alias")
	if err := os.Symlink(root, alias); err == nil {
		aliased, hashErr := expansionPublicationRootHash(alias)
		if hashErr != nil || aliased != original {
			t.Fatalf("filesystem alias identity differs: hash=%s err=%v", aliased, hashErr)
		}
	} else if runtime.GOOS != "windows" {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Dir(root), filepath.Join(parent, "Project-old")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	replacement, err := expansionPublicationRootHash(root)
	if err != nil {
		t.Fatal(err)
	}
	if replacement == original {
		t.Fatal("replacement directory reused the original physical root identity")
	}
	if runtime.GOOS != "windows" {
		upper := filepath.Join(parent, "Case", "output")
		lower := filepath.Join(parent, "case", "output")
		if err := os.MkdirAll(upper, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(lower, 0o755); err != nil {
			t.Fatal(err)
		}
		upperHash, upperErr := expansionPublicationRootHash(upper)
		lowerHash, lowerErr := expansionPublicationRootHash(lower)
		if upperErr != nil || lowerErr != nil || upperHash == lowerHash {
			t.Fatalf("case-sensitive roots collided: upper=%s lower=%s errors=%v/%v", upperHash, lowerHash, upperErr, lowerErr)
		}
	} else {
		caseAlias, caseErr := expansionPublicationRootHash(strings.ToUpper(root))
		if caseErr != nil || caseAlias != replacement {
			t.Fatalf("Windows case alias changed root identity: alias=%s replacement=%s err=%v", caseAlias, replacement, caseErr)
		}
	}
}

func TestExpansionPublicationAuthorityRegistryIsExternalAndProtected(t *testing.T) {
	st, _, _ := expansionAuthorityFixture(t)
	if _, err := os.Stat(filepath.Join(st.dir, filepath.FromSlash(expansionPublicationPrivateKeyFile))); !os.IsNotExist(err) {
		t.Fatalf("project-local private authority remains: %v", err)
	}
	var trust ExpansionPublicationTrust
	if err := newIO(st.dir).ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
		t.Fatal(err)
	}
	path, err := authorityProjectRecordPath(trust.ProjectInstance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("external authority mode=%v err=%v", info.Mode().Perm(), err)
		}
	}
	var record expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSON(path, &record); err != nil || record.KeyID != trust.KeyID || record.Epoch != trust.Epoch {
		t.Fatalf("protected registry record=%+v err=%v", record, err)
	}
}

func TestExpansionPublicationAuthorityEpochRevocationAndInterruptedRotation(t *testing.T) {
	st, checkpoint, volumes := expansionAuthorityFixture(t)
	var trust ExpansionPublicationTrust
	if err := newIO(st.dir).ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
		t.Fatal(err)
	}
	path, err := authorityProjectRecordPath(trust.ProjectInstance)
	if err != nil {
		t.Fatal(err)
	}
	var original expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSON(path, &original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte("interrupted rotation staging bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(path + ".tmp")
	if _, err := st.acceptedExpansionSources(checkpoint, volumes); err != nil {
		t.Fatalf("uncommitted rotation staging invalidated the current epoch: %v", err)
	}
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	rotated := original
	rotated.Epoch++
	rotated.RevokedBefore = rotated.Epoch
	rotated.KeyID = "project-rotated"
	rotated.PublicKey = base64.StdEncoding.EncodeToString(publicKey)
	rotated.PrivateKey = base64.StdEncoding.EncodeToString(privateKey)
	if err := writeProtectedAuthorityJSON(path, rotated); err != nil {
		t.Fatal(err)
	}
	defer writeProtectedAuthorityJSON(path, original)
	if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
		t.Fatal("old trust and receipt replayed after the external epoch advanced")
	}
}

func TestExpansionPublicationAuthorityAtomicRotationAndRollback(t *testing.T) {
	t.Run("success revokes old public pair", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		oldTrust, _ := os.ReadFile(filepath.Join(st.dir, filepath.FromSlash(expansionPublicationTrustFile)))
		oldReceipt, _ := os.ReadFile(filepath.Join(st.dir, filepath.FromSlash(expansionPublicationReceiptFile)))
		if err := st.Revisions.RotateExpansionPublicationAuthority(); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err != nil {
			t.Fatalf("rotated authority did not validate: %v", err)
		}
		if err := os.WriteFile(filepath.Join(st.dir, filepath.FromSlash(expansionPublicationTrustFile)), oldTrust, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(st.dir, filepath.FromSlash(expansionPublicationReceiptFile)), oldReceipt, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err == nil {
			t.Fatal("old receipt replayed after atomic key rotation")
		}
	})

	t.Run("receipt failure restores public and external state", func(t *testing.T) {
		st, checkpoint, volumes := expansionAuthorityFixture(t)
		before, err := capturePublicationAuthoritySnapshot(newIO(st.dir))
		if err != nil {
			t.Fatal(err)
		}
		restore := st.SetExpansionWriteFaultForTesting(func(rel, stage string) error {
			if rel == expansionPublicationReceiptFile && stage == "after_temp_sync" {
				return errors.New("injected rotation receipt failure")
			}
			return nil
		})
		if err := st.Revisions.RotateExpansionPublicationAuthority(); err == nil {
			t.Fatal("rotation receipt failure did not fire")
		}
		restore()
		after, err := capturePublicationAuthoritySnapshot(newIO(st.dir))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(before, after) {
			t.Fatalf("failed rotation changed public authority\nbefore=%+v\nafter=%+v", before, after)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err != nil {
			t.Fatalf("failed rotation did not restore external key registry: %v", err)
		}
	})
}

func TestExpansionPublicationAuthorityRotationJournalRecoversOnStartup(t *testing.T) {
	st, checkpoint, volumes := expansionAuthorityFixture(t)
	var trust ExpansionPublicationTrust
	if err := newIO(st.dir).ReadJSON(expansionPublicationTrustFile, &trust); err != nil {
		t.Fatal(err)
	}
	recordPath, _ := authorityProjectRecordPath(trust.ProjectInstance)
	journalPath, _ := authorityRotationJournalPath(trust.ProjectInstance)
	var current expansionAuthorityProjectRecord
	if err := readProtectedAuthorityJSON(recordPath, &current); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := capturePublicationAuthoritySnapshot(newIO(st.dir))
	trustData, _ := os.ReadFile(filepath.Join(st.dir, filepath.FromSlash(expansionPublicationTrustFile)))
	receiptData, _ := os.ReadFile(filepath.Join(st.dir, filepath.FromSlash(expansionPublicationReceiptFile)))

	t.Run("finish exact applied pair", func(t *testing.T) {
		journal := expansionAuthorityRotationJournal{Version: 1, OutputDir: st.dir, OldRecord: current, NewRecord: current, PublicSnapshot: snapshot, NewTrust: trustData, NewReceipt: receiptData}
		if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
			t.Fatal(err)
		}
		reopened := NewStore(st.dir)
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(journalPath); !os.IsNotExist(err) {
			t.Fatalf("completed rotation journal remains: %v", err)
		}
	})

	t.Run("rollback partial public pair", func(t *testing.T) {
		staged := current
		staged.Epoch++
		staged.RevokedBefore = staged.Epoch
		staged.KeyID = "project-partial-rotation"
		if err := writeProtectedAuthorityJSON(recordPath, staged); err != nil {
			t.Fatal(err)
		}
		journal := expansionAuthorityRotationJournal{Version: 1, OutputDir: st.dir, OldRecord: current, NewRecord: staged, PublicSnapshot: snapshot, NewTrust: []byte("partial-new-trust"), NewReceipt: []byte("partial-new-receipt")}
		if err := writeProtectedAuthorityJSON(journalPath, journal); err != nil {
			t.Fatal(err)
		}
		reopened := NewStore(st.dir)
		if err := reopened.RecoverStructureMigration(); err != nil {
			t.Fatal(err)
		}
		if _, err := st.acceptedExpansionSources(checkpoint, volumes); err != nil {
			t.Fatalf("startup did not restore the previous authority: %v", err)
		}
	})
}

func TestPublicationAuthoritySnapshotRestoresExactBytesAndPermissions(t *testing.T) {
	io := newIO(t.TempDir())
	fixtures := map[string]struct {
		data []byte
		mode os.FileMode
	}{
		expansionPublicationReceiptFile: {[]byte("old-receipt"), 0o644},
	}
	for rel, fixture := range fixtures {
		if err := writeFileAtomicWithMode(io, rel, fixture.data, fixture.mode); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, err := capturePublicationAuthoritySnapshot(io)
	if err != nil {
		t.Fatal(err)
	}
	for rel := range fixtures {
		if err := io.WriteFileUnlocked(rel, []byte("replacement")); err != nil {
			t.Fatal(err)
		}
	}
	if err := restorePublicationAuthoritySnapshot(io, snapshot); err != nil {
		t.Fatal(err)
	}
	for rel, fixture := range fixtures {
		data, err := os.ReadFile(io.path(rel))
		if err != nil || string(data) != string(fixture.data) {
			t.Fatalf("restored %s=%q err=%v", rel, data, err)
		}
		if runtime.GOOS != "windows" {
			info, _ := os.Stat(io.path(rel))
			if info.Mode().Perm() != fixture.mode {
				t.Fatalf("restored %s mode=%o want=%o", rel, info.Mode().Perm(), fixture.mode)
			}
		}
	}
	if _, err := os.Stat(io.path(expansionPublicationPrivateKeyFile)); !os.IsNotExist(err) {
		t.Fatalf("project-local private key was restored from public transaction state: %v", err)
	}
}

func TestPublicationAuthoritySnapshotRestoresExactExternalRecordAndDeletesNewGeneration(t *testing.T) {
	useExpansionAuthorityRootForTest(t, filepath.Join(t.TempDir(), "authority"))
	outputDir := t.TempDir()
	io := newIO(outputDir)
	oldTrust, _, err := createExpansionProjectAuthority("snapshot-project", outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := io.WriteJSON(expansionPublicationTrustFile, oldTrust); err != nil {
		t.Fatal(err)
	}
	if err := cleanupExpansionAuthorityCreationForOutput(outputDir); err != nil {
		t.Fatal(err)
	}
	before, err := capturePublicationAuthoritySnapshot(io)
	if err != nil {
		t.Fatal(err)
	}
	registryBefore := snapshotExpansionAuthorityRegistry(t)

	newTrust, _, err := createExpansionProjectAuthority("snapshot-project", outputDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := io.WriteJSON(expansionPublicationTrustFile, newTrust); err != nil {
		t.Fatal(err)
	}
	if err := restorePublicationAuthoritySnapshot(io, before); err != nil {
		t.Fatal(err)
	}
	if registryAfter := snapshotExpansionAuthorityRegistry(t); !reflect.DeepEqual(registryBefore, registryAfter) {
		t.Fatalf("external authority snapshot was not restored byte-identically\nbefore=%+v\nafter=%+v", registryBefore, registryAfter)
	}
	var restored ExpansionPublicationTrust
	if err := io.ReadJSON(expansionPublicationTrustFile, &restored); err != nil || !reflect.DeepEqual(restored, oldTrust) {
		t.Fatalf("public trust did not return to the old generation: trust=%+v err=%v", restored, err)
	}
}

func TestExpansionPublicationReceiptSupportsCloneFreshAuditWithoutPrivateAuthority(t *testing.T) {
	useExpansionAuthorityRootForTest(t, filepath.Join(t.TempDir(), "authority"))
	source, checkpoint, volumes := expansionAuthorityFixture(t)
	var receipt ExpansionPublicationReceipt
	if err := newIO(source.dir).ReadJSON(expansionPublicationReceiptFile, &receipt); err != nil {
		t.Fatal(err)
	}
	cloneRoot := t.TempDir()
	cloneOutput := filepath.Join(cloneRoot, "output")
	if err := os.CopyFS(cloneOutput, os.DirFS(source.dir)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cloneOutput, filepath.FromSlash(expansionPublicationPrivateKeyFile))); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cloneOutput, filepath.FromSlash(expansionRuntimePath))); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(cloneOutput, "meta", "revisions", "transaction.lock")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := newIO(cloneRoot).WriteJSON("project.json", publicationProjectManifest{Version: 1, ID: "validation-clone", Name: "validation-clone", RootDir: cloneRoot, OutputDir: cloneOutput, CreatedAt: now, UpdatedAt: now, LastAccessedAt: now, ClonedFromID: receipt.ProjectID, ClonedAt: &now}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneOutput, "meta", "revisions", "transaction.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	manifestPaths := make([]string, 0)
	if err := filepath.WalkDir(cloneRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		rel, relErr := filepath.Rel(cloneRoot, path)
		if relErr == nil && filepath.ToSlash(rel) != "output/meta/revisions/transaction.lock" {
			manifestPaths = append(manifestPaths, filepath.ToSlash(rel))
		}
		return relErr
	}); err != nil {
		t.Fatal(err)
	}
	manifestData, err := BuildExpansionValidationCloneManifest(cloneRoot, "validation-clone", receipt.ProjectID, strings.Repeat("b", 64), manifestPaths)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(cloneOutput, "meta", "revisions"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneOutput, filepath.FromSlash(expansionValidationCloneManifestFile)), manifestData, 0o444); err != nil {
		t.Fatal(err)
	}
	lineage, err := CreateExpansionPublicationCloneLineage(source.dir, "validation-clone", manifestData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneOutput, filepath.FromSlash(expansionPublicationLineageFile)), lineage, 0o644); err != nil {
		t.Fatal(err)
	}
	reportData, err := BuildExpansionValidationCloneReport(manifestData, lineage)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cloneOutput, filepath.FromSlash(expansionValidationCloneReportFile)), reportData, 0o444); err != nil {
		t.Fatal(err)
	}
	clone := NewStore(cloneOutput)
	if _, err := clone.acceptedExpansionSources(checkpoint, volumes); err != nil {
		t.Fatalf("clone fresh audit could not verify public publication authority: %v", err)
	}
	postValidationArtifact := filepath.Join(cloneOutput, "post-validation-write.json")
	completionAuditAfterExpansionValidation = func() {
		_ = os.WriteFile(postValidationArtifact, []byte("{}"), 0o600)
	}
	if _, err := clone.RunNormalCompletionAudit(); err == nil {
		t.Fatal("completion fresh audit accepted a write after manifest validation")
	}
	completionAuditAfterExpansionValidation = nil
	t.Cleanup(func() { completionAuditAfterExpansionValidation = nil })
	if err := os.Remove(postValidationArtifact); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cloneOutput, filepath.FromSlash(expansionPublicationPrivateKeyFile))); !os.IsNotExist(err) {
		t.Fatal("fresh audit recreated or retained the private publication authority")
	}
	assertTamperRejected := func(name string, mutate func(root string) (func(), error)) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			restore, err := mutate(cloneRoot)
			if err != nil {
				t.Fatal(err)
			}
			defer restore()
			if _, err := clone.acceptedExpansionSources(checkpoint, volumes); err == nil {
				t.Fatal("fresh audit accepted clone content outside the signed exact manifest")
			}
		})
	}
	assertTamperRejected("owned artifact replacement", func(root string) (func(), error) {
		path := filepath.Join(root, "output", "layered_outline.json")
		original, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return func() { _ = os.WriteFile(path, original, 0o644) }, os.WriteFile(path, []byte("[]"), 0o644)
	})
	assertTamperRejected("owned artifact deletion", func(root string) (func(), error) {
		path := filepath.Join(root, "output", "layered_outline.json")
		original, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return func() { _ = os.WriteFile(path, original, 0o644) }, os.Remove(path)
	})
	assertTamperRejected("unmanifested artifact addition", func(root string) (func(), error) {
		path := filepath.Join(root, "output", "unexpected.json")
		return func() { _ = os.Remove(path) }, os.WriteFile(path, []byte("{}"), 0o644)
	})
	assertTamperRejected("manifest field tampering", func(root string) (func(), error) {
		path := filepath.Join(root, "output", filepath.FromSlash(expansionValidationCloneManifestFile))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0o644); err != nil {
			return nil, err
		}
		return func() { _ = os.WriteFile(path, data, 0o644); _ = os.Chmod(path, 0o444) }, os.WriteFile(path, bytes.Replace(data, []byte(`"file_count":`), []byte(`"file_count": 1, "old_file_count":`), 1), 0o644)
	})
	assertTamperRejected("clone report digest tampering", func(root string) (func(), error) {
		path := filepath.Join(root, "output", filepath.FromSlash(expansionValidationCloneReportFile))
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if err := os.Chmod(path, 0o644); err != nil {
			return nil, err
		}
		tampered := bytes.Replace(data, []byte(`"manifest_digest": "`), []byte(`"manifest_digest": "0`), 1)
		return func() { _ = os.WriteFile(path, data, 0o644); _ = os.Chmod(path, 0o444) }, os.WriteFile(path, tampered, 0o644)
	})
	assertTamperRejected("transaction lock oversized", func(root string) (func(), error) {
		path := filepath.Join(root, "output", "meta", "revisions", "transaction.lock")
		return func() { _ = os.WriteFile(path, nil, 0o600) }, os.WriteFile(path, bytes.Repeat([]byte("x"), 513), 0o600)
	})
	assertTamperRejected("transaction lock directory", func(root string) (func(), error) {
		path := filepath.Join(root, "output", "meta", "revisions", "transaction.lock")
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		return func() { _ = os.RemoveAll(path); _ = os.WriteFile(path, nil, 0o600) }, os.Mkdir(path, 0o700)
	})
	assertTamperRejected("transaction lock hardlink", func(root string) (func(), error) {
		path := filepath.Join(root, "output", "meta", "revisions", "transaction.lock")
		peer := filepath.Join(root, "lock-peer")
		if err := os.WriteFile(peer, nil, 0o600); err != nil {
			return nil, err
		}
		if err := os.Remove(path); err != nil {
			return nil, err
		}
		return func() { _ = os.Remove(path); _ = os.Remove(peer); _ = os.WriteFile(path, nil, 0o600) }, os.Link(peer, path)
	})
	physicalReplayRoot := t.TempDir()
	physicalReplayOutput := filepath.Join(physicalReplayRoot, "output")
	if err := os.CopyFS(physicalReplayOutput, os.DirFS(cloneOutput)); err != nil {
		t.Fatal(err)
	}
	physicalReplayProject, err := os.ReadFile(filepath.Join(cloneRoot, "project.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(physicalReplayRoot, "project.json"), physicalReplayProject, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(physicalReplayOutput).acceptedExpansionSources(checkpoint, volumes); err == nil {
		t.Fatal("whole signed clone replayed at another physical root")
	}
	replayRoot := t.TempDir()
	replayOutput := filepath.Join(replayRoot, "output")
	if err := os.CopyFS(replayOutput, os.DirFS(cloneOutput)); err != nil {
		t.Fatal(err)
	}
	if err := newIO(replayRoot).WriteJSON("project.json", publicationProjectManifest{ID: "validation-clone-replay", ClonedFromID: receipt.ProjectID}); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(replayOutput).acceptedExpansionSources(checkpoint, volumes); err == nil {
		t.Fatal("signed lineage replayed into a different anonymous clone")
	}
}

func TestExpansionPublicationProjectManifestFailsClosed(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "output")
	if err := os.MkdirAll(output, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	valid := publicationProjectManifest{Version: 1, ID: "project-a", Name: "project-a", RootDir: root, OutputDir: output, CreatedAt: now, UpdatedAt: now, LastAccessedAt: now}
	original, err := json.MarshalIndent(valid, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "project.json")
	mutations := map[string]func() error{
		"missing":   func() error { return os.Remove(path) },
		"truncated": func() error { return os.WriteFile(path, []byte(`{"version":1`), 0o600) },
		"unknown field": func() error {
			return os.WriteFile(path, bytes.Replace(original, []byte("\n}"), []byte(",\n  \"unexpected\": true\n}"), 1), 0o600)
		},
		"empty id": func() error {
			invalid := valid
			invalid.ID = ""
			data, _ := json.Marshal(invalid)
			return os.WriteFile(path, data, 0o600)
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, original, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := mutate(); err != nil {
				t.Fatal(err)
			}
			if _, err := publicationProjectMatches(output, "project-a"); err == nil {
				t.Fatal("invalid project manifest was accepted")
			}
			if _, err := publicationProjectID(output); err == nil {
				t.Fatal("invalid project manifest reached the publication signing boundary")
			}
		})
	}
	t.Run("root output mismatch", func(t *testing.T) {
		invalid := valid
		invalid.OutputDir = filepath.Join(root, "other-output")
		data, _ := json.MarshalIndent(invalid, "", "  ")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := publicationProjectID(output); err == nil {
			t.Fatal("mismatched manifest output reached publication signing")
		}
	})
	t.Run("check after replacement", func(t *testing.T) {
		if err := os.WriteFile(path, original, 0o600); err != nil {
			t.Fatal(err)
		}
		identity, err := capturePublicationProjectIdentity(output)
		if err != nil {
			t.Fatal(err)
		}
		replacement := path + ".replacement"
		if err := os.WriteFile(replacement, original, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(replacement, path); err != nil {
			t.Fatal(err)
		}
		if err := identity.Verify(); err == nil {
			t.Fatal("same-byte project manifest replacement escaped identity binding")
		}
	})
	_ = os.WriteFile(path, original, 0o600)
}

func TestExpansionValidationCloneSnapshotRejectsABARewrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact.json")
	original := []byte(`{"value":"a"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureExpansionValidationCloneSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"value":"b"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Verify(); err == nil {
		t.Fatal("fresh-audit A-B-A rewrite escaped filesystem generation validation")
	}
}

func TestExpansionValidationCloneSnapshotRejectsExactTreeDrift(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "artifact.json")
	if err := os.WriteFile(path, []byte(`{"value":"a"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := captureExpansionValidationCloneSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	added := filepath.Join(root, "added.json")
	if err := os.WriteFile(added, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Verify(); err == nil {
		t.Fatal("fresh audit accepted a post-snapshot file addition")
	}
	if err := os.Remove(added); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, filepath.Join(root, "renamed.json")); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.Verify(); err == nil {
		t.Fatal("fresh audit accepted a post-snapshot rename/removal")
	}
}

func expansionAuthorityFixture(t *testing.T) (*Store, *domain.CompletionRevalidationCheckpoint, []domain.VolumeOutline) {
	t.Helper()
	useExpansionAuthorityRootForTest(t, filepath.Join(t.TempDir(), "publication-authority"))
	st := newCompletionAuditFixture(t, completionAuditFixtureOptions{dramaticFacts: true, expansionOrigin: true})
	// This low-level audit fixture seals a receipt without driving the revision
	// publication state machine. Mark that synthetic setup complete so startup
	// tests exercise the rotation journal they construct, not an intentionally
	// uncommitted first-publication creation journal.
	if err := cleanupExpansionAuthorityCreationForOutput(st.dir); err != nil {
		t.Fatal(err)
	}
	progress, err := st.Progress.Load()
	if err != nil || progress == nil || progress.CompletionRevalidation == nil {
		t.Fatalf("load completion checkpoint: %v", err)
	}
	volumes, err := st.Outline.LoadLayeredOutline()
	if err != nil {
		t.Fatal(err)
	}
	checkpoint := *progress.CompletionRevalidation
	return st, &checkpoint, volumes
}
