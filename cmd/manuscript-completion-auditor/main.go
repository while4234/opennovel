package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	storepkg "github.com/voocel/ainovel-cli/internal/store"
)

const receiptRel = "meta/manuscript/completion-audit-receipt.json"
const trustRel = "meta/manuscript/completion-auditor-trust.json"

func main() {
	if len(os.Args) < 2 || os.Args[1] != "audit" {
		fail("invalid_operation")
	}
	flags := flag.NewFlagSet("audit", flag.ContinueOnError)
	projectRoot := flags.String("project-root", "", "project output root")
	if flags.Parse(os.Args[2:]) != nil || strings.TrimSpace(*projectRoot) == "" {
		fail("invalid_request")
	}
	st := storepkg.NewStore(*projectRoot)
	if err := st.Init(); err != nil {
		fail("store_unavailable")
	}
	digest, err := st.RunNormalCompletionAudit()
	if err != nil {
		fail("audit_failed")
	}
	if err := sealReceipt(*projectRoot); err != nil {
		fail("seal_failed")
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"report_digest": digest})
}

func sealReceipt(root string) error {
	privatePath := filepath.Join(root, ".completion-auditor", "ed25519.key")
	privateKey, err := loadOrCreatePrivateKey(privatePath)
	if err != nil {
		return err
	}
	receiptPath := filepath.Join(root, filepath.FromSlash(receiptRel))
	payload, err := os.ReadFile(receiptPath)
	if err != nil {
		return err
	}
	var receipt map[string]any
	if err := json.Unmarshal(payload, &receipt); err != nil {
		return err
	}
	receipt["signature"] = ""
	canonical, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	receipt["signature"] = base64.StdEncoding.EncodeToString(ed25519.Sign(privateKey, canonical))
	sealed, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	if err := atomicWrite(receiptPath, append(sealed, '\n'), 0o600); err != nil {
		return err
	}
	trust := map[string]any{"version": 1, "algorithm": "ed25519", "public_key": base64.StdEncoding.EncodeToString(privateKey.Public().(ed25519.PublicKey))}
	encodedTrust, _ := json.MarshalIndent(trust, "", "  ")
	return atomicWrite(filepath.Join(root, filepath.FromSlash(trustRel)), append(encodedTrust, '\n'), 0o644)
}

func loadOrCreatePrivateKey(path string) (ed25519.PrivateKey, error) {
	if payload, err := os.ReadFile(path); err == nil {
		decoded, decodeErr := base64.StdEncoding.DecodeString(strings.TrimSpace(string(payload)))
		if decodeErr != nil || len(decoded) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("invalid private identity")
		}
		return ed25519.PrivateKey(decoded), nil
	}
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := atomicWrite(path, []byte(base64.StdEncoding.EncodeToString(privateKey)), 0o600); err != nil {
		return nil, err
	}
	return privateKey, nil
}

func atomicWrite(path string, payload []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".audit-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(mode); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(payload); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func fail(code string) { fmt.Fprintf(os.Stderr, "{\"error\":{\"code\":%q}}\n", code); os.Exit(1) }
