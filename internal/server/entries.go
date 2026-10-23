package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Lamisator/fundus/internal/markdown"
	"github.com/Lamisator/fundus/internal/store"
	"gopkg.in/yaml.v3"
)

func (s *Server) render(ctx context.Context, src string) string {
	return markdown.Render(src, markdown.Options{Titles: func(ids []int64) map[int64]string { return s.st.Titles(ctx, ids) }})
}

func parseQuery(r *http.Request, viewer int64) store.Query {
	v := r.URL.Query()
	q := store.Query{
		Viewer: viewer,
		Q:      v.Get("q"),
		Kind:   v.Get("kind"),
		Status: v.Get("status"),
		Sort:   v.Get("sort"),
	}
	for _, t := range v["tag"] {
		q.Tags = append(q.Tags, strings.Split(t, ",")...)
	}
	if sp := v.Get("space"); sp != "" {
		if id, err := strconv.ParseInt(sp, 10, 64); err == nil {
			q.SpaceID = &id
		} else if sp == "none" {
			zero := int64(0)
			q.SpaceID = &zero
		}
	}
	q.Starred = truthy(v.Get("starred"))
	q.Trash = truthy(v.Get("trash"))
	q.Facets = truthy(v.Get("facets"))
	if p := v.Get("pinned"); p != "" {
		b := truthy(p)
		q.Pinned = &b
	}
	q.Author, _ = strconv.ParseInt(v.Get("author"), 10, 64)
	q.Limit, _ = strconv.Atoi(v.Get("limit"))
	q.Offset, _ = strconv.Atoi(v.Get("offset"))
	return q
}

func truthy(s string) bool { return s == "1" || s == "true" || s == "yes" }

// spaceParam resolves a ?space= slug to its id.
func (s *Server) spaceParam(r *http.Request, q *store.Query) {
	sp := r.URL.Query().Get("space")
	if sp == "" || q.SpaceID != nil {
		return
	}
	if x, err := s.st.GetSpace(r.Context(), sp); err == nil {
		q.SpaceID = &x.ID
	} else {
		none := int64(-1)
		q.SpaceID = &none
	}
}

func (s *Server) handleListEntries(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r, who(r).ID)
	s.spaceParam(r, &q)
	if q.Trash && !who(r).canWrite() {
		apiError(w, http.StatusForbidden, "forbidden", "editors only")
		return
	}
	res, err := s.st.Search(r.Context(), q)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// handleSearch is /entries with facets on by default.
func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	q := parseQuery(r, who(r).ID)
	s.spaceParam(r, &q)
	if r.URL.Query().Get("facets") == "" {
		q.Facets = true
	}
	q.Trash = false
	res, err := s.st.Search(r.Context(), q)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

type entryDetail struct {
	store.Entry
	Solutions   []store.Solution   `json:"solutions,omitempty"`
	Links       []store.Link       `json:"links,omitempty"`
	Attachments []store.Attachment `json:"attachments,omitempty"`
	Shares      []store.Share      `json:"shares,omitempty"`
}

func (s *Server) loadEntry(w http.ResponseWriter, r *http.Request) (store.Entry, bool) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return store.Entry{}, false
	}
	e, err := s.st.GetEntry(r.Context(), who(r).ID, id)
	if err != nil {
		fail(w, err)
		return e, false
	}
	if e.DeletedAt != nil && !who(r).canWrite() {
		fail(w, store.ErrNotFound)
		return e, false
	}
	return e, true
}

func (s *Server) handleGetEntry(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	v := r.URL.Query()
	if v.Get("render") != "false" && v.Get("render") != "0" {
		e.BodyHTML = s.render(r.Context(), e.Body)
	}
	if truthy(v.Get("count_view")) {
		s.st.CountView(r.Context(), e.ID)
		e.ViewCount++
	}
	d := entryDetail{Entry: e}
	expand := map[string]bool{}
	for _, x := range strings.Split(v.Get("expand"), ",") {
		expand[strings.TrimSpace(x)] = true
	}
	all := expand["all"]
	var err error
	if all || expand["solutions"] {
		if d.Solutions, err = s.st.ListSolutions(r.Context(), who(r).ID, e.ID); err != nil {
			fail(w, err)
			return
		}
		for i := range d.Solutions {
			d.Solutions[i].BodyHTML = s.render(r.Context(), d.Solutions[i].Body)
		}
	}
	if all || expand["links"] {
		if d.Links, err = s.st.Links(r.Context(), who(r).ID, e.ID); err != nil {
			fail(w, err)
			return
		}
	}
	if all || expand["attachments"] {
		if d.Attachments, err = s.st.ListAttachments(r.Context(), e.ID); err != nil {
			fail(w, err)
			return
		}
		for i := range d.Attachments {
			d.Attachments[i].URL = fileURL(d.Attachments[i])
		}
	}
	if (all || expand["shares"]) && who(r).canWrite() {
		if d.Shares, err = s.st.ListShares(r.Context(), e.ID); err != nil {
			fail(w, err)
			return
		}
		for i := range d.Shares {
			d.Shares[i].URL = s.baseURL(r) + "/s/" + d.Shares[i].Token
		}
	}
	writeJSON(w, http.StatusOK, d)
}

// validateEntry fills defaults and checks kind/status/space; cur is the
// existing entry for updates.
func (s *Server) validateEntry(ctx context.Context, in *store.EntryInput, cur *store.Entry) error {
	if in.Kind == nil && cur == nil {
		k := "note"
		in.Kind = &k
	}
	kindKey := ""
	if in.Kind != nil {
		kindKey = *in.Kind
	} else {
		kindKey = cur.Kind
	}
	kind, err := s.st.GetKind(ctx, kindKey)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("unknown kind %q", kindKey)
	} else if err != nil {
		return err
	}
	if in.Title != nil {
		t := strings.TrimSpace(*in.Title)
		if t == "" {
			return errors.New("title must not be empty")
		}
		if len(t) > 300 {
			return errors.New("title is limited to 300 characters")
		}
	} else if cur == nil {
		return errors.New("title is required")
	}
	if in.Body != nil && len(*in.Body) > 2<<20 {
		return errors.New("body is limited to 2 MiB")
	}
	if in.ErrorText != nil && len(*in.ErrorText) > 256<<10 {
		return errors.New("error_text is limited to 256 KiB")
	}
	// Status: must be one of the kind's statuses. Changing the kind resets an
	// incompatible status to the new kind's default.
	status := ""
	if in.Status != nil {
		status = *in.Status
	} else if cur != nil {
		status = cur.Status
	}
	allowed := func(st string) bool {
		for _, x := range kind.Statuses {
			if x == st {
				return true
			}
		}
		return false
	}
	switch {
	case len(kind.Statuses) == 0:
		if in.Status != nil && *in.Status != "" {
			return fmt.Errorf("kind %q has no statuses", kind.Key)
		}
		if cur != nil && cur.Status != "" {
			empty := ""
			in.Status = &empty
		}
	case status == "" || !allowed(status):
		if in.Status != nil && *in.Status != "" {
			return fmt.Errorf("status must be one of %s", strings.Join(kind.Statuses, ", "))
		}
		def := kind.Statuses[0]
		in.Status = &def
	}
	if in.SpaceID != nil {
		if *in.SpaceID == 0 {
			in.SpaceID, in.ClearSpace = nil, true
		} else if !s.st.SpaceExists(ctx, *in.SpaceID) {
			return errors.New("unknown space_id")
		}
	}
	if in.Fields != nil && len(*in.Fields) > 50 {
		return errors.New("at most 50 fields")
	}
	if in.Tags != nil && len(*in.Tags) > 50 {
		return errors.New("at most 50 tags")
	}
	return nil
}

var fileRefRe = regexp.MustCompile(`/files/([A-Za-z0-9_\-]{16,})`)

func (s *Server) claimFiles(ctx context.Context, entryID int64, texts ...string) {
	var ids []string
	for _, t := range texts {
		for _, m := range fileRefRe.FindAllStringSubmatch(t, -1) {
			ids = append(ids, m[1])
		}
	}
	_ = s.st.ClaimAttachments(ctx, entryID, ids)
}

type createEntryInput struct {
	store.EntryInput
	Solutions []struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		Accepted bool   `json:"accepted"`
	} `json:"solutions"`
	Links []struct {
		To  int64  `json:"to"`
		Rel string `json:"rel"`
	} `json:"links"`
}

func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	var in createEntryInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.validateEntry(r.Context(), &in.EntryInput, nil); err != nil {
		badRequest(w, err.Error())
		return
	}
	for _, l := range in.Links {
		if l.Rel != "" && !validRel(l.Rel) {
			badRequest(w, "link rel must be one of "+strings.Join(store.LinkRels, ", "))
			return
		}
	}
	p := who(r)
	e, err := s.st.CreateEntry(r.Context(), p.ID, in.EntryInput)
	if err != nil {
		fail(w, err)
		return
	}
	body := ""
	if in.Body != nil {
		body = *in.Body
	}
	s.claimFiles(r.Context(), e.ID, body)
	for _, sol := range in.Solutions {
		if strings.TrimSpace(sol.Body) == "" {
			continue
		}
		if _, err := s.st.CreateSolution(r.Context(), p.ID, e.ID, sol.Title, sol.Body, sol.Accepted); err != nil {
			_ = s.st.PurgeEntry(r.Context(), e.ID) // all or nothing
			fail(w, err)
			return
		}
		s.claimFiles(r.Context(), e.ID, sol.Body)
	}
	for _, l := range in.Links {
		rel := l.Rel
		if rel == "" {
			rel = "related"
		}
		if l.To != e.ID {
			_ = s.st.AddLink(r.Context(), p.ID, e.ID, l.To, rel)
		}
	}
	if len(in.Solutions) > 0 {
		e, _ = s.st.GetEntry(r.Context(), p.ID, e.ID)
	}
	s.audit(r, "entry.created", strconv.FormatInt(e.ID, 10), e.Title)
	s.hooks.emit("entry.created", e)
	w.Header().Set("Location", "/api/v1/entries/"+strconv.FormatInt(e.ID, 10))
	writeJSON(w, http.StatusCreated, e)
}

func (s *Server) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	cur, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	var in store.EntryInput
	if !readJSON(w, r, &in) {
		return
	}
	if err := s.validateEntry(r.Context(), &in, &cur); err != nil {
		badRequest(w, err.Error())
		return
	}
	e, err := s.st.UpdateEntry(r.Context(), who(r).ID, cur.ID, in)
	if errors.Is(err, store.ErrConflict) {
		apiError(w, http.StatusConflict, "version_conflict",
			fmt.Sprintf("the entry was changed by someone else (now at version %d); reload and merge", cur.Version))
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	if in.Body != nil {
		s.claimFiles(r.Context(), e.ID, *in.Body)
	}
	s.hooks.emit("entry.updated", e)
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	purge := truthy(r.URL.Query().Get("purge"))
	var err error
	if purge {
		if !who(r).isAdmin() {
			apiError(w, http.StatusForbidden, "forbidden", "only admins can purge entries")
			return
		}
		err = s.st.PurgeEntry(r.Context(), e.ID)
	} else {
		err = s.st.TrashEntry(r.Context(), e.ID)
	}
	if err != nil {
		fail(w, err)
		return
	}
	action := "entry.trashed"
	if purge {
		action = "entry.purged"
	}
	s.audit(r, action, strconv.FormatInt(e.ID, 10), e.Title)
	s.hooks.emit("entry.deleted", map[string]any{"id": e.ID, "title": e.Title, "purged": purge})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRestoreEntry(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	if err := s.st.RestoreEntry(r.Context(), id); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "entry.restored", strconv.FormatInt(id, 10), "")
	e, err := s.st.GetEntry(r.Context(), who(r).ID, id)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (s *Server) handlePin(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "id")
		if !ok {
			return
		}
		if err := s.st.SetPinned(r.Context(), id, on); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleStar(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		e, ok := s.loadEntry(w, r)
		if !ok {
			return
		}
		if err := s.st.Star(r.Context(), who(r).ID, e.ID, on); err != nil {
			fail(w, err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// ---------------------------------------------------------------- markdown export

type frontMatter struct {
	ID        int64             `yaml:"id"`
	Kind      string            `yaml:"kind"`
	Title     string            `yaml:"title"`
	Summary   string            `yaml:"summary,omitempty"`
	Status    string            `yaml:"status,omitempty"`
	Space     string            `yaml:"space,omitempty"`
	Tags      []string          `yaml:"tags,omitempty"`
	Fields    map[string]string `yaml:"fields,omitempty"`
	Author    string            `yaml:"author,omitempty"`
	Created   time.Time         `yaml:"created"`
	Updated   time.Time         `yaml:"updated"`
	Version   int               `yaml:"version"`
	SourceURL string            `yaml:"source,omitempty"`
}

// entryMarkdown renders an entry and its solutions as one Markdown file with
// YAML front matter.
func (s *Server) entryMarkdown(ctx context.Context, r *http.Request, e store.Entry, viewer int64) string {
	fm := frontMatter{ID: e.ID, Kind: e.Kind, Title: e.Title, Summary: e.Summary, Status: e.Status, Tags: e.Tags,
		Fields: e.Fields, Created: e.CreatedAt.UTC(), Updated: e.UpdatedAt.UTC(), Version: e.Version,
		SourceURL: s.baseURL(r) + "/e/" + strconv.FormatInt(e.ID, 10)}
	if len(fm.Fields) == 0 {
		fm.Fields = nil
	}
	if e.CreatedBy != nil {
		fm.Author = e.CreatedBy.Username
	}
	if e.SpaceID != nil {
		if sp, err := s.st.GetSpace(ctx, strconv.FormatInt(*e.SpaceID, 10)); err == nil {
			fm.Space = sp.Slug
		}
	}
	y, _ := yaml.Marshal(fm)
	var b strings.Builder
	b.WriteString("---\n")
	b.Write(y)
	b.WriteString("---\n\n# ")
	b.WriteString(e.Title)
	b.WriteString("\n\n")
	if e.Summary != "" {
		b.WriteString("> " + strings.ReplaceAll(e.Summary, "\n", "\n> ") + "\n\n")
	}
	if e.ErrorText != "" {
		b.WriteString("## Error\n\n```text\n" + e.ErrorText + "\n```\n\n")
	}
	b.WriteString(strings.TrimSpace(e.Body))
	b.WriteString("\n")
	sols, _ := s.st.ListSolutions(ctx, viewer, e.ID)
	for _, sol := range sols {
		title := sol.Title
		if title == "" {
			title = "Solution"
		}
		mark := ""
		if sol.Accepted {
			mark = " ✓"
		}
		b.WriteString("\n## " + title + mark + "\n\n" + strings.TrimSpace(sol.Body) + "\n")
	}
	return b.String()
}

func (s *Server) handleEntryMarkdown(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	if truthy(r.URL.Query().Get("download")) {
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%d-%s.md"`, e.ID, slugify(e.Title)))
	}
	_, _ = w.Write([]byte(s.entryMarkdown(r.Context(), r, e, who(r).ID)))
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(slugRe.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "entry"
	}
	return s
}

// ---------------------------------------------------------------- revisions

func (s *Server) handleListRevisions(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	list, err := s.st.ListRevisions(r.Context(), e.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleGetRevision(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		fail(w, store.ErrNotFound)
		return
	}
	rev, err := s.st.GetRevision(r.Context(), e.ID, v)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rev)
}

func (s *Server) handleRestoreRevision(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	v, err := strconv.Atoi(r.PathValue("version"))
	if err != nil {
		fail(w, store.ErrNotFound)
		return
	}
	rev, err := s.st.GetRevision(r.Context(), e.ID, v)
	if err != nil {
		fail(w, err)
		return
	}
	in := store.EntryInput{
		Kind: &rev.Kind, Title: &rev.Title, Summary: &rev.Summary, Body: &rev.Body, ErrorText: &rev.ErrorText,
		Status: &rev.Status, Tags: &rev.Tags, Fields: &rev.Fields, Note: fmt.Sprintf("Restored version %d", v),
	}
	if rev.SpaceID != nil && s.st.SpaceExists(r.Context(), *rev.SpaceID) {
		in.SpaceID = rev.SpaceID
	} else {
		in.ClearSpace = true
	}
	if err := s.validateEntry(r.Context(), &in, &e); err != nil {
		badRequest(w, "cannot restore: "+err.Error())
		return
	}
	ne, err := s.st.UpdateEntry(r.Context(), who(r).ID, e.ID, in)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "entry.reverted", strconv.FormatInt(e.ID, 10), fmt.Sprintf("to version %d", v))
	s.hooks.emit("entry.updated", ne)
	writeJSON(w, http.StatusOK, ne)
}

// ---------------------------------------------------------------- solutions

func (s *Server) handleListSolutions(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	list, err := s.st.ListSolutions(r.Context(), who(r).ID, e.ID)
	if err != nil {
		fail(w, err)
		return
	}
	for i := range list {
		list[i].BodyHTML = s.render(r.Context(), list[i].Body)
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateSolution(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	var in struct {
		Title    string `json:"title"`
		Body     string `json:"body"`
		Accepted bool   `json:"accepted"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if strings.TrimSpace(in.Body) == "" {
		badRequest(w, "body is required")
		return
	}
	if len(in.Body) > 1<<20 || len(in.Title) > 300 {
		badRequest(w, "solution too large")
		return
	}
	sol, err := s.st.CreateSolution(r.Context(), who(r).ID, e.ID, in.Title, in.Body, in.Accepted)
	if err != nil {
		fail(w, err)
		return
	}
	s.claimFiles(r.Context(), e.ID, in.Body)
	sol.BodyHTML = s.render(r.Context(), sol.Body)
	s.hooks.emit("solution.created", map[string]any{"entry_id": e.ID, "entry_title": e.Title, "solution": sol})
	writeJSON(w, http.StatusCreated, sol)
}

func (s *Server) handleUpdateSolution(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	var in struct {
		Title *string `json:"title"`
		Body  *string `json:"body"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Body != nil && strings.TrimSpace(*in.Body) == "" {
		badRequest(w, "body must not be empty")
		return
	}
	sol, err := s.st.UpdateSolution(r.Context(), who(r).ID, id, in.Title, in.Body)
	if err != nil {
		fail(w, err)
		return
	}
	if in.Body != nil {
		s.claimFiles(r.Context(), sol.EntryID, *in.Body)
	}
	sol.BodyHTML = s.render(r.Context(), sol.Body)
	writeJSON(w, http.StatusOK, sol)
}

func (s *Server) handleDeleteSolution(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r, "id")
	if !ok {
		return
	}
	entryID, err := s.st.DeleteSolution(r.Context(), id)
	if err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "solution.deleted", strconv.FormatInt(entryID, 10), strconv.FormatInt(id, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAcceptSolution(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "id")
		if !ok {
			return
		}
		sol, err := s.st.GetSolution(r.Context(), who(r).ID, id)
		if err != nil {
			fail(w, err)
			return
		}
		if err := s.st.AcceptSolution(r.Context(), sol.EntryID, id, on); err != nil {
			fail(w, err)
			return
		}
		if on {
			s.hooks.emit("solution.accepted", map[string]any{"entry_id": sol.EntryID, "solution_id": id})
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleVoteSolution(on bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, ok := pathID(w, r, "id")
		if !ok {
			return
		}
		if _, err := s.st.GetSolution(r.Context(), who(r).ID, id); err != nil {
			fail(w, err)
			return
		}
		if err := s.st.VoteSolution(r.Context(), who(r).ID, id, on); err != nil {
			fail(w, err)
			return
		}
		sol, err := s.st.GetSolution(r.Context(), who(r).ID, id)
		if err != nil {
			fail(w, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"works_for": sol.Votes, "voted": sol.Voted})
	}
}

// ---------------------------------------------------------------- links

func validRel(rel string) bool {
	for _, x := range store.LinkRels {
		if x == rel {
			return true
		}
	}
	return false
}

func (s *Server) handleListLinks(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	links, err := s.st.Links(r.Context(), who(r).ID, e.ID)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, links)
}

func (s *Server) handleAddLink(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	var in struct {
		To  int64  `json:"to"`
		Rel string `json:"rel"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	if in.Rel == "" {
		in.Rel = "related"
	}
	if !validRel(in.Rel) {
		badRequest(w, "rel must be one of "+strings.Join(store.LinkRels, ", "))
		return
	}
	if in.To == e.ID {
		badRequest(w, "an entry cannot link to itself")
		return
	}
	if _, err := s.st.GetEntry(r.Context(), 0, in.To); err != nil {
		badRequest(w, "target entry does not exist")
		return
	}
	if err := s.st.AddLink(r.Context(), who(r).ID, e.ID, in.To, in.Rel); err != nil {
		fail(w, err)
		return
	}
	links, _ := s.st.Links(r.Context(), who(r).ID, e.ID)
	writeJSON(w, http.StatusCreated, links)
}

func (s *Server) handleDeleteLink(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	to, _ := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	rel := r.URL.Query().Get("rel")
	if rel == "" {
		rel = "related"
	}
	if err := s.st.DeleteLink(r.Context(), e.ID, to, rel); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------- similar / render

func (s *Server) handleEntrySimilar(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	list, err := s.st.Similar(r.Context(), who(r).ID, e.Title, e.ErrorText, e.Tags, e.ID, limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleSimilar(w http.ResponseWriter, r *http.Request) {
	v := r.URL.Query()
	var tags []string
	for _, t := range v["tag"] {
		tags = append(tags, strings.Split(t, ",")...)
	}
	exclude, _ := strconv.ParseInt(v.Get("exclude"), 10, 64)
	limit, _ := strconv.Atoi(v.Get("limit"))
	list, err := s.st.Similar(r.Context(), who(r).ID, v.Get("title"), v.Get("error"), tags, exclude, limit)
	if err != nil {
		fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRender(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Markdown string `json:"markdown"`
	}
	if !readJSON(w, r, &in) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"html": s.render(r.Context(), in.Markdown)})
}

// ---------------------------------------------------------------- shares

func (s *Server) handleListShares(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	list, err := s.st.ListShares(r.Context(), e.ID)
	if err != nil {
		fail(w, err)
		return
	}
	for i := range list {
		list[i].URL = s.baseURL(r) + "/s/" + list[i].Token
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleCreateShare(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	var in struct {
		ExpiresIn int `json:"expires_in_days"`
	}
	// The body is optional.
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
		badRequest(w, "invalid JSON body: "+err.Error())
		return
	}
	var exp *time.Time
	if in.ExpiresIn > 0 {
		t := time.Now().Add(time.Duration(in.ExpiresIn) * 24 * time.Hour)
		exp = &t
	}
	sh, err := s.st.CreateShare(r.Context(), who(r).ID, e.ID, exp)
	if err != nil {
		fail(w, err)
		return
	}
	sh.URL = s.baseURL(r) + "/s/" + sh.Token
	s.audit(r, "share.created", strconv.FormatInt(e.ID, 10), e.Title)
	writeJSON(w, http.StatusCreated, sh)
}

func (s *Server) handleDeleteShare(w http.ResponseWriter, r *http.Request) {
	e, ok := s.loadEntry(w, r)
	if !ok {
		return
	}
	if err := s.st.DeleteShare(r.Context(), e.ID, r.PathValue("token")); err != nil {
		fail(w, err)
		return
	}
	s.audit(r, "share.revoked", strconv.FormatInt(e.ID, 10), "")
	w.WriteHeader(http.StatusNoContent)
}

// sortedKeys is a small helper for deterministic output.
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
