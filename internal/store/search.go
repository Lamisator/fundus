package store

import (
	"context"
	"html"
	"regexp"
	"strings"
)

// Query describes a listing or full-text search over entries.
type Query struct {
	Viewer  int64
	Q       string
	Kind    string
	SpaceID *int64
	Status  string
	Tags    []string // entries must carry all of them
	Starred bool
	Author  int64
	Trash   bool // list deleted entries instead of live ones
	Pinned  *bool
	Sort    string // relevance | updated | created | title | views
	Limit   int
	Offset  int
	Facets  bool
}

type Count struct {
	Key   string `json:"key"`
	Count int    `json:"count"`
}

type Facets struct {
	Kinds    []Count `json:"kinds"`
	Statuses []Count `json:"statuses"`
	Spaces   []Count `json:"spaces"`
	Tags     []Count `json:"tags"`
}

type Result struct {
	Items  []Entry `json:"items"`
	Total  int     `json:"total"`
	Limit  int     `json:"limit"`
	Offset int     `json:"offset"`
	Facets *Facets `json:"facets,omitempty"`
}

// The query CTE derives three tsqueries from the user's input:
//
//	web  - websearch syntax with English stemming ("quoted", -not, or)
//	pre  - every word as a prefix, ANDed (search-as-you-type)
//	anyq - every word ORed, used for long inputs such as pasted log lines
const queryCTE = `WITH q AS (SELECT
	websearch_to_tsquery('english', $2) AS web,
	(SELECT to_tsquery('simple', string_agg(quote_literal(l) || ':*', ' & ')) FROM unnest(tsvector_to_array(to_tsvector('simple', $2))) l) AS pre,
	(SELECT to_tsquery('simple', string_agg(quote_literal(l), ' | ')) FROM unnest(tsvector_to_array(to_tsvector('simple', $2))) l) AS anyq,
	(SELECT count(*) FROM unnest(tsvector_to_array(to_tsvector('simple', $2)))) AS n,
	$3::text AS likeq
) `

const matchSQL = `(e.search @@ q.web OR e.search @@ q.pre OR (q.n >= 5 AND e.search @@ q.anyq)
	OR e.title % $2 OR (length($2) >= 6 AND e.error_text ILIKE q.likeq))`

const rankSQL = `(2 * coalesce(ts_rank(e.search, q.web), 0) + 2 * coalesce(ts_rank(e.search, q.pre), 0)
	+ coalesce(ts_rank(e.search, q.anyq), 0) + similarity(e.title, $2)
	+ CASE WHEN e.error_text <> '' THEN similarity(e.error_text, $2) ELSE 0 END
	+ CASE WHEN e.pinned THEN 0.05 ELSE 0 END)`

func likeEscape(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return "%" + r.Replace(s) + "%"
}

// where builds the filter. $1 is always the viewer and, when searching, $2/$3
// are the query text and its ILIKE pattern. skip names a facet dimension to
// leave out so facet counts show the alternatives.
func (q Query) where(skip string) *builder {
	b := &builder{args: []any{q.Viewer}}
	if q.Q != "" {
		b.args = append(b.args, q.Q, likeEscape(q.Q))
		b.add(matchSQL)
	}
	if q.Trash {
		b.add("e.deleted_at IS NOT NULL")
	} else {
		b.add("e.deleted_at IS NULL")
	}
	if q.Kind != "" && skip != "kind" {
		b.add("e.kind = " + b.arg(q.Kind))
	}
	if q.SpaceID != nil && skip != "space" {
		if *q.SpaceID == 0 {
			b.add("e.space_id IS NULL")
		} else {
			b.add("e.space_id = " + b.arg(*q.SpaceID))
		}
	}
	if q.Status != "" && skip != "status" {
		b.add("e.status = " + b.arg(q.Status))
	}
	if len(q.Tags) > 0 {
		b.add("e.tags @> " + b.arg(NormalizeTags(q.Tags)))
	}
	if q.Starred {
		b.add("EXISTS (SELECT 1 FROM stars st2 WHERE st2.entry_id = e.id AND st2.user_id = $1)")
	}
	if q.Author != 0 {
		b.add("e.created_by = " + b.arg(q.Author))
	}
	if q.Pinned != nil {
		b.add("e.pinned = " + b.arg(*q.Pinned))
	}
	return b
}

func (q Query) prefix() string {
	if q.Q != "" {
		return queryCTE
	}
	return ""
}

func (q Query) from() string {
	if q.Q != "" {
		return entryFrom + " CROSS JOIN q"
	}
	return entryFrom
}

func (s *Store) Search(ctx context.Context, q Query) (Result, error) {
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 25
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	q.Q = strings.TrimSpace(q.Q)
	if len(q.Q) > 4000 {
		q.Q = q.Q[:4000]
	}
	res := Result{Items: []Entry{}, Limit: q.Limit, Offset: q.Offset}

	b := q.where("")
	order := "e.pinned DESC, e.updated_at DESC"
	switch q.Sort {
	case "created":
		order = "e.created_at DESC"
	case "title":
		order = "lower(e.title)"
	case "views":
		order = "e.view_count DESC, e.updated_at DESC"
	case "updated":
		order = "e.updated_at DESC"
	default: // "relevance" or empty: best match when searching, else recent first
		if q.Q != "" {
			order = "rank DESC, e.updated_at DESC"
		}
	}
	extra := ", 0::float8 AS rank, ''::text AS snippet"
	if q.Q != "" {
		extra = ", " + rankSQL + ` AS rank,
			coalesce(ts_headline('english', left(e.summary || E'\n' || e.body || E'\n' || e.error_text, 20000), coalesce(q.pre, q.web),
				'StartSel=⟦, StopSel=⟧, MaxWords=30, MinWords=12, MaxFragments=2, FragmentDelimiter=" … "'), '') AS snippet`
	}
	sql := q.prefix() + `SELECT ` + entryCols + extra + q.from() + ` WHERE ` + b.sql() +
		` ORDER BY ` + order + ` LIMIT ` + b.arg(q.Limit) + ` OFFSET ` + b.arg(q.Offset)
	rows, err := s.db.Query(ctx, sql, b.args...)
	if err != nil {
		return res, err
	}
	for rows.Next() {
		var rank float64
		var snippet string
		e, err := scanEntry(rows, &rank, &snippet)
		if err != nil {
			rows.Close()
			return res, err
		}
		e.Rank = rank
		e.Snippet = cleanSnippet(snippet)
		e.Body, e.ErrorText = "", ""
		res.Items = append(res.Items, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}

	// $1 (the viewer) is unused below; the cast keeps Postgres from
	// complaining that it cannot infer its type.
	cb := q.where("")
	if err := s.db.QueryRow(ctx, q.prefix()+`SELECT count(*) FROM entries e`+crossQ(q)+` WHERE $1::bigint IS NOT NULL AND `+cb.sql(), cb.args...).Scan(&res.Total); err != nil {
		return res, err
	}
	if q.Facets {
		f, err := s.facets(ctx, q)
		if err != nil {
			return res, err
		}
		res.Facets = &f
	}
	return res, nil
}

func crossQ(q Query) string {
	if q.Q != "" {
		return " CROSS JOIN q"
	}
	return ""
}

func (s *Store) facets(ctx context.Context, q Query) (Facets, error) {
	f := Facets{}
	run := func(dim, expr string, limit int, dst *[]Count) error {
		b := q.where(dim)
		sql := q.prefix() + `SELECT ` + expr + `, count(*) FROM entries e` + crossQ(q) + ` WHERE $1::bigint IS NOT NULL AND ` + b.sql() +
			` GROUP BY 1 ORDER BY 2 DESC, 1 LIMIT ` + b.arg(limit)
		rows, err := s.db.Query(ctx, sql, b.args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		*dst = []Count{}
		for rows.Next() {
			var c Count
			if err := rows.Scan(&c.Key, &c.Count); err != nil {
				return err
			}
			*dst = append(*dst, c)
		}
		return rows.Err()
	}
	if err := run("kind", "e.kind", 50, &f.Kinds); err != nil {
		return f, err
	}
	if err := run("status", "e.status", 50, &f.Statuses); err != nil {
		return f, err
	}
	if err := run("space", "coalesce(e.space_id::text, '')", 100, &f.Spaces); err != nil {
		return f, err
	}
	if err := run("tags", "unnest(e.tags)", 40, &f.Tags); err != nil {
		return f, err
	}
	// Drop the "no status" bucket; it is noise in the UI.
	st := f.Statuses[:0]
	for _, c := range f.Statuses {
		if c.Key != "" {
			st = append(st, c)
		}
	}
	f.Statuses = st
	return f, nil
}

var (
	mdNoise = regexp.MustCompile("(?m)^\\s{0,3}(#{1,6}\\s+|[-*+]\\s+\\[[ xX]\\]\\s+|[-*+>]\\s+|\\d+\\.\\s+)|```[a-z]*|\\*\\*|__|`|!?\\[\\[|\\]\\]")
	spaces  = regexp.MustCompile(`\s+`)
)

// cleanSnippet strips markdown noise, HTML-escapes and turns the headline
// markers into <mark> tags.
func cleanSnippet(s string) string {
	s = mdNoise.ReplaceAllString(s, "")
	s = spaces.ReplaceAllString(s, " ")
	s = html.EscapeString(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "⟦", "<mark>")
	return strings.ReplaceAll(s, "⟧", "</mark>")
}

// Similar finds entries that look like the given title / error text / tags,
// for duplicate detection while writing and "related" suggestions.
func (s *Store) Similar(ctx context.Context, viewer int64, title, errText string, tags []string, exclude int64, limit int) ([]Entry, error) {
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	title, errText = strings.TrimSpace(title), strings.TrimSpace(errText)
	if len(errText) > 2000 {
		errText = errText[:2000]
	}
	tags = NormalizeTags(tags)
	if title == "" && errText == "" && len(tags) == 0 {
		return []Entry{}, nil
	}
	// Score: trigram similarity of the titles, the share of this title's
	// (stemmed) words that appear in the other entry's title or tags, error
	// text similarity and shared tags. Body text only selects candidates;
	// counting it in the score made every entry that mentions "connect" or
	// "linux" look related.
	sql := `WITH q AS (SELECT
		(SELECT to_tsquery('simple', string_agg(quote_literal(l), ' | ')) FROM unnest(tsvector_to_array(to_tsvector('english', $2))) l) AS anyq,
		tsvector_to_array(to_tsvector('english', $2)) AS lex)
		SELECT ` + entryCols + `,
		(2 * power(similarity(e.title, $2), 2)
		 + CASE WHEN cardinality(q.lex) > 0 THEN cardinality(ARRAY(
		       SELECT unnest(q.lex) INTERSECT SELECT unnest(tsvector_to_array(ts_filter(e.search, '{a}')))))::float8 / cardinality(q.lex) ELSE 0 END
		 + CASE WHEN $3 <> '' AND e.error_text <> '' THEN similarity(e.error_text, $3) * 2 ELSE 0 END
		 + 0.1 * cardinality(ARRAY(SELECT unnest(e.tags) INTERSECT SELECT unnest($4::text[])))) AS score` + entryFrom + ` CROSS JOIN q
		WHERE e.deleted_at IS NULL AND e.id <> $5
		  AND (($2 <> '' AND (e.title % $2 OR e.search @@ q.anyq))
		       OR ($3 <> '' AND e.error_text % $3)
		       OR e.tags && $4::text[])
		ORDER BY score DESC LIMIT $6`
	rows, err := s.db.Query(ctx, sql, viewer, title, errText, tags, exclude, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		var score float64
		e, err := scanEntry(rows, &score)
		if err != nil {
			return nil, err
		}
		if score < 0.3 {
			continue
		}
		e.Rank = score
		e.Body, e.ErrorText = "", ""
		out = append(out, e)
	}
	return out, rows.Err()
}
