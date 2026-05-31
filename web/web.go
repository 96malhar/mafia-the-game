// Package web embeds the static frontend assets into the binary using
// the embed package, so the server ships as a single self-contained
// executable with no runtime dependency on an on-disk web/ directory.
//
// FS is rooted at this directory, so "index.html" and "favicon.png" sit
// at the top level — exactly what http.FileServer(http.FS(FS)) expects
// to serve "/" -> index.html.
package web

import (
	"embed"
	"io/fs"
)

//go:embed index.html favicon.png
var embedded embed.FS

// FS is the filesystem of static assets served by the HTTP server.
var FS fs.FS = embedded
