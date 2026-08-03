package bootstrap

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDockerNonRootConfigAndAuthorityVolumesAreSeparated(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate Docker deployment contract test")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	dockerfile := readDeploymentContract(t, filepath.Join(repositoryRoot, "Dockerfile"))
	compose := readDeploymentContract(t, filepath.Join(repositoryRoot, "docker-compose.yml"))
	readme := readDeploymentContract(t, filepath.Join(repositoryRoot, "README.md"))
	architecture := readDeploymentContract(t, filepath.Join(repositoryRoot, "docs", "manuscript-revision-architecture.md"))

	for name, document := range map[string]string{
		"Dockerfile":   dockerfile,
		"compose":      compose,
		"README":       readme,
		"architecture": architecture,
	} {
		for _, required := range []string{"/home/ainovel/.ainovel", "/var/lib/ainovel"} {
			if !strings.Contains(document, required) {
				t.Fatalf("%s does not declare separated config/authority path %s", name, required)
			}
		}
		if strings.Contains(document, "/root/.ainovel") {
			t.Fatalf("%s still directs non-root runtime configuration to root HOME", name)
		}
	}
	for _, required := range []string{
		"ENV HOME=/home/ainovel",
		`VOLUME ["/home/ainovel/.ainovel", "/var/lib/ainovel"]`,
		"adduser -D -h /home/ainovel -u 65532",
		"USER 65532:65532",
	} {
		if !strings.Contains(dockerfile, required) {
			t.Fatalf("Dockerfile misses non-root discovery contract %q", required)
		}
	}
	if strings.Contains(dockerfile, "XDG_CONFIG_HOME=/var/lib/ainovel") {
		t.Fatal("authority volume is still advertised as ordinary config storage")
	}
}

func readDeploymentContract(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
