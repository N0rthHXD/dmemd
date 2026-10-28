//go:build !webui

package webui

import "net/http"

// NewHandler reports that this binary was built without the management UI.
// It never touches dist or the filesystem.
func NewHandler() (http.Handler, error) {
	return nil, nil
}
