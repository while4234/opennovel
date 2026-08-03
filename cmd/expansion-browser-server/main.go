//go:build acceptance

package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/voocel/ainovel-cli/internal/entry/web"
)

func main() {
	root := filepath.Join(os.TempDir(), "ainovel-expansion-browser-acceptance")
	handler, err := web.NewExpansionBrowserAcceptanceHandler(root)
	if err != nil {
		log.Fatal(err)
	}
	defer handler.Close()
	log.Fatal(http.ListenAndServe("127.0.0.1:4182", handler))
}
