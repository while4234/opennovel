package codexauth

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// OfficialGPT56Models are documented Codex candidates. model/list remains the
// source of truth for what the currently authenticated account can use.
var OfficialGPT56Models = []string{"gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna"}

type ModelCatalogEntry struct {
	ID                        string   `json:"id"`
	Model                     string   `json:"model"`
	Hidden                    bool     `json:"hidden"`
	DefaultReasoningEffort    string   `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []string `json:"-"`
}

type catalogReasoningEffort struct {
	ReasoningEffort string `json:"reasoningEffort"`
}

type catalogEntryWire struct {
	ID                        string                   `json:"id"`
	Model                     string                   `json:"model"`
	Hidden                    bool                     `json:"hidden"`
	DefaultReasoningEffort    string                   `json:"defaultReasoningEffort"`
	SupportedReasoningEfforts []catalogReasoningEffort `json:"supportedReasoningEfforts"`
}

// ReadModelCatalog follows the same Codex app-server handshake used by
// CowWechat: initialize, initialized, then credential-bound model/list.
func ReadModelCatalog(ctx context.Context, authFile string) ([]ModelCatalogEntry, error) {
	authPath := ResolveAuthPath(authFile)
	if info, err := os.Stat(authPath); err != nil || info.IsDir() {
		return nil, fmt.Errorf("Codex auth file is not configured or does not exist")
	}
	binary, err := exec.LookPath("codex")
	if err != nil {
		return nil, fmt.Errorf("Codex CLI with app-server support was not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, binary, "app-server", "--listen", "stdio://")
	cmd.Env = append(os.Environ(), "CODEX_HOME="+filepath.Dir(authPath), "CODEX_AUTH_FILE="+authPath)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stdout: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, fmt.Errorf("open Codex app-server stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start Codex app-server: %w", err)
	}
	defer func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()

	lines := make(chan []byte)
	scanErr := make(chan error, 1)
	go scanCatalogLines(stdout, lines, scanErr)
	stderrTail := make(chan string, 1)
	go func() {
		data, _ := io.ReadAll(io.LimitReader(stderr, 32<<10))
		stderrTail <- strings.TrimSpace(string(data))
	}()

	initialize := map[string]any{
		"id": 1, "method": "initialize",
		"params": map[string]any{
			"clientInfo":   map[string]any{"name": "ainovel", "title": "AINovel", "version": "model-catalog"},
			"capabilities": map[string]any{"experimentalApi": true, "optOutNotificationMethods": []string{}},
		},
	}
	if err := writeCatalogMessage(stdin, initialize); err != nil {
		return nil, err
	}
	if _, err := waitCatalogResponse(ctx, stdin, lines, scanErr, 1); err != nil {
		return nil, catalogErrorWithStderr(err, stderrTail)
	}
	if err := writeCatalogMessage(stdin, map[string]any{"method": "initialized", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	if err := writeCatalogMessage(stdin, map[string]any{"id": 2, "method": "model/list", "params": map[string]any{}}); err != nil {
		return nil, err
	}
	result, err := waitCatalogResponse(ctx, stdin, lines, scanErr, 2)
	if err != nil {
		return nil, catalogErrorWithStderr(err, stderrTail)
	}
	var payload struct {
		Data []catalogEntryWire `json:"data"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return nil, fmt.Errorf("decode Codex model/list result: %w", err)
	}
	out := make([]ModelCatalogEntry, 0, len(payload.Data))
	for _, item := range payload.Data {
		id := strings.TrimSpace(item.ID)
		if id == "" {
			id = strings.TrimSpace(item.Model)
		}
		if id == "" || item.Hidden {
			continue
		}
		entry := ModelCatalogEntry{ID: id, Model: strings.TrimSpace(item.Model), DefaultReasoningEffort: strings.TrimSpace(item.DefaultReasoningEffort)}
		for _, effort := range item.SupportedReasoningEfforts {
			if value := strings.TrimSpace(effort.ReasoningEffort); value != "" {
				entry.SupportedReasoningEfforts = append(entry.SupportedReasoningEfforts, value)
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

func scanCatalogLines(reader io.Reader, lines chan<- []byte, scanErr chan<- error) {
	defer close(lines)
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64<<10), 4<<20)
	for scanner.Scan() {
		line := append([]byte(nil), scanner.Bytes()...)
		lines <- line
	}
	scanErr <- scanner.Err()
}

func writeCatalogMessage(writer io.Writer, message any) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := writer.Write(data); err != nil {
		return fmt.Errorf("write Codex app-server request: %w", err)
	}
	return nil
}

func waitCatalogResponse(ctx context.Context, stdin io.Writer, lines <-chan []byte, scanErr <-chan error, requestID int) (json.RawMessage, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("Codex app-server request timed out: %w", ctx.Err())
		case err := <-scanErr:
			if err != nil {
				return nil, fmt.Errorf("read Codex app-server response: %w", err)
			}
			return nil, fmt.Errorf("Codex app-server exited before responding")
		case line, ok := <-lines:
			if !ok {
				return nil, fmt.Errorf("Codex app-server exited before responding")
			}
			var message struct {
				ID     *int            `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(line, &message) != nil {
				continue
			}
			if message.Method != "" && message.ID != nil && len(message.Result) == 0 && len(message.Error) == 0 {
				_ = writeCatalogMessage(stdin, map[string]any{"id": *message.ID, "result": map[string]any{}})
				continue
			}
			if message.ID == nil || *message.ID != requestID {
				continue
			}
			if len(message.Error) > 0 && string(message.Error) != "null" {
				return nil, fmt.Errorf("Codex app-server request failed: %s", strings.TrimSpace(string(message.Error)))
			}
			return message.Result, nil
		}
	}
}

func catalogErrorWithStderr(err error, stderr <-chan string) error {
	select {
	case tail := <-stderr:
		if tail != "" {
			if len(tail) > 300 {
				tail = tail[len(tail)-300:]
			}
			return fmt.Errorf("%w; app-server stderr: %s", err, tail)
		}
	default:
	}
	return err
}
