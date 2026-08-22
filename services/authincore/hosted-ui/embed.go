// Package hostedui embeds the built hosted login UI bundle (hosted-ui/dist)
// so the authincore binary can serve the end-user login/registration/recovery
// pages without a separate static host.
package hostedui

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// FS returns the embedded dist/ directory as a root filesystem (so "index.html"
// and "assets/..." resolve at the top level), ready to hand to http.FS.
func FS() (fs.FS, error) {
	return fs.Sub(dist, "dist")
}
