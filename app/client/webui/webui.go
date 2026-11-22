// Package webui is the browser UI of niq: the SPA bundle and the embedded
// filesystem the gateway mounts. It holds no server code — the gateway serves
// these files, and the bundle itself talks to the gateway over HTTP.
package webui

import (
	"embed"
	"io/fs"
)

//go:embed assets/dist/*
var embedded embed.FS

// Assets returns the built SPA bundle, rooted at the dist directory.
func Assets() fs.FS {
	sub, err := fs.Sub(embedded, "assets/dist")
	if err != nil {
		// The embed pattern guarantees this subtree exists.
		panic(err)
	}
	return sub
}
