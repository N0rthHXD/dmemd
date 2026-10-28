//go:build webui

package webui

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed dist
var embedded embed.FS

// NewHandler returns the embedded management UI. A webui-tagged build without
// a generated dist directory fails at compile time; initialization also checks
// that index.html exists.
func NewHandler() (http.Handler, error) {
	dist, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, err
	}
	return newHandler(dist)
}
