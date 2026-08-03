package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
)

const keyMaterial = "ainovel-private-download-bundle-v1-20260716"

type credentials struct {
	Version        int             `json:"version"`
	XAuthorization string          `json:"x_authorization"`
	BaiduPCSConfig json.RawMessage `json:"baidu_pcs_config"`
}

func main() {
	headersPath := flag.String("headers", "", "path to temporary DaliPan request headers")
	baiduPath := flag.String("baidu-config", "", "path to BaiduPCS-Go pcs_config.json")
	outputPath := flag.String("out", "", "encrypted output path")
	deleteHeaders := flag.Bool("delete-headers", false, "delete plaintext header file after success")
	flag.Parse()
	if *headersPath == "" || *baiduPath == "" || *outputPath == "" {
		fatal("headers, baidu-config and out are required")
	}
	headerBytes, err := os.ReadFile(*headersPath)
	if err != nil {
		fatal("read headers: %v", err)
	}
	xAuthorization := headerValue(string(headerBytes), "X-Authorization")
	if xAuthorization == "" {
		fatal("X-Authorization is empty")
	}
	baiduConfig, err := os.ReadFile(*baiduPath)
	if err != nil {
		fatal("read BaiduPCS-Go config: %v", err)
	}
	if !json.Valid(baiduConfig) {
		fatal("BaiduPCS-Go config is not valid JSON")
	}
	plain, err := json.Marshal(credentials{Version: 1, XAuthorization: xAuthorization, BaiduPCSConfig: baiduConfig})
	if err != nil {
		fatal("encode credentials: %v", err)
	}
	key := sha256.Sum256([]byte(keyMaterial))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		fatal("create cipher: %v", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		fatal("create GCM: %v", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		fatal("create nonce: %v", err)
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	payload := base64.StdEncoding.EncodeToString(append(nonce, sealed...)) + "\n"
	if err := os.WriteFile(*outputPath, []byte(payload), 0o600); err != nil {
		fatal("write encrypted credentials: %v", err)
	}
	if *deleteHeaders {
		if err := os.Remove(*headersPath); err != nil && !os.IsNotExist(err) {
			fatal("delete plaintext headers: %v", err)
		}
	}
	fmt.Println("credentials sealed successfully; no credential values were displayed")
}

func headerValue(text, name string) string {
	prefix := strings.ToLower(name) + ":"
	for _, line := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(strings.ToLower(trimmed), prefix) {
			return strings.TrimSpace(trimmed[len(prefix):])
		}
	}
	return ""
}

func fatal(format string, arguments ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", arguments...)
	os.Exit(1)
}
