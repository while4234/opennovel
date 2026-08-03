package web

import (
	"embed"
	"io/fs"
)

//go:embed static
var embeddedStatic embed.FS

func StaticFS() fs.FS {
	static, err := fs.Sub(embeddedStatic, "static")
	if err != nil {
		return embeddedStatic
	}
	return static
}
