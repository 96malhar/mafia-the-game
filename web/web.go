// Package web embeds the static frontend assets into the binary using
// the embed package, so the server ships as a single self-contained
// executable with no runtime dependency on an on-disk web/ directory.
//
// FS is rooted at this directory, so the assets ("index.html",
// "styles.css", the ordered *.js files, and "favicon.png") sit at the
// top level — exactly what http.FileServer(http.FS(FS)) expects to serve
// "/" -> index.html and "/helpers.js" -> helpers.js, etc.
package web

import (
	"embed"
	"io/fs"
)

// The frontend is split into index.html, styles.css, and a set of
// ordered classic scripts (helpers.js → … → main.js); the *.js glob
// keeps this directive from needing an edit each time a script is added
// or renamed.
//
//go:embed index.html favicon.png styles.css *.js
var embedded embed.FS

// FS is the filesystem of static assets served by the HTTP server.
var FS fs.FS = embedded
