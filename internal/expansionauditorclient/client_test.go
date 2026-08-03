package expansionauditorclient

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

func TestAuditorClientClassifiesMissingProcessAndMalformedResponse(t *testing.T) {
	t.Run("missing", func(t *testing.T) {
		client := &Client{command: filepath.Join(t.TempDir(), "missing-auditor.exe")}
		if err := client.Init(context.Background(), t.TempDir()); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("missing command classification=%v", err)
		}
	})
	for _, test := range []struct {
		name, body string
		want       error
	}{
		{name: "process", body: "@exit /b 7\r\n", want: ErrProcess},
		{name: "decode", body: "@echo not-json\r\n", want: ErrDecode},
	} {
		t.Run(test.name, func(t *testing.T) {
			if runtime.GOOS != "windows" {
				t.Skip("Windows command shim regression")
			}
			path := filepath.Join(t.TempDir(), "auditor.cmd")
			if err := os.WriteFile(path, []byte(test.body), 0o700); err != nil {
				t.Fatal(err)
			}
			client := &Client{command: path}
			var err error
			if test.want == ErrDecode {
				_, err = client.ReviewDependency(context.Background(), t.TempDir(), "task")
			} else {
				err = client.Init(context.Background(), t.TempDir())
			}
			if !errors.Is(err, test.want) {
				t.Fatalf("classification=%v want=%v", err, test.want)
			}
		})
	}
}

func TestReleaseLayoutDiscoversAndStartsIndependentAuditorWithoutEnvironment(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	repository := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	layout := t.TempDir()
	name := "expansion-auditor"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	auditor := filepath.Join(layout, name)
	goCommand := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goCommand += ".exe"
	}
	build := exec.Command(goCommand, "build", "-trimpath", "-o", auditor, "./cmd/expansion-auditor")
	build.Dir = repository
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release-layout auditor: %v\n%s", err, output)
	}
	prior, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(layout); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(prior) })
	t.Setenv(commandEnvironment, "")
	client, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Clean(client.Command()) != filepath.Clean(auditor) {
		t.Fatalf("resolved command=%q want %q", client.Command(), auditor)
	}
	project := filepath.Join(t.TempDir(), "project")
	if err := client.Init(context.Background(), project); err != nil {
		t.Fatalf("start release-layout auditor: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, ".ai", "revisions", "expansion-auditor-trust.json")); err != nil {
		t.Fatalf("auditor did not persist public trust: %v", err)
	}
}
