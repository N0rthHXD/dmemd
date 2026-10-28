package webui

import (
	"bytes"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

var hashedAsset = regexp.MustCompile(`-[A-Za-z0-9_-]{8,}\.[A-Za-z0-9]+$`)

var contentTypes = map[string]string{
	".css":   "text/css; charset=utf-8",
	".html":  "text/html; charset=utf-8",
	".js":    "text/javascript; charset=utf-8",
	".json":  "application/json",
	".map":   "application/json",
	".svg":   "image/svg+xml",
	".woff":  "font/woff",
	".woff2": "font/woff2",
}

type handler struct {
	files fs.FS
	index []byte
}

// newHandler serves one embedded dist tree under /ui/. It is shared by the
// build-tagged embed implementations and intentionally depends only on fs.FS.
func newHandler(files fs.FS) (http.Handler, error) {
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		return nil, errors.New("管理台产物缺少 index.html")
	}
	return &handler{files: files, index: index}, nil
}

func (h *handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if request.URL.Path == "/ui/" {
		h.writeIndex(w, request)
		return
	}
	cleaned := path.Clean(request.URL.Path)
	if cleaned == "/ui" {
		http.Redirect(w, request, "/ui/", http.StatusMovedPermanently)
		return
	}
	if !strings.HasPrefix(cleaned, "/ui/") {
		http.NotFound(w, request)
		return
	}
	relative := strings.TrimPrefix(cleaned, "/ui/")
	if relative == "" || relative == "." {
		h.writeIndex(w, request)
		return
	}
	if info, err := fs.Stat(h.files, relative); err == nil && info.IsDir() {
		http.NotFound(w, request)
		return
	}

	if strings.HasPrefix(relative, "assets/") || path.Ext(relative) != "" {
		h.serveStatic(w, request, relative)
		return
	}
	h.writeIndex(w, request)
}

func (h *handler) serveStatic(w http.ResponseWriter, request *http.Request, relative string) {
	info, err := fs.Stat(h.files, relative)
	if err != nil || info.IsDir() {
		http.NotFound(w, request)
		return
	}
	data, err := fs.ReadFile(h.files, relative)
	if err != nil {
		http.NotFound(w, request)
		return
	}
	contentType := contentTypes[path.Ext(relative)]
	if contentType == "" {
		contentType = mime.TypeByExtension(path.Ext(relative))
	}
	if contentType == "" {
		contentType = http.DetectContentType(data)
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if hashedAsset.MatchString(relative) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, request, relative, info.ModTime(), bytes.NewReader(data))
}

func (h *handler) writeIndex(w http.ResponseWriter, request *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, request, "index.html", time.Time{}, bytes.NewReader(h.index))
}
