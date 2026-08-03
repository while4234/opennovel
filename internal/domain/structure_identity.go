package domain

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"strings"
)

const (
	StructureKindVolume  = "volume"
	StructureKindArc     = "arc"
	StructureKindChapter = "chapter"
)

// LegacyStructureID derives a stable ID from the project identity and the
// node's original structural path. It is intentionally deterministic so a
// read-only legacy open can expose IDs without writing a migration eagerly.
func LegacyStructureID(projectID, kind, legacyPath string) string {
	kind = normalizeStructureKind(kind)
	legacyPath = path.Clean(strings.ReplaceAll(strings.TrimSpace(legacyPath), "\\", "/"))
	sum := sha256.Sum256([]byte(strings.TrimSpace(projectID) + "\x00" + kind + "\x00" + legacyPath))
	return structureIDPrefix(kind) + hex.EncodeToString(sum[:16])
}

// NewStructureID creates a persistent unique ID for a newly introduced node.
func NewStructureID(kind string) (string, error) {
	kind = normalizeStructureKind(kind)
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate %s ID: %w", kind, err)
	}
	return structureIDPrefix(kind) + hex.EncodeToString(random), nil
}

func normalizeStructureKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case StructureKindVolume:
		return StructureKindVolume
	case StructureKindArc:
		return StructureKindArc
	default:
		return StructureKindChapter
	}
}

func structureIDPrefix(kind string) string {
	switch kind {
	case StructureKindVolume:
		return "vol_"
	case StructureKindArc:
		return "arc_"
	default:
		return "ch_"
	}
}
