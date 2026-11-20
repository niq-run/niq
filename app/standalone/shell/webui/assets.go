package webui

import (
	"embed"
	"io/fs"
)

//go:embed assets/dist/*
var embeddedAssets embed.FS

// AssetsFS exposes the embedded SPA bundle. The browser shell is its only
// host: project processes serve JSON/SSE only.
func AssetsFS() (fs.FS, error) {
	return fs.Sub(embeddedAssets, "assets/dist")
}
