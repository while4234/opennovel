package completionauditorclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const commandEnvironment = "AINOVEL_COMPLETION_AUDITOR"

type Result struct {
	ReportDigest string `json:"report_digest"`
}

type Client struct{ command string }

func New() (*Client, error) {
	command := strings.TrimSpace(os.Getenv(commandEnvironment))
	if command == "" {
		name := "manuscript-completion-auditor"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		if executable, err := os.Executable(); err == nil {
			candidate := filepath.Join(filepath.Dir(executable), name)
			if info, statErr := os.Stat(candidate); statErr == nil && !info.IsDir() {
				command = candidate
			}
		}
	}
	if command == "" {
		return nil, fmt.Errorf("independent completion auditor is unavailable")
	}
	return &Client{command: command}, nil
}

func (c *Client) Audit(ctx context.Context, projectRoot string) (Result, error) {
	var result Result
	if c == nil || c.command == "" {
		return result, fmt.Errorf("independent completion auditor is unavailable")
	}
	command := exec.CommandContext(ctx, c.command, "audit", "--project-root", filepath.Clean(projectRoot))
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &bytes.Buffer{}
	if err := command.Run(); err != nil {
		return result, fmt.Errorf("independent completion audit failed")
	}
	decoder := json.NewDecoder(&stdout)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil || strings.TrimSpace(result.ReportDigest) == "" {
		return Result{}, fmt.Errorf("independent completion audit response is invalid")
	}
	return result, nil
}
