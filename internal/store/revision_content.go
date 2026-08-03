package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
)

type RevisionContentStore struct {
	io *IO
}

func NewRevisionContentStore(io *IO) *RevisionContentStore {
	return &RevisionContentStore{io: io}
}

func (s *RevisionContentStore) PutMarkdown(content string) (domain.ManuscriptContentRef, error) {
	return s.put([]byte(content), "text/markdown", ".md")
}

func (s *RevisionContentStore) PutJSON(value any) (domain.ManuscriptContentRef, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return domain.ManuscriptContentRef{}, err
	}
	return s.put(payload, "application/json", ".json")
}

// PutJSONTracked reports whether this call created the content-addressed
// object. Callers that coordinate a larger transaction can remove a newly
// created object if the authoritative transaction later fails.
func (s *RevisionContentStore) PutJSONTracked(value any) (domain.ManuscriptContentRef, bool, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return domain.ManuscriptContentRef{}, false, err
	}
	return s.putTracked(payload, "application/json", ".json")
}

func (s *RevisionContentStore) PutRawJSON(payload []byte) (domain.ManuscriptContentRef, error) {
	if !json.Valid(payload) {
		return domain.ManuscriptContentRef{}, fmt.Errorf("revision JSON content is invalid")
	}
	return s.put(payload, "application/json", ".json")
}

func (s *RevisionContentStore) put(payload []byte, mediaType, extension string) (domain.ManuscriptContentRef, error) {
	ref, _, err := s.putTracked(payload, mediaType, extension)
	return ref, err
}

func (s *RevisionContentStore) putTracked(payload []byte, mediaType, extension string) (domain.ManuscriptContentRef, bool, error) {
	digest := sha256.Sum256(payload)
	sha := hex.EncodeToString(digest[:])
	ref := domain.ManuscriptContentRef{SHA256: sha, MediaType: mediaType, Size: int64(len(payload))}
	rel := revisionContentPath(sha, extension)
	if existing, err := s.io.ReadFile(rel); err == nil {
		if domain.ContentSignature(existing) != sha {
			return domain.ManuscriptContentRef{}, false, fmt.Errorf("revision content address collision or corruption for %s", sha)
		}
		return ref, false, nil
	} else if !os.IsNotExist(err) {
		return domain.ManuscriptContentRef{}, false, err
	}
	if err := s.io.WriteMarkdown(rel, string(payload)); err != nil {
		return domain.ManuscriptContentRef{}, false, err
	}
	return ref, true, nil
}

// RemoveCreated removes a content object created by a failed enclosing
// transaction. It is intentionally explicit rather than a general delete API.
func (s *RevisionContentStore) RemoveCreated(ref domain.ManuscriptContentRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	extension := ".json"
	if ref.MediaType == "text/markdown" {
		extension = ".md"
	}
	return s.io.RemoveFile(revisionContentPath(ref.SHA256, extension))
}

func (s *RevisionContentStore) Read(ref domain.ManuscriptContentRef) ([]byte, error) {
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	extension := ".json"
	if ref.MediaType == "text/markdown" {
		extension = ".md"
	}
	payload, err := s.io.ReadFile(revisionContentPath(ref.SHA256, extension))
	if err != nil {
		return nil, err
	}
	if int64(len(payload)) != ref.Size || domain.ContentSignature(payload) != ref.SHA256 {
		return nil, fmt.Errorf("revision content %s failed SHA-256 verification", ref.SHA256)
	}
	return payload, nil
}

func revisionContentPath(sha, extension string) string {
	sha = strings.ToLower(strings.TrimSpace(sha))
	if len(sha) != 64 {
		return filepath.ToSlash(filepath.Join("meta", "revisions", "content", "invalid"))
	}
	return filepath.ToSlash(filepath.Join("meta", "revisions", "content", "sha256", sha[:2], sha+extension))
}
