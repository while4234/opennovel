package expansionauditorclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/voocel/ainovel-cli/internal/domain"
	"github.com/voocel/ainovel-cli/internal/host"
)

var (
	ErrUnavailable  = fmt.Errorf("expansion auditor unavailable")
	ErrProcess      = fmt.Errorf("expansion auditor process failed")
	ErrDecode       = fmt.Errorf("expansion auditor response invalid")
	ErrTaskNotFound = fmt.Errorf("expansion auditor task not found")
)

const commandEnvironment = "AINOVEL_EXPANSION_AUDITOR"

// Client is the product-side IPC boundary. It knows only how to invoke the
// separately composed auditor process and decode signed public artifacts. It
// never opens the auditor identity file and contains no signing code.
type Client struct {
	command string
}

func New() (*Client, error) {
	command := strings.TrimSpace(os.Getenv(commandEnvironment))
	if command == "" {
		name := "expansion-auditor"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if executable, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(executable), name)
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				command = candidate
			}
		}
		if command == "" {
			if workingDirectory, err := os.Getwd(); err == nil {
				for _, candidate := range []string{filepath.Join(workingDirectory, name), filepath.Join(workingDirectory, "bin", name)} {
					if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
						command = candidate
						break
					}
				}
			}
		}
	}
	if command == "" {
		return nil, fmt.Errorf("%w: independent command is missing; set %s", ErrUnavailable, commandEnvironment)
	}
	return &Client{command: command}, nil
}

// Command returns the resolved independent component for startup diagnostics.
func (client *Client) Command() string {
	if client == nil {
		return ""
	}
	return client.command
}

func (client *Client) Init(ctx context.Context, projectRoot string) error {
	return client.run(ctx, nil, "init", "--project-root", filepath.Clean(projectRoot))
}

func (client *Client) ReviewDependency(ctx context.Context, projectRoot, taskID string) (domain.ExpansionDependencyReview, error) {
	var result domain.ExpansionDependencyReview
	err := client.run(ctx, &result, "dependency", "--project-root", filepath.Clean(projectRoot), "--task-id", strings.TrimSpace(taskID))
	return result, err
}

func (client *Client) ReviewRevision(ctx context.Context, projectRoot, revisionID string) (host.ExpansionAuditArtifact, error) {
	var result host.ExpansionAuditArtifact
	err := client.run(ctx, &result, "revision", "--project-root", filepath.Clean(projectRoot), "--revision-id", strings.TrimSpace(revisionID))
	return result, err
}

func (client *Client) run(ctx context.Context, result any, args ...string) error {
	if client == nil || strings.TrimSpace(client.command) == "" {
		return fmt.Errorf("%w: independent process is not configured", ErrUnavailable)
	}
	command := exec.CommandContext(ctx, client.command, args...)
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrUnavailable, message)
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 3 {
			return ErrTaskNotFound
		}
		return fmt.Errorf("%w: %s", ErrProcess, message)
	}
	if result == nil {
		return nil
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("%w: %v", ErrDecode, err)
	}
	return nil
}
