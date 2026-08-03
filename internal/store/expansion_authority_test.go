package store

import (
	"path/filepath"
	"testing"
)

// useExpansionAuthorityRootForTest is an explicit same-package dependency
// injection boundary. Production binaries have no input that reaches these
// resolvers, including environment variables and argv[0].
func useExpansionAuthorityRootForTest(t *testing.T, rootDir string) string {
	t.Helper()
	rootDir = filepath.Clean(rootDir)
	previousRoot := expansionAuthorityRootDirResolver
	previousInstallation := expansionAuthorityInstallationDirResolver
	previousInitializationAllowed := expansionAuthorityInitializationAllowed
	previousTestConfigurationActive := expansionAuthorityTestConfigurationActive
	previousBootstrapVerifier := expansionAuthorityBootstrapVerifier
	expansionAuthorityRootDirResolver = func() (string, error) { return rootDir, nil }
	expansionAuthorityInstallationDirResolver = func(string) (string, error) { return rootDir + ".installation", nil }
	expansionAuthorityInitializationAllowed = true
	expansionAuthorityTestConfigurationActive = true
	expansionAuthorityBootstrapVerifier = func() error { return nil }
	t.Cleanup(func() {
		expansionAuthorityRootDirResolver = previousRoot
		expansionAuthorityInstallationDirResolver = previousInstallation
		expansionAuthorityInitializationAllowed = previousInitializationAllowed
		expansionAuthorityTestConfigurationActive = previousTestConfigurationActive
		expansionAuthorityBootstrapVerifier = previousBootstrapVerifier
	})
	return rootDir
}
