package server

import (
	"bytes"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/Lamisator/fundus/internal/markdown"
	"github.com/Lamisator/fundus/internal/store"
	"github.com/Lamisator/fundus/web"
)

var shareTmpl = template.Must(template.New("share.html").Funcs(template.FuncMap{
	"date": func(t time.Time) string { return t.Format("2 Jan 2006") },
}).ParseFS(web.FS, "templates/share.html"))

type shareView struct {
	Site      string
	Entry     store.Entry
	Kind      store.Kind
	BodyHTML  template.HTML
	Solutions []shareSolution
	Fields    [][2]string
}

type shareSolution struct {
	store.Solution
	HTML template.HTML
}

// handleSharePage renders a read-only public page for a share token.
func (s *Server) handleSharePage(w http.ResponseWriter, r *http.Request) {
	tok := r.PathValue("token")
	id, err := s.st.ResolveShare(r.Context(), tok)
	if err != nil {
		http.Error(w, "This share link is invalid or has expired.", http.StatusNotFound)
		return
	}
	e, err := s.st.GetEntry(r.Context(), 0, id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	kind, _ := s.st.GetKind(r.Context(), e.Kind)
	opt := markdown.Options{
		FileQuery: "share=" + url.QueryEscape(tok),
		// Links to other entries would require a login, so render them as plain text targets.
		Titles: func(ids []int64) map[int64]string { return s.st.Titles(r.Context(), ids) },
	}
	v := shareView{Site: s.cfg.SiteName, Entry: e, Kind: kind, BodyHTML: template.HTML(markdown.Render(e.Body, opt))}
	for _, k := range sortedKeys(e.Fields) {
		v.Fields = append(v.Fields, [2]string{k, e.Fields[k]})
	}
	sols, _ := s.st.ListSolutions(r.Context(), 0, e.ID)
	for _, so := range sols {
		v.Solutions = append(v.Solutions, shareSolution{Solution: so, HTML: template.HTML(markdown.Render(so.Body, opt))})
	}
	var buf bytes.Buffer
	if err := shareTmpl.Execute(&buf, v); err != nil {
		slog.Error("share template", "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Robots-Tag", "noindex")
	_, _ = w.Write(buf.Bytes())
}
