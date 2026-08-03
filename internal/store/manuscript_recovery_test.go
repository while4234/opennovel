package store

import (
	"errors"
	"testing"
)

func TestRequireManuscriptWriteReadyRetriesManuscriptPublicationRecovery(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := st.Init(); err != nil {
		t.Fatal(err)
	}
	st.manuscriptPublicationRecoveryErr = errors.New("injected manuscript journal failure")
	st.recoveryErr = st.manuscriptPublicationRecoveryErr
	status := st.ManuscriptRecoveryState()
	if !status.Required || len(status.Owners) != 1 || status.Owners[0] != "manuscript_publication" {
		t.Fatalf("status = %+v", status)
	}
	if err := st.RequireManuscriptWriteReady(); err != nil {
		t.Fatalf("retry recovery: %v", err)
	}
	if status = st.ManuscriptRecoveryState(); status.Required {
		t.Fatalf("status after retry = %+v", status)
	}
}

func TestManuscriptRecoveryStateSeparatesGlobalAndProjectAuthorityFailures(t *testing.T) {
	st := NewStore(t.TempDir())
	st.authorityRecoveryErr = errors.New("global authority maintenance warning")
	st.recoveryErr = st.authorityRecoveryErr
	if status := st.ManuscriptRecoveryState(); status.Required {
		t.Fatalf("global maintenance must not block manuscript writes: %+v", status)
	}

	st.publicationAuthorityRecoveryErr = errors.New("project publication authority recovery")
	st.refreshRecoveryErrorLocked()
	status := st.ManuscriptRecoveryState()
	if !status.Required || !status.Retryable || len(status.Owners) != 1 || status.Owners[0] != "publication_authority" {
		t.Fatalf("project authority status = %+v", status)
	}
}

func TestManuscriptRecoveryStateDoesNotMislabelUnknownRecoveryAsStructureMigration(t *testing.T) {
	st := NewStore(t.TempDir())
	st.recoveryErr = errors.New("unclassified global maintenance warning")
	if status := st.ManuscriptRecoveryState(); status.Required {
		t.Fatalf("unclassified recovery must not be mislabeled as structure migration: %+v", status)
	}

	st.structureMigrationRecoveryErr = errors.New("pending structure migration")
	st.refreshRecoveryErrorLocked()
	status := st.ManuscriptRecoveryState()
	if !status.Required || len(status.Owners) != 1 || status.Owners[0] != "structure_migration" {
		t.Fatalf("structure migration status = %+v", status)
	}
}
