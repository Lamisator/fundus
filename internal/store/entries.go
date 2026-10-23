package store

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type Entry struct {
	ID        int64             `json:"id"`
	Kind      string            `json:"kind"`
	SpaceID   *int64            `json:"space_id"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary"`
	Body      string            `json:"body,omitempty"`
	BodyHTML  string            `json:"body_html,omitempty"`
	ErrorText string            `json:"error_text,omitempty"`
	Status    string            `json:"status"`
	Tags      []string          `json:"tags"`
	Fields    map[string]string `json:"fields"`
	Pinned    bool              `json:"pinned"`
	Version   int               `json:"version"`
	ViewCount int64             `json:"view_count"`
	CreatedBy *UserRef          `json:"created_by"`
	UpdatedBy *UserRef          `json:"updated_by"`
	CreatedAt time.Time         `json:"created_at"`
	UpdatedAt time.Time         `json:"updated_at"`
	DeletedAt *time.Time        `json:"deleted_at,omitempty"`

	SolutionCount int  `json:"solution_count"`
	HasAccepted   bool `json:"has_accepted_solution"`
	Starred       bool `json:"starred"`

	// Search results only.
	Snippet string  `json:"snippet,omitempty"`
	Rank    float64 `json:"rank,omitempty"`
}

// entryCols lists the columns scanned by scanEntry; $1 must be the
// requesting user's id (for the starred flag).
const entryCols = `e.id, e.kind, e.space_id, e.title, e.summary, e.body, e.error_text, e.status, e.tags, e.fields,
	e.pinned, e.version, e.view_count, e.created_at, e.updated_at, e.deleted_at,
	cu.id, coalesce(cu.username, ''), coalesce(cu.display_name, ''),
	uu.id, coalesce(uu.username, ''), coalesce(uu.display_name, ''),
	(SELECT count(*) FROM solutions so WHERE so.entry_id = e.id),
	EXISTS (SELECT 1 FROM solutions so WHERE so.entry_id = e.id AND so.accepted),
	EXISTS (SELECT 1 FROM stars st WHERE st.entry_id = e.id AND st.user_id = $1)`

const entryFrom = ` FROM entries e
	LEFT JOIN users cu ON cu.id = e.created_by
	LEFT JOIN users uu ON uu.id = e.updated_by`

const entrySelect = `SELECT ` + entryCols + entryFrom

func scanEntry(r scanner, extra ...any) (Entry, error) {
	var e Entry
	var cid, uid *int64
	var cname, cdisp, uname, udisp string
	var fields []byte
	dst := []any{&e.ID, &e.Kind, &e.SpaceID, &e.Title, &e.Summary, &e.Body, &e.ErrorText, &e.Status, &e.Tags, &fields,
		&e.Pinned, &e.Version, &e.ViewCount, &e.CreatedAt, &e.UpdatedAt, &e.DeletedAt,
		&cid, &cname, &cdisp, &uid, &uname, &udisp, &e.SolutionCount, &e.HasAccepted, &e.Starred}
	if err := r.Scan(append(dst, extra...)...); err != nil {
		return e, notFound(err)
	}
	if cid != nil {
		e.CreatedBy = &UserRef{ID: *cid, Username: cname, DisplayName: cdisp}
	}
	if uid != nil {
		e.UpdatedBy = &UserRef{ID: *uid, Username: uname, DisplayName: udisp}
	}
	e.Fields = map[string]string{}
	_ = json.Unmarshal(fields, &e.Fields)
	if e.Tags == nil {
		e.Tags = []string{}
	}
	return e, nil
}

func (s *Store) GetEntry(ctx context.Context, viewer, id int64) (Entry, error) {
	return scanEntry(s.db.QueryRow(ctx, entrySelect+` WHERE e.id = $2`, viewer, id))
}

func (s *Store) CountView(ctx context.Context, id int64) {
	_, _ = s.db.Exec(ctx, `UPDATE entries SET view_count = view_count + 1 WHERE id = $1`, id)
}

// EntryInput is used for both create and patch; nil = unchanged.
type EntryInput struct {
	Kind      *string            `json:"kind"`
	SpaceID   *int64             `json:"space_id"`
	Title     *string            `json:"title"`
	Summary   *string            `json:"summary"`
	Body      *string            `json:"body"`
	ErrorText *string            `json:"error_text"`
	Status    *string            `json:"status"`
	Tags      *[]string          `json:"tags"`
	Fields    *map[string]string `json:"fields"`
	Pinned    *bool              `json:"pinned"`
	// Optimistic locking for updates: when set, the update fails with
	// ErrConflict if the entry has moved on.
	Version *int `json:"version"`
	// Revision note ("what changed"), stored with the revision.
	Note string `json:"note"`
	// ClearSpace removes the space (space_id: null cannot be told apart
	// from "absent" in JSON).
	ClearSpace bool `json:"-"`
}

func cleanFields(in map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range in {
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k != "" && v != "" && len(k) <= 64 {
			out[k] = v
		}
	}
	return out
}

func (s *Store) CreateEntry(ctx context.Context, by int64, in EntryInput) (Entry, error) {
	str := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	tags := []string{}
	if in.Tags != nil {
		tags = NormalizeTags(*in.Tags)
	}
	fields := map[string]string{}
	if in.Fields != nil {
		fields = cleanFields(*in.Fields)
	}
	fj, _ := json.Marshal(fields)
	pinned := in.Pinned != nil && *in.Pinned

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback(ctx)
	var id int64
	err = tx.QueryRow(ctx, `INSERT INTO entries (kind, space_id, title, summary, body, error_text, status, tags, fields, pinned, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $11) RETURNING id`,
		str(in.Kind), in.SpaceID, strings.TrimSpace(str(in.Title)), strings.TrimSpace(str(in.Summary)), str(in.Body),
		strings.TrimRight(str(in.ErrorText), " \n\t"), str(in.Status), tags, fj, pinned, by).Scan(&id)
	if err != nil {
		return Entry{}, err
	}
	if err := writeRevision(ctx, tx, id, by, in.Note); err != nil {
		return Entry{}, err
	}
	if err := writeRefs(ctx, tx, id, str(in.Body)); err != nil {
		return Entry{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, err
	}
	return s.GetEntry(ctx, by, id)
}

func (s *Store) UpdateEntry(ctx context.Context, by, id int64, in EntryInput) (Entry, error) {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Entry{}, err
	}
	defer tx.Rollback(ctx)

	var cur int
	if err := tx.QueryRow(ctx, `SELECT version FROM entries WHERE id = $1 FOR UPDATE`, id).Scan(&cur); err != nil {
		return Entry{}, notFound(err)
	}
	if in.Version != nil && *in.Version != cur {
		return Entry{}, ErrConflict
	}
	if unchanged, err := s.isNoop(ctx, tx, id, in); err != nil {
		return Entry{}, err
	} else if unchanged {
		if err := tx.Commit(ctx); err != nil {
			return Entry{}, err
		}
		return s.GetEntry(ctx, by, id)
	}
	var tags []string
	if in.Tags != nil {
		tags = NormalizeTags(*in.Tags)
	}
	var fj []byte
	if in.Fields != nil {
		fj, _ = json.Marshal(cleanFields(*in.Fields))
	}
	trim := func(p *string) *string {
		if p == nil {
			return nil
		}
		t := strings.TrimSpace(*p)
		return &t
	}
	var errText *string
	if in.ErrorText != nil {
		t := strings.TrimRight(*in.ErrorText, " \n\t")
		errText = &t
	}
	_, err = tx.Exec(ctx, `UPDATE entries SET
		kind       = coalesce($2, kind),
		space_id   = CASE WHEN $3 THEN NULL ELSE coalesce($4, space_id) END,
		title      = coalesce($5, title),
		summary    = coalesce($6, summary),
		body       = coalesce($7, body),
		error_text = coalesce($8, error_text),
		status     = coalesce($9, status),
		tags       = coalesce($10, tags),
		fields     = coalesce($11, fields),
		pinned     = coalesce($12, pinned),
		version    = version + 1,
		updated_by = $13,
		updated_at = now()
		WHERE id = $1`,
		id, in.Kind, in.ClearSpace, in.SpaceID, trim(in.Title), trim(in.Summary), in.Body, errText, in.Status,
		tags, fj, in.Pinned, by)
	if err != nil {
		return Entry{}, err
	}
	if err := writeRevision(ctx, tx, id, by, in.Note); err != nil {
		return Entry{}, err
	}
	if in.Body != nil {
		if err := writeRefs(ctx, tx, id, *in.Body); err != nil {
			return Entry{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return Entry{}, err
	}
	return s.GetEntry(ctx, by, id)
}

// isNoop reports whether applying in would leave the entry unchanged, so a
// save without edits does not create an empty revision.
func (s *Store) isNoop(ctx context.Context, tx pgx.Tx, id int64, in EntryInput) (bool, error) {
	var e Entry
	var fields []byte
	err := tx.QueryRow(ctx, `SELECT kind, space_id, title, summary, body, error_text, status, tags, fields, pinned FROM entries WHERE id = $1`, id).
		Scan(&e.Kind, &e.SpaceID, &e.Title, &e.Summary, &e.Body, &e.ErrorText, &e.Status, &e.Tags, &fields, &e.Pinned)
	if err != nil {
		return false, err
	}
	_ = json.Unmarshal(fields, &e.Fields)
	same := func(p *string, v string, trim bool) bool {
		if p == nil {
			return true
		}
		x := *p
		if trim {
			x = strings.TrimSpace(x)
		}
		return x == v
	}
	if !same(in.Kind, e.Kind, false) || !same(in.Title, e.Title, true) || !same(in.Summary, e.Summary, true) ||
		!same(in.Body, e.Body, false) || !same(in.Status, e.Status, false) {
		return false, nil
	}
	if in.ErrorText != nil && strings.TrimRight(*in.ErrorText, " \n\t") != e.ErrorText {
		return false, nil
	}
	if in.ClearSpace && e.SpaceID != nil || in.SpaceID != nil && (e.SpaceID == nil || *in.SpaceID != *e.SpaceID) {
		return false, nil
	}
	if in.Pinned != nil && *in.Pinned != e.Pinned {
		return false, nil
	}
	if in.Tags != nil && strings.Join(NormalizeTags(*in.Tags), "\x00") != strings.Join(e.Tags, "\x00") {
		return false, nil
	}
	if in.Fields != nil {
		nf := cleanFields(*in.Fields)
		if len(nf) != len(e.Fields) {
			return false, nil
		}
		for k, v := range nf {
			if e.Fields[k] != v {
				return false, nil
			}
		}
	}
	return true, nil
}

// SetPinned toggles the pin without creating a revision.
func (s *Store) SetPinned(ctx context.Context, id int64, pinned bool) error {
	tag, err := s.db.Exec(ctx, `UPDATE entries SET pinned = $2 WHERE id = $1`, id, pinned)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func writeRevision(ctx context.Context, tx pgx.Tx, id, by int64, note string) error {
	_, err := tx.Exec(ctx, `INSERT INTO entry_revisions (entry_id, version, kind, space_id, title, summary, body, error_text, status, tags, fields, note, edited_by)
		SELECT id, version, kind, space_id, title, summary, body, error_text, status, tags, fields, $2, $3 FROM entries WHERE id = $1`,
		id, strings.TrimSpace(note), by)
	return err
}

var refRe = regexp.MustCompile(`\[\[(\d+)(?:\|[^\]]*)?\]\]`)

// RefIDs extracts [[123]] / [[123|label]] references from markdown.
func RefIDs(body string) []int64 {
	seen := map[int64]bool{}
	var out []int64
	for _, m := range refRe.FindAllStringSubmatch(body, -1) {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

func writeRefs(ctx context.Context, tx pgx.Tx, id int64, body string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM entry_refs WHERE from_id = $1`, id); err != nil {
		return err
	}
	ids := RefIDs(body)
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `INSERT INTO entry_refs (from_id, to_id)
		SELECT $1, e.id FROM entries e WHERE e.id = ANY($2) AND e.id <> $1 ON CONFLICT DO NOTHING`, id, ids)
	return err
}

// TrashEntry soft-deletes; PurgeEntry removes for good.
func (s *Store) TrashEntry(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE entries SET deleted_at = now() WHERE id = $1 AND deleted_at IS NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) RestoreEntry(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `UPDATE entries SET deleted_at = NULL WHERE id = $1 AND deleted_at IS NOT NULL`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) PurgeEntry(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM entries WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- revisions

type Revision struct {
	Version   int               `json:"version"`
	Kind      string            `json:"kind"`
	SpaceID   *int64            `json:"space_id"`
	Title     string            `json:"title"`
	Summary   string            `json:"summary"`
	Body      string            `json:"body,omitempty"`
	ErrorText string            `json:"error_text,omitempty"`
	Status    string            `json:"status"`
	Tags      []string          `json:"tags"`
	Fields    map[string]string `json:"fields"`
	Note      string            `json:"note"`
	EditedBy  *UserRef          `json:"edited_by"`
	EditedAt  time.Time         `json:"edited_at"`
}

func (s *Store) ListRevisions(ctx context.Context, entryID int64) ([]Revision, error) {
	rows, err := s.db.Query(ctx, `SELECT r.version, r.kind, r.space_id, r.title, r.summary, '', '', r.status, r.tags, r.fields, r.note,
		u.id, coalesce(u.username, ''), coalesce(u.display_name, ''), r.edited_at
		FROM entry_revisions r LEFT JOIN users u ON u.id = r.edited_by
		WHERE r.entry_id = $1 ORDER BY r.version DESC`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) GetRevision(ctx context.Context, entryID int64, version int) (Revision, error) {
	return scanRevision(s.db.QueryRow(ctx, `SELECT r.version, r.kind, r.space_id, r.title, r.summary, r.body, r.error_text, r.status, r.tags, r.fields, r.note,
		u.id, coalesce(u.username, ''), coalesce(u.display_name, ''), r.edited_at
		FROM entry_revisions r LEFT JOIN users u ON u.id = r.edited_by
		WHERE r.entry_id = $1 AND r.version = $2`, entryID, version))
}

func scanRevision(r scanner) (Revision, error) {
	var x Revision
	var fields []byte
	var uid *int64
	var uname, udisp string
	err := r.Scan(&x.Version, &x.Kind, &x.SpaceID, &x.Title, &x.Summary, &x.Body, &x.ErrorText, &x.Status, &x.Tags, &fields, &x.Note,
		&uid, &uname, &udisp, &x.EditedAt)
	if err != nil {
		return x, notFound(err)
	}
	if uid != nil {
		x.EditedBy = &UserRef{ID: *uid, Username: uname, DisplayName: udisp}
	}
	x.Fields = map[string]string{}
	_ = json.Unmarshal(fields, &x.Fields)
	return x, nil
}

// ---------------------------------------------------------------- solutions

type Solution struct {
	ID        int64     `json:"id"`
	EntryID   int64     `json:"entry_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	BodyHTML  string    `json:"body_html,omitempty"`
	Accepted  bool      `json:"accepted"`
	Votes     int       `json:"works_for"`
	Voted     bool      `json:"voted"`
	CreatedBy *UserRef  `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const solutionSelect = `SELECT s.id, s.entry_id, s.title, s.body, s.accepted,
	(SELECT count(*) FROM solution_votes v WHERE v.solution_id = s.id),
	EXISTS (SELECT 1 FROM solution_votes v WHERE v.solution_id = s.id AND v.user_id = $1),
	u.id, coalesce(u.username, ''), coalesce(u.display_name, ''), s.created_at, s.updated_at
	FROM solutions s LEFT JOIN users u ON u.id = s.created_by`

func scanSolution(r scanner) (Solution, error) {
	var x Solution
	var uid *int64
	var uname, udisp string
	err := r.Scan(&x.ID, &x.EntryID, &x.Title, &x.Body, &x.Accepted, &x.Votes, &x.Voted, &uid, &uname, &udisp, &x.CreatedAt, &x.UpdatedAt)
	if err != nil {
		return x, notFound(err)
	}
	if uid != nil {
		x.CreatedBy = &UserRef{ID: *uid, Username: uname, DisplayName: udisp}
	}
	return x, nil
}

func (s *Store) ListSolutions(ctx context.Context, viewer, entryID int64) ([]Solution, error) {
	rows, err := s.db.Query(ctx, solutionSelect+` WHERE s.entry_id = $2
		ORDER BY s.accepted DESC, (SELECT count(*) FROM solution_votes v WHERE v.solution_id = s.id) DESC, s.created_at`, viewer, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Solution{}
	for rows.Next() {
		x, err := scanSolution(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetSolution(ctx context.Context, viewer, id int64) (Solution, error) {
	return scanSolution(s.db.QueryRow(ctx, solutionSelect+` WHERE s.id = $2`, viewer, id))
}

// syncSolutions refreshes the denormalised search text of an entry.
func (s *Store) syncSolutions(ctx context.Context, entryID int64) error {
	_, err := s.db.Exec(ctx, `UPDATE entries SET solutions_text = coalesce((
		SELECT string_agg(title || ' ' || body, E'\n') FROM solutions WHERE entry_id = $1), '') WHERE id = $1`, entryID)
	return err
}

func (s *Store) CreateSolution(ctx context.Context, by, entryID int64, title, body string, accepted bool) (Solution, error) {
	var id int64
	err := s.db.QueryRow(ctx, `INSERT INTO solutions (entry_id, title, body, created_by) VALUES ($1, $2, $3, $4) RETURNING id`,
		entryID, strings.TrimSpace(title), body, by).Scan(&id)
	if err != nil {
		return Solution{}, err
	}
	if accepted {
		if err := s.AcceptSolution(ctx, entryID, id, true); err != nil {
			return Solution{}, err
		}
	}
	if err := s.syncSolutions(ctx, entryID); err != nil {
		return Solution{}, err
	}
	s.touch(ctx, entryID, by)
	return s.GetSolution(ctx, by, id)
}

func (s *Store) UpdateSolution(ctx context.Context, by, id int64, title, body *string) (Solution, error) {
	var entryID int64
	err := s.db.QueryRow(ctx, `UPDATE solutions SET title = coalesce($2, title), body = coalesce($3, body), updated_at = now()
		WHERE id = $1 RETURNING entry_id`, id, title, body).Scan(&entryID)
	if err != nil {
		return Solution{}, notFound(err)
	}
	if err := s.syncSolutions(ctx, entryID); err != nil {
		return Solution{}, err
	}
	s.touch(ctx, entryID, by)
	return s.GetSolution(ctx, by, id)
}

func (s *Store) DeleteSolution(ctx context.Context, id int64) (int64, error) {
	var entryID int64
	err := s.db.QueryRow(ctx, `DELETE FROM solutions WHERE id = $1 RETURNING entry_id`, id).Scan(&entryID)
	if err != nil {
		return 0, notFound(err)
	}
	return entryID, s.syncSolutions(ctx, entryID)
}

// AcceptSolution marks one solution as the accepted one (only one per
// entry). Accepting moves an open/investigating problem to "solved".
func (s *Store) AcceptSolution(ctx context.Context, entryID, id int64, accept bool) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if accept {
		if _, err := tx.Exec(ctx, `UPDATE solutions SET accepted = (id = $2) WHERE entry_id = $1`, entryID, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE entries e SET status = 'solved' FROM kinds k
			WHERE e.id = $1 AND k.key = e.kind AND e.status IN ('', 'open', 'investigating')
			AND 'solved' = ANY(k.statuses)`, entryID); err != nil {
			return err
		}
	} else {
		tag, err := tx.Exec(ctx, `UPDATE solutions SET accepted = FALSE WHERE id = $1 AND entry_id = $2`, id, entryID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
	}
	return tx.Commit(ctx)
}

func (s *Store) VoteSolution(ctx context.Context, userID, id int64, vote bool) error {
	var err error
	if vote {
		_, err = s.db.Exec(ctx, `INSERT INTO solution_votes (solution_id, user_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, id, userID)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM solution_votes WHERE solution_id = $1 AND user_id = $2`, id, userID)
	}
	return err
}

func (s *Store) touch(ctx context.Context, entryID, by int64) {
	_, _ = s.db.Exec(ctx, `UPDATE entries SET updated_at = now(), updated_by = $2 WHERE id = $1`, entryID, by)
}
