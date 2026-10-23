package store

import (
	"context"
	"encoding/json"
	"strings"
	"time"
)

// ---------------------------------------------------------------- spaces

type Space struct {
	ID          int64     `json:"id"`
	Slug        string    `json:"slug"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Icon        string    `json:"icon"`
	Color       string    `json:"color"`
	CreatedAt   time.Time `json:"created_at"`
	EntryCount  int       `json:"entry_count"`
}

func (s *Store) ListSpaces(ctx context.Context) ([]Space, error) {
	rows, err := s.db.Query(ctx, `SELECT sp.id, sp.slug, sp.name, sp.description, sp.icon, sp.color, sp.created_at,
		(SELECT count(*) FROM entries e WHERE e.space_id = sp.id AND e.deleted_at IS NULL)
		FROM spaces sp ORDER BY lower(sp.name)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Space{}
	for rows.Next() {
		var x Space
		if err := rows.Scan(&x.ID, &x.Slug, &x.Name, &x.Description, &x.Icon, &x.Color, &x.CreatedAt, &x.EntryCount); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) GetSpace(ctx context.Context, idOrSlug string) (Space, error) {
	var x Space
	err := s.db.QueryRow(ctx, `SELECT sp.id, sp.slug, sp.name, sp.description, sp.icon, sp.color, sp.created_at,
		(SELECT count(*) FROM entries e WHERE e.space_id = sp.id AND e.deleted_at IS NULL)
		FROM spaces sp WHERE sp.slug = $1 OR sp.id::text = $1`, idOrSlug).
		Scan(&x.ID, &x.Slug, &x.Name, &x.Description, &x.Icon, &x.Color, &x.CreatedAt, &x.EntryCount)
	return x, notFound(err)
}

func (s *Store) CreateSpace(ctx context.Context, x Space) (Space, error) {
	err := s.db.QueryRow(ctx, `INSERT INTO spaces (slug, name, description, icon, color) VALUES ($1, $2, $3, $4, $5)
		RETURNING id, created_at`, x.Slug, x.Name, x.Description, x.Icon, x.Color).Scan(&x.ID, &x.CreatedAt)
	if isUnique(err) {
		return x, ErrConflict
	}
	return x, err
}

func (s *Store) UpdateSpace(ctx context.Context, x Space) (Space, error) {
	tag, err := s.db.Exec(ctx, `UPDATE spaces SET slug = $2, name = $3, description = $4, icon = $5, color = $6 WHERE id = $1`,
		x.ID, x.Slug, x.Name, x.Description, x.Icon, x.Color)
	if isUnique(err) {
		return x, ErrConflict
	}
	if err == nil && tag.RowsAffected() == 0 {
		return x, ErrNotFound
	}
	if err != nil {
		return x, err
	}
	return s.GetSpace(ctx, x.Slug)
}

func (s *Store) DeleteSpace(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM spaces WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- kinds

type KindField struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Placeholder string `json:"placeholder"`
}

type Kind struct {
	Key          string      `json:"key"`
	Label        string      `json:"label"`
	Plural       string      `json:"plural"`
	Icon         string      `json:"icon"`
	Color        string      `json:"color"`
	Description  string      `json:"description"`
	Template     string      `json:"template"`
	Statuses     []string    `json:"statuses"`
	HasSolutions bool        `json:"has_solutions"`
	Fields       []KindField `json:"fields"`
	Sort         int         `json:"sort"`
	EntryCount   int         `json:"entry_count"`
}

func (s *Store) ListKinds(ctx context.Context) ([]Kind, error) {
	rows, err := s.db.Query(ctx, `SELECT k.key, k.label, k.plural, k.icon, k.color, k.description, k.template, k.statuses,
		k.has_solutions, k.fields, k.sort,
		(SELECT count(*) FROM entries e WHERE e.kind = k.key AND e.deleted_at IS NULL)
		FROM kinds k ORDER BY k.sort, k.label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Kind{}
	for rows.Next() {
		var k Kind
		var fields []byte
		if err := rows.Scan(&k.Key, &k.Label, &k.Plural, &k.Icon, &k.Color, &k.Description, &k.Template, &k.Statuses,
			&k.HasSolutions, &fields, &k.Sort, &k.EntryCount); err != nil {
			return nil, err
		}
		k.Fields = []KindField{}
		_ = json.Unmarshal(fields, &k.Fields)
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) GetKind(ctx context.Context, key string) (Kind, error) {
	kinds, err := s.ListKinds(ctx)
	if err != nil {
		return Kind{}, err
	}
	for _, k := range kinds {
		if k.Key == key {
			return k, nil
		}
	}
	return Kind{}, ErrNotFound
}

// UpsertKind creates or replaces a kind definition.
func (s *Store) UpsertKind(ctx context.Context, k Kind) error {
	if k.Fields == nil {
		k.Fields = []KindField{}
	}
	if k.Statuses == nil {
		k.Statuses = []string{}
	}
	fj, _ := json.Marshal(k.Fields)
	_, err := s.db.Exec(ctx, `INSERT INTO kinds (key, label, plural, icon, color, description, template, statuses, has_solutions, fields, sort)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		ON CONFLICT (key) DO UPDATE SET label = $2, plural = $3, icon = $4, color = $5, description = $6, template = $7,
		statuses = $8, has_solutions = $9, fields = $10, sort = $11`,
		k.Key, k.Label, k.Plural, k.Icon, k.Color, k.Description, k.Template, k.Statuses, k.HasSolutions, fj, k.Sort)
	return err
}

func (s *Store) DeleteKind(ctx context.Context, key string) error {
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM entries WHERE kind = $1`, key).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrConflict
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM kinds WHERE key = $1`, key)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ---------------------------------------------------------------- tags

func (s *Store) ListTags(ctx context.Context, prefix string, limit int) ([]Count, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	rows, err := s.db.Query(ctx, `SELECT t, count(*) FROM entries e, unnest(e.tags) t
		WHERE e.deleted_at IS NULL AND t LIKE $1 || '%' GROUP BY t ORDER BY 2 DESC, 1 LIMIT $2`,
		strings.ToLower(prefix), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Key, &c.Count); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// RenameTag renames (or merges, if the target exists) a tag everywhere.
func (s *Store) RenameTag(ctx context.Context, from, to string) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE entries SET tags = ARRAY(SELECT DISTINCT unnest(array_replace(tags, $1, $2)))
		WHERE $1 = ANY(tags)`, from, to)
	return tag.RowsAffected(), err
}

func (s *Store) DeleteTag(ctx context.Context, name string) (int64, error) {
	tag, err := s.db.Exec(ctx, `UPDATE entries SET tags = array_remove(tags, $1) WHERE $1 = ANY(tags)`, name)
	return tag.RowsAffected(), err
}

// ---------------------------------------------------------------- stars

func (s *Store) Star(ctx context.Context, userID, entryID int64, on bool) error {
	var err error
	if on {
		_, err = s.db.Exec(ctx, `INSERT INTO stars (user_id, entry_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`, userID, entryID)
	} else {
		_, err = s.db.Exec(ctx, `DELETE FROM stars WHERE user_id = $1 AND entry_id = $2`, userID, entryID)
	}
	return err
}

// ---------------------------------------------------------------- links

type Link struct {
	Rel       string `json:"rel"`
	Direction string `json:"direction"` // outgoing | incoming | mention
	Entry     Entry  `json:"entry"`
}

var LinkRels = []string{"related", "duplicate-of", "caused-by", "depends-on", "supersedes"}

func (s *Store) AddLink(ctx context.Context, by, from, to int64, rel string) error {
	_, err := s.db.Exec(ctx, `INSERT INTO entry_links (from_id, to_id, rel, created_by) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, from, to, rel, by)
	return err
}

func (s *Store) DeleteLink(ctx context.Context, from, to int64, rel string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM entry_links WHERE ((from_id = $1 AND to_id = $2) OR (from_id = $2 AND to_id = $1)) AND rel = $3`, from, to, rel)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// Links returns explicit links in both directions plus [[id]] backlinks.
func (s *Store) Links(ctx context.Context, viewer, id int64) ([]Link, error) {
	sql := `SELECT x.rel, x.dir, ` + entryCols + ` FROM (
		SELECT rel, 'outgoing' AS dir, to_id AS other, created_at FROM entry_links WHERE from_id = $2
		UNION ALL SELECT rel, 'incoming', from_id, created_at FROM entry_links WHERE to_id = $2
		UNION ALL SELECT 'mentioned-by', 'mention', from_id, now() FROM entry_refs WHERE to_id = $2
	) x JOIN entries e ON e.id = x.other
	LEFT JOIN users cu ON cu.id = e.created_by
	LEFT JOIN users uu ON uu.id = e.updated_by
	WHERE e.deleted_at IS NULL ORDER BY x.dir, x.created_at`
	rows, err := s.db.Query(ctx, sql, viewer, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Link{}
	for rows.Next() {
		var l Link
		var e Entry
		// scanEntry expects the entry columns first; scan rel/dir separately.
		var rel, dir string
		e, err = scanEntryPrefixed(rows, &rel, &dir)
		if err != nil {
			return nil, err
		}
		e.Body, e.ErrorText = "", ""
		l.Rel, l.Direction, l.Entry = rel, dir, e
		out = append(out, l)
	}
	return out, rows.Err()
}

func scanEntryPrefixed(r scanner, pre ...any) (Entry, error) {
	return scanEntry(prefixScanner{r, pre})
}

type prefixScanner struct {
	r   scanner
	pre []any
}

func (p prefixScanner) Scan(dst ...any) error { return p.r.Scan(append(p.pre, dst...)...) }

// ---------------------------------------------------------------- attachments

type Attachment struct {
	ID          string    `json:"id"`
	EntryID     *int64    `json:"entry_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256"`
	CreatedAt   time.Time `json:"created_at"`
	URL         string    `json:"url"`
}

func (s *Store) AddAttachment(ctx context.Context, a Attachment, by int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO attachments (id, entry_id, filename, content_type, size, sha256, uploaded_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, a.ID, a.EntryID, a.Filename, a.ContentType, a.Size, a.SHA256, by)
	return err
}

func (s *Store) GetAttachment(ctx context.Context, id string) (Attachment, error) {
	var a Attachment
	err := s.db.QueryRow(ctx, `SELECT id, entry_id, filename, content_type, size, sha256, created_at FROM attachments WHERE id = $1`, id).
		Scan(&a.ID, &a.EntryID, &a.Filename, &a.ContentType, &a.Size, &a.SHA256, &a.CreatedAt)
	return a, notFound(err)
}

func (s *Store) ListAttachments(ctx context.Context, entryID int64) ([]Attachment, error) {
	rows, err := s.db.Query(ctx, `SELECT id, entry_id, filename, content_type, size, sha256, created_at FROM attachments
		WHERE entry_id = $1 ORDER BY created_at`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Attachment{}
	for rows.Next() {
		var a Attachment
		if err := rows.Scan(&a.ID, &a.EntryID, &a.Filename, &a.ContentType, &a.Size, &a.SHA256, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ClaimAttachments binds uploads referenced in a body (uploaded while the
// entry did not exist yet) to the entry.
func (s *Store) ClaimAttachments(ctx context.Context, entryID int64, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := s.db.Exec(ctx, `UPDATE attachments SET entry_id = $1 WHERE id = ANY($2) AND entry_id IS NULL`, entryID, ids)
	return err
}

func (s *Store) DeleteAttachment(ctx context.Context, id string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM attachments WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// OrphanAttachments lists uploads that never got bound to an entry (or whose
// entry was purged) and are older than a day.
func (s *Store) OrphanAttachments(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM attachments WHERE entry_id IS NULL AND created_at < now() - interval '1 day'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// AttachmentVisibleViaShare reports whether an attachment belongs to the
// entry a share token exposes.
func (s *Store) AttachmentVisibleViaShare(ctx context.Context, attID, token string) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM attachments a JOIN share_links sl ON sl.entry_id = a.entry_id
		WHERE a.id = $1 AND sl.token = $2 AND (sl.expires_at IS NULL OR sl.expires_at > now()))`, attID, token).Scan(&ok)
	return ok
}

// ---------------------------------------------------------------- share links

type Share struct {
	Token     string     `json:"token"`
	EntryID   int64      `json:"entry_id"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt *time.Time `json:"expires_at"`
	Views     int64      `json:"views"`
	URL       string     `json:"url"`
}

func (s *Store) CreateShare(ctx context.Context, by, entryID int64, expires *time.Time) (Share, error) {
	sh := Share{Token: RandomToken(18), EntryID: entryID, ExpiresAt: expires}
	err := s.db.QueryRow(ctx, `INSERT INTO share_links (token, entry_id, created_by, expires_at) VALUES ($1, $2, $3, $4) RETURNING created_at`,
		sh.Token, entryID, by, expires).Scan(&sh.CreatedAt)
	return sh, err
}

func (s *Store) ListShares(ctx context.Context, entryID int64) ([]Share, error) {
	rows, err := s.db.Query(ctx, `SELECT token, entry_id, created_at, expires_at, views FROM share_links WHERE entry_id = $1 ORDER BY created_at`, entryID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Share{}
	for rows.Next() {
		var x Share
		if err := rows.Scan(&x.Token, &x.EntryID, &x.CreatedAt, &x.ExpiresAt, &x.Views); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}

func (s *Store) DeleteShare(ctx context.Context, entryID int64, token string) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM share_links WHERE token = $1 AND entry_id = $2`, token, entryID)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// ResolveShare returns the entry id behind a live share token and counts the view.
func (s *Store) ResolveShare(ctx context.Context, token string) (int64, error) {
	var id int64
	err := s.db.QueryRow(ctx, `UPDATE share_links sl SET views = views + 1 FROM entries e
		WHERE sl.token = $1 AND e.id = sl.entry_id AND e.deleted_at IS NULL
		AND (sl.expires_at IS NULL OR sl.expires_at > now()) RETURNING sl.entry_id`, token).Scan(&id)
	return id, notFound(err)
}

// ---------------------------------------------------------------- webhooks

type Webhook struct {
	ID         int64      `json:"id"`
	URL        string     `json:"url"`
	Secret     string     `json:"secret,omitempty"`
	Events     []string   `json:"events"`
	Active     bool       `json:"active"`
	CreatedAt  time.Time  `json:"created_at"`
	LastStatus string     `json:"last_status"`
	LastAt     *time.Time `json:"last_at"`
}

func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.Query(ctx, `SELECT id, url, secret, events, active, created_at, last_status, last_at FROM webhooks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		var w Webhook
		if err := rows.Scan(&w.ID, &w.URL, &w.Secret, &w.Events, &w.Active, &w.CreatedAt, &w.LastStatus, &w.LastAt); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) CreateWebhook(ctx context.Context, w Webhook) (Webhook, error) {
	err := s.db.QueryRow(ctx, `INSERT INTO webhooks (url, secret, events, active) VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		w.URL, w.Secret, w.Events, w.Active).Scan(&w.ID, &w.CreatedAt)
	return w, err
}

func (s *Store) UpdateWebhook(ctx context.Context, w Webhook) error {
	tag, err := s.db.Exec(ctx, `UPDATE webhooks SET url = $2, events = $3, active = $4 WHERE id = $1`, w.ID, w.URL, w.Events, w.Active)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) DeleteWebhook(ctx context.Context, id int64) error {
	tag, err := s.db.Exec(ctx, `DELETE FROM webhooks WHERE id = $1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) WebhookResult(ctx context.Context, id int64, status string) {
	_, _ = s.db.Exec(ctx, `UPDATE webhooks SET last_status = $2, last_at = now() WHERE id = $1`, id, status)
}

// ---------------------------------------------------------------- audit log

type AuditEvent struct {
	ID     int64     `json:"id"`
	At     time.Time `json:"at"`
	User   *UserRef  `json:"user"`
	Action string    `json:"action"`
	Target string    `json:"target"`
	Detail string    `json:"detail"`
	IP     string    `json:"ip"`
}

func (s *Store) Audit(ctx context.Context, userID int64, action, target, detail, ip string) {
	var uid *int64
	if userID != 0 {
		uid = &userID
	}
	_, _ = s.db.Exec(ctx, `INSERT INTO audit_log (user_id, action, target, detail, ip) VALUES ($1, $2, $3, $4, $5)`,
		uid, action, target, detail, ip)
}

func (s *Store) ListAudit(ctx context.Context, limit, offset int) ([]AuditEvent, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.Query(ctx, `SELECT a.id, a.at, u.id, coalesce(u.username, ''), coalesce(u.display_name, ''), a.action, a.target, a.detail, a.ip
		FROM audit_log a LEFT JOIN users u ON u.id = a.user_id ORDER BY a.at DESC LIMIT $1 OFFSET $2`, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditEvent{}
	for rows.Next() {
		var a AuditEvent
		var uid *int64
		var un, ud string
		if err := rows.Scan(&a.ID, &a.At, &uid, &un, &ud, &a.Action, &a.Target, &a.Detail, &a.IP); err != nil {
			return nil, err
		}
		if uid != nil {
			a.User = &UserRef{ID: *uid, Username: un, DisplayName: ud}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------- stats

type Stats struct {
	Entries      int     `json:"entries"`
	Problems     int     `json:"problems_open"`
	Solved       int     `json:"problems_solved"`
	Solutions    int     `json:"solutions"`
	Tags         int     `json:"tags"`
	Attachments  int     `json:"attachments"`
	AttachBytes  int64   `json:"attachment_bytes"`
	Users        int     `json:"users"`
	Trash        int     `json:"trash"`
	Activity     []Count `json:"activity"` // edits per day, last 365 days
	ThisWeek     int     `json:"edits_this_week"`
	SolveRateDay float64 `json:"median_days_to_solve"`
}

func (s *Store) Stats(ctx context.Context) (Stats, error) {
	var st Stats
	err := s.db.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM entries WHERE deleted_at IS NULL),
		(SELECT count(*) FROM entries WHERE deleted_at IS NULL AND status IN ('open', 'investigating')),
		(SELECT count(*) FROM entries WHERE deleted_at IS NULL AND status IN ('solved', 'workaround')),
		(SELECT count(*) FROM solutions),
		(SELECT count(DISTINCT t) FROM entries, unnest(tags) t WHERE deleted_at IS NULL),
		(SELECT count(*) FROM attachments),
		(SELECT coalesce(sum(size), 0) FROM attachments),
		(SELECT count(*) FROM users),
		(SELECT count(*) FROM entries WHERE deleted_at IS NOT NULL),
		(SELECT count(*) FROM entry_revisions WHERE edited_at > now() - interval '7 days'),
		coalesce((SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY extract(epoch FROM s.created_at - e.created_at) / 86400)
			FROM solutions s JOIN entries e ON e.id = s.entry_id WHERE s.accepted), 0)`).
		Scan(&st.Entries, &st.Problems, &st.Solved, &st.Solutions, &st.Tags, &st.Attachments, &st.AttachBytes, &st.Users, &st.Trash,
			&st.ThisWeek, &st.SolveRateDay)
	if err != nil {
		return st, err
	}
	rows, err := s.db.Query(ctx, `SELECT to_char(d, 'YYYY-MM-DD'), count(r.id)
		FROM generate_series(current_date - 364, current_date, interval '1 day') d
		LEFT JOIN entry_revisions r ON r.edited_at >= d AND r.edited_at < d + interval '1 day'
		GROUP BY d ORDER BY d`)
	if err != nil {
		return st, err
	}
	defer rows.Close()
	st.Activity = []Count{}
	for rows.Next() {
		var c Count
		if err := rows.Scan(&c.Key, &c.Count); err != nil {
			return st, err
		}
		st.Activity = append(st.Activity, c)
	}
	return st, rows.Err()
}

// ---------------------------------------------------------------- export

// AllEntries streams every live entry with its solutions for export.
func (s *Store) AllEntries(ctx context.Context, fn func(Entry) error) error {
	rows, err := s.db.Query(ctx, entrySelect+` WHERE e.deleted_at IS NULL ORDER BY e.id`, int64(0))
	if err != nil {
		return err
	}
	var all []Entry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			rows.Close()
			return err
		}
		all = append(all, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, e := range all {
		if err := fn(e); err != nil {
			return err
		}
	}
	return nil
}

// Titles maps entry ids to titles, for rendering [[id]] references.
func (s *Store) Titles(ctx context.Context, ids []int64) map[int64]string {
	out := map[int64]string{}
	rows, err := s.db.Query(ctx, `SELECT id, title FROM entries WHERE id = ANY($1) AND deleted_at IS NULL`, ids)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var t string
		if rows.Scan(&id, &t) == nil {
			out[id] = t
		}
	}
	return out
}

func (s *Store) SpaceExists(ctx context.Context, id int64) bool {
	var ok bool
	_ = s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM spaces WHERE id = $1)`, id).Scan(&ok)
	return ok
}
