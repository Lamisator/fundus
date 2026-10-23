package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Lamisator/fundus/internal/store"
)

// ---------------------------------------------------------------- tags

func (s *Server) handleListTags(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.st.ListTags(r.Context(), r.URL.Query().Get("prefix"), limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRenameTag(w http.ResponseWriter, r *http.Request) {
	from := r.PathValue("name")
	var in struct {
		Name string `json:"name"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	to := store.NormalizeTags([]string{in.Name})
	if len(to) == 0 {
		badRequest(w, "name is required")
		return
	}
	n, err := s.st.RenameTag(r.Context(), from, to[0])
	if err != nil {
		fail(w, err)
		return
	}
	if n == 0 {
		apiError(w, http.StatusNotFound, "not_found", "no entry carries the tag "+strconv.Quote(from))
		return
	}
	s.audit(r, "tag.renamed", from, to[0])
	writeJSON(w, http.StatusOK, map[string]any{"name": to[0], "entries": n})
}

func (s *Server) handleDeleteTag(w http.ResponseWriter, r *http.Request) {
	n, err := s.st.DeleteTag(r.Context(), r.PathValue("name"))
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "tag.deleted", r.PathValue("name"), "")
	writeJSON(w, http.StatusOK, map[string]any{"entries": n})
}

// ---------------------------------------------------------------- spaces

var (
	slugOK  = regexp.MustCompile(`^[a-z0-9][a-z0-9\-]{0,62}$`)
	colorOK = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	keyOK   = regexp.MustCompile(`^[a-z][a-z0-9_\-]{0,31}$`)
)

func (s *Server) handleListSpaces(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListSpaces(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetSpace(w http.ResponseWriter, r *http.Request) {
	sp, err := s.st.GetSpace(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sp)
}

func validateSpace(x *store.Space) string {
	x.Name = strings.TrimSpace(x.Name)
	if x.Name == "" || len(x.Name) > 80 {
		return "name: 1-80 characters"
	}
	if x.Slug == "" {
		x.Slug = slugify(x.Name)
	}
	if !slugOK.MatchString(x.Slug) {
		return "slug: lowercase letters, digits and dashes"
	}
	if x.Color == "" {
		x.Color = "#5b6cff"
	}
	if !colorOK.MatchString(x.Color) {
		return "color must look like #12abef"
	}
	if x.Icon == "" {
		x.Icon = "folder"
	}
	return ""
}

func (s *Server) handleCreateSpace(w http.ResponseWriter, r *http.Request) {
	var x store.Space
	if !readJSON(w, r, &x) {
		return
	}
	if msg := validateSpace(&x); msg != "" {
		badRequest(w, msg)
		return
	}
	x, err := s.st.CreateSpace(r.Context(), x)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "space.created", x.Slug, x.Name)
	writeJSON(w, http.StatusCreated, x)
}

func (s *Server) handleUpdateSpace(w http.ResponseWriter, r *http.Request) {
	cur, err := s.st.GetSpace(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	var in struct {
		Slug        *string `json:"slug"`
		Name        *string `json:"name"`
		Description *string `json:"description"`
		Icon        *string `json:"icon"`
		Color       *string `json:"color"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	set(&cur.Slug, in.Slug)
	set(&cur.Name, in.Name)
	set(&cur.Description, in.Description)
	set(&cur.Icon, in.Icon)
	set(&cur.Color, in.Color)
	if msg := validateSpace(&cur); msg != "" {
		badRequest(w, msg)
		return
	}
	x, err := s.st.UpdateSpace(r.Context(), cur)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, x)
}

func (s *Server) handleDeleteSpace(w http.ResponseWriter, r *http.Request) {
	sp, err := s.st.GetSpace(r.Context(), r.PathValue("id"))
	if err != nil {
		fail(w, err)
		return
	}
	if err := s.st.DeleteSpace(r.Context(), sp.ID); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "space.deleted", sp.Slug, sp.Name)
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- kinds

func (s *Server) handleListKinds(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListKinds(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handlePutKind(w http.ResponseWriter, r *http.Request) {
	var k store.Kind
	if !readJSON(w, r, &k) {
		return
	}
	k.Key = r.PathValue("key")
	if !keyOK.MatchString(k.Key) {
		badRequest(w, "key: lowercase letter followed by letters, digits, - or _ (max 32)")
		return
	}
	k.Label = strings.TrimSpace(k.Label)
	if k.Label == "" {
		badRequest(w, "label is required")
		return
	}
	if k.Plural == "" {
		k.Plural = k.Label + "s"
	}
	if k.Color == "" {
		k.Color = "#5b6cff"
	}
	if !colorOK.MatchString(k.Color) {
		badRequest(w, "color must look like #12abef")
		return
	}
	if k.Icon == "" {
		k.Icon = "note"
	}
	statuses := []string{}
	for _, st := range k.Statuses {
		st = strings.ToLower(strings.TrimSpace(st))
		if st != "" && keyOK.MatchString(st) {
			statuses = append(statuses, st)
		}
	}
	k.Statuses = statuses
	if k.Sort == 0 {
		k.Sort = 100
	}
	if err := s.st.UpsertKind(r.Context(), k); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "kind.saved", k.Key, k.Label)
	k, _ = s.st.GetKind(r.Context(), k.Key)
	writeJSON(w, http.StatusOK, k)
}

func (s *Server) handleDeleteKind(w http.ResponseWriter, r *http.Request) {
	err := s.st.DeleteKind(r.Context(), r.PathValue("key"))
	if errors.Is(err, store.ErrConflict) {
		apiError(w, http.StatusConflict, "in_use", "entries of this kind still exist (including the trash)")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "kind.deleted", r.PathValue("key"), "")
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- stats

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.st.Stats(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ---------------------------------------------------------------- export / import

type exportSolution struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Accepted  bool      `json:"accepted"`
	CreatedAt time.Time `json:"created_at"`
}

type exportLink struct {
	To  int64  `json:"to"`
	Rel string `json:"rel"`
}

type exportEntry struct {
	ID        int64             `json:"id"`
	Kind      string            `json:"kind"`
	Space     string            `json:"space,omitempty"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary"`
	Body      string            `json:"body"`
	ErrorText string            `json:"error_text"`
	Status    string            `json:"status"`
	Tags      []string          `json:"tags"`
	Fields    map[string]string `json:"fields"`
	Pinned    bool              `json:"pinned"`
	Author    string            `json:"author,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	Solutions []exportSolution  `json:"solutions"`
	Links     []exportLink      `json:"links"`
}

type exportDoc struct {
	Format     string        `json:"format"`
	Version    string        `json:"fundus_version"`
	ExportedAt time.Time     `json:"exported_at"`
	Kinds      []store.Kind  `json:"kinds"`
	Spaces     []store.Space `json:"spaces"`
	Entries    []exportEntry `json:"entries"`
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	doc := exportDoc{Format: "fundus-export/1", Version: Version, ExportedAt: time.Now().UTC(), Entries: []exportEntry{}}
	var err error
	if doc.Kinds, err = s.st.ListKinds(ctx); err != nil {
		fail(w, err)
		return
	}
	if doc.Spaces, err = s.st.ListSpaces(ctx); err != nil {
		fail(w, err)
		return
	}
	slugs := map[int64]string{}
	for _, sp := range doc.Spaces {
		slugs[sp.ID] = sp.Slug
	}
	err = s.st.AllEntries(ctx, func(e store.Entry) error {
		x := exportEntry{ID: e.ID, Kind: e.Kind, Title: e.Title, Summary: e.Summary, Body: e.Body, ErrorText: e.ErrorText,
			Status: e.Status, Tags: e.Tags, Fields: e.Fields, Pinned: e.Pinned, CreatedAt: e.CreatedAt, UpdatedAt: e.UpdatedAt,
			Solutions: []exportSolution{}, Links: []exportLink{}}
		if e.SpaceID != nil {
			x.Space = slugs[*e.SpaceID]
		}
		if e.CreatedBy != nil {
			x.Author = e.CreatedBy.Username
		}
		sols, err := s.st.ListSolutions(ctx, 0, e.ID)
		if err != nil {
			return err
		}
		for _, so := range sols {
			x.Solutions = append(x.Solutions, exportSolution{Title: so.Title, Body: so.Body, Accepted: so.Accepted, CreatedAt: so.CreatedAt})
		}
		links, err := s.st.Links(ctx, 0, e.ID)
		if err != nil {
			return err
		}
		for _, l := range links {
			if l.Direction == "outgoing" {
				x.Links = append(x.Links, exportLink{To: l.Entry.ID, Rel: l.Rel})
			}
		}
		doc.Entries = append(doc.Entries, x)
		return nil
	})
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fundus-export-%s.json"`, time.Now().Format("2006-01-02")))
	writeJSON(w, http.StatusOK, doc)
}

// handleImport loads a fundus-export/1 document. Kinds are upserted, spaces
// matched by slug, entries always created anew (ids are re-assigned and
// links/[[id]] references re-mapped).
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
	var doc exportDoc
	if err := json.NewDecoder(r.Body).Decode(&doc); err != nil {
		badRequest(w, "invalid export document: "+err.Error())
		return
	}
	if doc.Format != "fundus-export/1" {
		badRequest(w, "unsupported format "+strconv.Quote(doc.Format))
		return
	}
	ctx := r.Context()
	me := who(r).ID
	for _, k := range doc.Kinds {
		if !colorOK.MatchString(k.Color) {
			k.Color = "#5b6cff"
		}
		if k.Label = strings.TrimSpace(k.Label); k.Label == "" {
			k.Label = k.Key
		}
		if keyOK.MatchString(k.Key) {
			if _, err := s.st.GetKind(ctx, k.Key); errors.Is(err, store.ErrNotFound) {
				if err := s.st.UpsertKind(ctx, k); err != nil {
					fail(w, err)
					return
				}
			}
		}
	}
	spaceIDs := map[string]int64{}
	existing, _ := s.st.ListSpaces(ctx)
	for _, sp := range existing {
		spaceIDs[sp.Slug] = sp.ID
	}
	for _, sp := range doc.Spaces {
		if _, ok := spaceIDs[sp.Slug]; ok || validateSpace(&sp) != "" {
			continue
		}
		created, err := s.st.CreateSpace(ctx, sp)
		if err != nil {
			fail(w, err)
			return
		}
		spaceIDs[created.Slug] = created.ID
	}
	idMap := map[int64]int64{}
	var created []int64
	for _, x := range doc.Entries {
		x := x
		in := store.EntryInput{Kind: &x.Kind, Title: &x.Title, Summary: &x.Summary, Body: &x.Body, ErrorText: &x.ErrorText,
			Status: &x.Status, Tags: &x.Tags, Fields: &x.Fields, Pinned: &x.Pinned, Note: "Imported"}
		if id, ok := spaceIDs[x.Space]; ok {
			in.SpaceID = &id
		}
		if err := s.validateEntry(ctx, &in, nil); err != nil {
			badRequest(w, fmt.Sprintf("entry %d (%s): %v", x.ID, x.Title, err))
			return
		}
		e, err := s.st.CreateEntry(ctx, me, in)
		if err != nil {
			fail(w, err)
			return
		}
		idMap[x.ID] = e.ID
		created = append(created, e.ID)
		for _, so := range x.Solutions {
			if _, err := s.st.CreateSolution(ctx, me, e.ID, so.Title, so.Body, so.Accepted); err != nil {
				fail(w, err)
				return
			}
		}
	}
	// Second pass: links and [[id]] references point at the new ids.
	refs := regexp.MustCompile(`\[\[(\d+)((?:\|[^\]]*)?)\]\]`)
	for _, x := range doc.Entries {
		nid := idMap[x.ID]
		for _, l := range x.Links {
			if to, ok := idMap[l.To]; ok && validRel(l.Rel) {
				_ = s.st.AddLink(ctx, me, nid, to, l.Rel)
			}
		}
		if strings.Contains(x.Body, "[[") {
			body := refs.ReplaceAllStringFunc(x.Body, func(m string) string {
				sm := refs.FindStringSubmatch(m)
				old, _ := strconv.ParseInt(sm[1], 10, 64)
				if n, ok := idMap[old]; ok {
					return fmt.Sprintf("[[%d%s]]", n, sm[2])
				}
				return m
			})
			if body != x.Body {
				_, _ = s.st.UpdateEntry(ctx, me, nid, store.EntryInput{Body: &body, Note: "Imported (re-mapped references)"})
			}
		}
	}
	s.audit(r, "import", "", fmt.Sprintf("%d entries", len(created)))
	writeJSON(w, http.StatusOK, map[string]any{"imported": len(created), "ids": idMap})
}

// ---------------------------------------------------------------- webhooks

var webhookEvents = []string{"*", "entry.created", "entry.updated", "entry.deleted", "solution.created", "solution.accepted"}

func validEvents(ev []string) bool {
	for _, e := range ev {
		ok := false
		for _, k := range webhookEvents {
			ok = ok || e == k
		}
		if !ok {
			return false
		}
	}
	return len(ev) > 0
}

func validHookURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != ""
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	list, err := s.st.ListWebhooks(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var in store.Webhook
	if !readJSON(w, r, &in) {
		return
	}
	if !validHookURL(in.URL) {
		badRequest(w, "url must be an http(s) URL")
		return
	}
	if len(in.Events) == 0 {
		in.Events = []string{"*"}
	}
	if !validEvents(in.Events) {
		badRequest(w, "events: "+strings.Join(webhookEvents, ", "))
		return
	}
	if in.Secret == "" {
		in.Secret = store.RandomToken(24)
	}
	in.Active = true
	hook, err := s.st.CreateWebhook(r.Context(), in)
	if err != nil {
		fail(w, err)
		return
	}
	s.hooks.reload()
	s.audit(r, "webhook.created", hook.URL, "")
	writeJSON(w, http.StatusCreated, hook)
}

func (s *Server) handleUpdateWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	list, err := s.st.ListWebhooks(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	var cur *store.Webhook
	for i := range list {
		if list[i].ID == id {
			cur = &list[i]
		}
	}
	if cur == nil {
		fail(w, store.ErrNotFound)
		return
	}
	var in struct {
		URL    *string   `json:"url"`
		Events *[]string `json:"events"`
		Active *bool     `json:"active"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.URL != nil {
		cur.URL = *in.URL
	}
	if in.Events != nil {
		cur.Events = *in.Events
	}
	if in.Active != nil {
		cur.Active = *in.Active
	}
	if !validHookURL(cur.URL) || !validEvents(cur.Events) {
		badRequest(w, "invalid url or events")
		return
	}
	if err := s.st.UpdateWebhook(r.Context(), *cur); err != nil {
		fail(w, err)
		return
	}
	s.hooks.reload()
	writeJSON(w, http.StatusOK, cur)
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.st.DeleteWebhook(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	s.hooks.reload()
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTestWebhook(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	list, err := s.st.ListWebhooks(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	for _, h := range list {
		if h.ID == id {
			status := s.hooks.deliver(r.Context(), h, "ping", map[string]string{"message": "Hello from Fundus"})
			writeJSON(w, http.StatusOK, map[string]string{"status": status})
			return
		}
	}
	fail(w, store.ErrNotFound)
}
