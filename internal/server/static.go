package server

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"sync"

	"github.com/Lamisator/fundus/web"
	"gopkg.in/yaml.v3"
)

// handleStatic serves /static/* from the embedded files and the single-page
// app shell for every other path.
func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	p := r.URL.Path
	if strings.HasPrefix(p, "/static/") {
		name := strings.TrimPrefix(path.Clean(p), "/static/")
		if _, err := fs.Stat(s.static, name); err != nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// ES modules import each other without a version suffix, so make
			// browsers revalidate (cheaply, via ETag) after an upgrade.
			w.Header().Set("Cache-Control", "no-cache")
		}
		w.Header().Set("ETag", s.etag(name))
		http.ServeFileFS(w, r, s.static, name)
		return
	}
	switch p {
	case "/favicon.ico", "/favicon.svg":
		w.Header().Set("Cache-Control", "public, max-age=86400")
		http.ServeFileFS(w, r, s.static, "img/logo.svg")
		return
	case "/robots.txt":
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("User-agent: *\nDisallow: /\n"))
		return
	}
	if path.Ext(p) != "" && !strings.HasPrefix(p, "/e/") {
		http.NotFound(w, r)
		return
	}
	s.serveShell(w, r)
}

var etags sync.Map

func (s *Server) etag(name string) string {
	if v, ok := etags.Load(name); ok {
		return v.(string)
	}
	b, _ := fs.ReadFile(s.static, name)
	sum := sha256.Sum256(b)
	tag := `"` + hex.EncodeToString(sum[:8]) + `"`
	etags.Store(name, tag)
	return tag
}

var (
	shellOnce sync.Once
	shellHTML []byte
)

func (s *Server) serveShell(w http.ResponseWriter, r *http.Request) {
	shellOnce.Do(func() {
		b, _ := fs.ReadFile(s.static, "index.html")
		b = bytes.ReplaceAll(b, []byte("{{VERSION}}"), []byte(Version))
		b = bytes.ReplaceAll(b, []byte("{{SITE}}"), []byte(htmlEscape(s.cfg.SiteName)))
		shellHTML = b
	})
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write(shellHTML)
}

func htmlEscape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;").Replace(s)
}

func (s *Server) handleOpenAPIYAML(w http.ResponseWriter, r *http.Request) {
	b, _ := fs.ReadFile(web.FS, "openapi.yaml")
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) handleOpenAPIJSON(w http.ResponseWriter, r *http.Request) {
	b, _ := fs.ReadFile(web.FS, "openapi.yaml")
	var doc map[string]any
	if err := yaml.Unmarshal(b, &doc); err != nil {
		fail(w, err)
		return
	}
	doc["servers"] = []map[string]string{{"url": s.baseURL(r), "description": "this instance"}}
	if info, ok := doc["info"].(map[string]any); ok {
		info["version"] = Version
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	_ = enc.Encode(doc)
}
