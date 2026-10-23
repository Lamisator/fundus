package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Lamisator/fundus/internal/store"
)

func fileURL(a store.Attachment) string {
	return "/files/" + a.ID + "/" + url.PathEscape(a.Filename)
}

func (s *Server) filePath(id string) string {
	return filepath.Join(s.cfg.DataDir, "attachments", id[:2], id)
}

func cleanFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, `\`, "/"))
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '/' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." {
		name = "file"
	}
	if len(name) > 180 {
		ext := filepath.Ext(name)
		if len(ext) > 20 {
			ext = ""
		}
		name = name[:180-len(ext)] + ext
	}
	return name
}

func (s *Server) handleUpload(w http.ResponseWriter, r *http.Request) {
	max := s.cfg.MaxUploadMB << 20
	r.Body = http.MaxBytesReader(w, r.Body, max+1<<20)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			apiError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("uploads are limited to %d MB", s.cfg.MaxUploadMB))
			return
		}
		badRequest(w, "expected multipart/form-data with a 'file' field")
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, hdr, err := r.FormFile("file")
	if err != nil {
		badRequest(w, "missing 'file' field")
		return
	}
	defer f.Close()
	if hdr.Size > max {
		apiError(w, http.StatusRequestEntityTooLarge, "too_large", fmt.Sprintf("uploads are limited to %d MB", s.cfg.MaxUploadMB))
		return
	}
	var entryID *int64
	if v := r.FormValue("entry_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			badRequest(w, "invalid entry_id")
			return
		}
		if _, err := s.st.GetEntry(r.Context(), 0, id); err != nil {
			badRequest(w, "entry_id does not exist")
			return
		}
		entryID = &id
	}

	id := store.RandomToken(18)
	path := s.filePath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		fail(w, err)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".upload-*")
	if err != nil {
		fail(w, err)
		return
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	head := make([]byte, 512)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	size, err := io.Copy(io.MultiWriter(tmp, h), io.MultiReader(strings.NewReader(string(head)), f))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fail(w, err)
		return
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		fail(w, err)
		return
	}
	name := cleanFilename(hdr.Filename)
	ctype := mime.TypeByExtension(strings.ToLower(filepath.Ext(name)))
	if ctype == "" {
		ctype = http.DetectContentType(head)
	}
	a := store.Attachment{ID: id, EntryID: entryID, Filename: name, ContentType: ctype, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}
	if err := s.st.AddAttachment(r.Context(), a, who(r).ID); err != nil {
		os.Remove(path)
		fail(w, err)
		return
	}
	a.URL = fileURL(a)
	writeJSON(w, http.StatusCreated, a)
}

// inlineTypes are shown in the browser; everything else downloads.
func inlineType(ct string) bool {
	ct = strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	switch {
	case ct == "image/svg+xml":
		return false // can carry script; served as download
	case strings.HasPrefix(ct, "image/"), strings.HasPrefix(ct, "video/"), strings.HasPrefix(ct, "audio/"):
		return true
	case ct == "application/pdf", ct == "text/plain":
		return true
	}
	return false
}

func (s *Server) handleFile(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if len(id) < 16 || strings.ContainsAny(id, "./\\") {
		http.NotFound(w, r)
		return
	}
	if who(r) == nil {
		share := r.URL.Query().Get("share")
		if share == "" || !s.st.AttachmentVisibleViaShare(r.Context(), id, share) {
			http.Error(w, "sign in to view this file", http.StatusUnauthorized)
			return
		}
	}
	a, err := s.st.GetAttachment(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	f, err := os.Open(s.filePath(a.ID))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fail(w, err)
		return
	}
	ct := a.ContentType
	disp := "attachment"
	if inlineType(ct) {
		disp = "inline"
	} else {
		ct = "application/octet-stream"
	}
	if strings.HasPrefix(ct, "text/plain") {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disp, map[string]string{"filename": a.Filename}))
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; img-src 'self'; media-src 'self'; style-src 'unsafe-inline'")
	w.Header().Set("Cache-Control", "private, max-age=86400")
	w.Header().Set("ETag", `"`+a.SHA256[:32]+`"`)
	http.ServeContent(w, r, "", st.ModTime(), f)
}

func (s *Server) handleListAttachments(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	list, err := s.st.ListAttachments(r.Context(), e.ID)
	if err != nil {
		fail(w, err)
		return
	}
	for i := range list {
		list[i].URL = fileURL(list[i])
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleDeleteAttachment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	a, err := s.st.GetAttachment(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.st.DeleteAttachment(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	_ = os.Remove(s.filePath(a.ID))
	s.audit(r, "attachment.deleted", a.Filename, id)
	w.WriteHeader(http.StatusNoContent)
}

// cleanOrphanFiles drops uploads that never got attached to an entry.
func (s *Server) cleanOrphanFiles(ctx context.Context) {
	ids, err := s.st.OrphanAttachments(ctx)
	if err != nil {
		return
	}
	for _, id := range ids {
		if err := s.st.DeleteAttachment(ctx, id); err == nil {
			_ = os.Remove(s.filePath(id))
		}
	}
	if len(ids) > 0 {
		slog.Info("removed orphaned uploads", "count", len(ids))
	}
}
