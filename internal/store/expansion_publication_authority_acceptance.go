//go:build acceptance

package store

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ConfigureExpansionAuthorityForAcceptanceProcess isolates the publication
// authority used by the explicitly tagged browser acceptance binary. This
// symbol is absent from production builds, which remain bound to the
// administrator-installed authority root.
func ConfigureExpansionAuthorityForAcceptanceProcess(rootDir string) (func(), error) {
	cleanRoot := filepath.Clean(rootDir)
	tempRoot := filepath.Clean(os.TempDir())
	relative, err := filepath.Rel(tempRoot, cleanRoot)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return nil, fmt.Errorf("acceptance authority root must be an isolated child of the system temp directory")
	}
	return configureExpansionAuthorityForIsolatedProcess(cleanRoot)
}
