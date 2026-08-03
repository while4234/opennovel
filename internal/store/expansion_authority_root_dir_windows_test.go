//go:build windows

package store

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuthorityInstallationRejectsUserOwnedInheritedWindowsACL(t *testing.T) {
	installation := filepath.Join(t.TempDir(), "publication-authority-installation-v1")
	if err := prepareExpansionAuthorityInstallationDir(installation); err != nil {
		t.Fatal(err)
	}
	if err := verifyExpansionAuthorityInstallationDir(installation); err == nil {
		t.Fatal("ordinary inherited user DACL was accepted as a release-managed installation anchor")
	}
}

func TestAuthorityRuntimeDangerousRightsAreProbedIndividuallyOnBothObjects(t *testing.T) {
	original := authorityOpenPathAccess
	t.Cleanup(func() { authorityOpenPathAccess = original })
	installation := filepath.Join(`C:\ProgramData\AINovel`, "publication-authority-installation-v1")
	parent := filepath.Dir(installation)

	for _, object := range []struct {
		name string
		path string
	}{{"parent", parent}, {"installation", installation}} {
		for _, granted := range authorityDangerousAccess {
			t.Run(object.name+"/"+granted.name, func(t *testing.T) {
				calls := make(map[string]int)
				authorityOpenPathAccess = func(path string, access uint32) (bool, error) {
					calls[fmt.Sprintf("%s:%08x", filepath.Clean(path), access)]++
					return filepath.Clean(path) == filepath.Clean(object.path) && access == granted.access, nil
				}
				err := verifyAuthorityRuntimeCannotMutate(object.path, object.name)
				if err == nil || !strings.Contains(err.Error(), granted.name) {
					t.Fatalf("single dangerous ACE %s was not rejected: %v", granted.name, err)
				}
				for _, expected := range authorityDangerousAccess {
					key := fmt.Sprintf("%s:%08x", filepath.Clean(object.path), expected.access)
					if expected.access == granted.access {
						if calls[key] != 1 {
							t.Fatalf("granted right %s probe count=%d", expected.name, calls[key])
						}
						break
					}
				}
			})
		}
	}
}

func TestAuthorityRuntimeAccessProbeFailsClosedOnUnexpectedWindowsError(t *testing.T) {
	original := authorityOpenPathAccess
	t.Cleanup(func() { authorityOpenPathAccess = original })
	authorityOpenPathAccess = func(string, uint32) (bool, error) {
		return false, fmt.Errorf("unexpected access-check failure")
	}
	if err := verifyAuthorityRuntimeCannotMutate(`C:\ProgramData\AINovel`, "parent"); err == nil || !strings.Contains(err.Error(), "unexpected access-check failure") {
		t.Fatalf("unexpected probe error was not fail-closed: %v", err)
	}
}

func TestAuthorityRuntimeRootRejectsObjectReplacementRightsButAllowsChildMaintenance(t *testing.T) {
	original := authorityOpenPathAccess
	t.Cleanup(func() { authorityOpenPathAccess = original })
	root := `C:\ProgramData\AINovel\publication-authority-v1`
	for _, granted := range authorityRootObjectDangerousAccess {
		t.Run(granted.name, func(t *testing.T) {
			authorityOpenPathAccess = func(path string, access uint32) (bool, error) {
				return filepath.Clean(path) == filepath.Clean(root) && access == granted.access, nil
			}
			if err := verifyAuthorityRuntimeCannotAccess(root, "runtime root", authorityRootObjectDangerousAccess); err == nil || !strings.Contains(err.Error(), granted.name) {
				t.Fatalf("runtime root object right %s was not rejected: %v", granted.name, err)
			}
		})
	}

	authorityOpenPathAccess = func(_ string, access uint32) (bool, error) {
		return access == uint32(0x00000040), nil // DELETE_CHILD is needed inside the writable root.
	}
	if err := verifyAuthorityRuntimeCannotAccess(root, "runtime root", authorityRootObjectDangerousAccess); err != nil {
		t.Fatalf("child maintenance right was confused with root replacement: %v", err)
	}
}
