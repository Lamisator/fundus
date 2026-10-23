package server

// A small Model Context Protocol server (streamable HTTP transport, JSON
// responses only) so AI assistants such as Claude can search and extend the
// knowledge base. Authenticate with an API token:
//
//	claude mcp add --transport http fundus https://kb.example.com/mcp \
//	    --header "Authorization: Bearer fdk_..."

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Lamisator/fundus/internal/store"
)

const mcpProtocol = "2025-06-18"

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func (s *Server) handleMCPGet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Allow", "POST")
	http.Error(w, "this MCP server does not offer an SSE stream; POST JSON-RPC messages", http.StatusMethodNotAllowed)
}

func (s *Server) handleMCP(w http.ResponseWriter, r *http.Request) {
	p := who(r)
	if p == nil {
		w.Header().Set("WWW-Authenticate", `Bearer realm="fundus"`)
		apiError(w, http.StatusUnauthorized, "unauthenticated", "send an API token: Authorization: Bearer fdk_...")
		return
	}
	var req rpcRequest
	r.Body = http.MaxBytesReader(w, r.Body, 4<<20)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, rpcResponse{JSONRPC: "2.0", ID: json.RawMessage("null"),
			Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if len(req.ID) == 0 { // notification
		w.WriteHeader(http.StatusAccepted)
		return
	}
	resp := rpcResponse{JSONRPC: "2.0", ID: req.ID}
	switch req.Method {
	case "initialize":
		var in struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &in)
		ver := mcpProtocol
		if in.ProtocolVersion != "" && in.ProtocolVersion < mcpProtocol {
			ver = in.ProtocolVersion
		}
		resp.Result = map[string]any{
			"protocolVersion": ver,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": "fundus", "title": s.cfg.SiteName, "version": Version},
			"instructions": "Fundus is a knowledge base, used mostly for IT problems and their solutions. " +
				"Search it before solving a technical problem from scratch; when you solve something worth remembering, " +
				"document it with create_entry (kind 'problem' plus a solution) after asking the user.",
		}
	case "ping":
		resp.Result = map[string]any{}
	case "tools/list":
		resp.Result = map[string]any{"tools": mcpTools(p.canWrite())}
	case "tools/call":
		var in struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &in); err != nil {
			resp.Error = &rpcError{Code: -32602, Message: "invalid params"}
			break
		}
		text, err := s.callTool(r, in.Name, in.Arguments)
		if err != nil {
			resp.Result = map[string]any{"content": []map[string]string{{"type": "text", "text": err.Error()}}, "isError": true}
		} else {
			resp.Result = map[string]any{"content": []map[string]string{{"type": "text", "text": text}}}
		}
	default:
		resp.Error = &rpcError{Code: -32601, Message: "method not found: " + req.Method}
	}
	writeJSON(w, http.StatusOK, resp)
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func prop(typ, desc string) map[string]any { return map[string]any{"type": typ, "description": desc} }

func mcpTools(write bool) []map[string]any {
	ro := map[string]any{"readOnlyHint": true}
	tools := []map[string]any{
		{"name": "search_knowledge", "title": "Search the knowledge base", "annotations": ro,
			"description": "Full-text search over all entries (titles, bodies, error messages, solutions, tags). " +
				"Supports \"quoted phrases\", -exclusions and OR. Pasting a raw error message works well.",
			"inputSchema": obj(map[string]any{
				"query":  prop("string", "search text or error message"),
				"kind":   prop("string", "restrict to a kind, e.g. problem, howto, runbook, snippet, note, reference"),
				"tag":    prop("string", "restrict to entries carrying this tag"),
				"status": prop("string", "e.g. open, solved, workaround"),
				"limit":  prop("integer", "max results (default 10)"),
			}, "query")},
		{"name": "get_entry", "title": "Read an entry", "annotations": ro,
			"description": "Return one entry as Markdown with front matter, including all of its solutions.",
			"inputSchema": obj(map[string]any{"id": prop("integer", "entry id")}, "id")},
		{"name": "find_similar", "title": "Find similar entries", "annotations": ro,
			"description": "Find entries that look like a given title and/or error text - use before creating one to avoid duplicates.",
			"inputSchema": obj(map[string]any{
				"title":      prop("string", "a title or short problem description"),
				"error_text": prop("string", "an error message"),
			})},
		{"name": "list_taxonomy", "title": "List kinds, spaces and tags", "annotations": ro,
			"description": "List the available entry kinds (with their statuses and suggested fields), spaces and the most used tags.",
			"inputSchema": obj(map[string]any{})},
	}
	if write {
		entryProps := map[string]any{
			"title":      prop("string", "short, specific title, e.g. 'nginx: 413 Request Entity Too Large on uploads'"),
			"kind":       prop("string", "problem (default), howto, runbook, snippet, note or reference"),
			"summary":    prop("string", "one or two sentence summary"),
			"body":       prop("string", "Markdown body: symptoms, environment, root cause, steps..."),
			"error_text": prop("string", "verbatim error message or log excerpt"),
			"status":     prop("string", "status for kinds that have one (problem: open, investigating, solved, workaround, wontfix)"),
			"tags":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "tags such as nginx, docker, linux"},
			"fields":     map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}, "description": "metadata such as os, software, version, host"},
			"space":      prop("string", "space slug"),
		}
		create := map[string]any{}
		for k, v := range entryProps {
			create[k] = v
		}
		create["solution"] = prop("string", "Markdown solution; it is stored as the accepted solution")
		update := map[string]any{"id": prop("integer", "entry id"), "note": prop("string", "what changed (revision note)")}
		for k, v := range entryProps {
			update[k] = v
		}
		tools = append(tools,
			map[string]any{"name": "create_entry", "title": "Create an entry",
				"description": "Document something new. For a solved IT problem use kind 'problem' and pass the fix as 'solution'.",
				"inputSchema": obj(create, "title")},
			map[string]any{"name": "update_entry", "title": "Update an entry",
				"description": "Change fields of an existing entry. Only the given fields change; body replaces the whole body.",
				"inputSchema": obj(update, "id")},
			map[string]any{"name": "add_solution", "title": "Add a solution",
				"description": "Add a solution (Markdown) to an entry, optionally marking it accepted (which marks a problem solved).",
				"inputSchema": obj(map[string]any{
					"entry_id": prop("integer", "entry id"),
					"title":    prop("string", "short title of the approach"),
					"body":     prop("string", "Markdown"),
					"accepted": prop("boolean", "mark as the accepted solution"),
				}, "entry_id", "body")},
		)
	}
	return tools
}

type toolArgs struct {
	Query     string            `json:"query"`
	Kind      string            `json:"kind"`
	Tag       string            `json:"tag"`
	Status    string            `json:"status"`
	Limit     int               `json:"limit"`
	ID        int64             `json:"id"`
	EntryID   int64             `json:"entry_id"`
	Title     *string           `json:"title"`
	Summary   *string           `json:"summary"`
	Body      *string           `json:"body"`
	ErrorText *string           `json:"error_text"`
	Tags      *[]string         `json:"tags"`
	Fields    map[string]string `json:"fields"`
	Space     string            `json:"space"`
	Solution  string            `json:"solution"`
	Note      string            `json:"note"`
	Accepted  bool              `json:"accepted"`
	KindPtr   *string           `json:"-"`
	StatusPtr *string           `json:"-"`
}

func (s *Server) callTool(r *http.Request, name string, raw json.RawMessage) (string, error) {
	var a toolArgs
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return "", fmt.Errorf("invalid arguments: %v", err)
		}
	}
	var keys map[string]json.RawMessage
	_ = json.Unmarshal(raw, &keys)
	if _, ok := keys["kind"]; ok {
		a.KindPtr = &a.Kind
	}
	if _, ok := keys["status"]; ok {
		a.StatusPtr = &a.Status
	}
	p := who(r)
	ctx := r.Context()
	base := s.baseURL(r)
	writeTool := name == "create_entry" || name == "update_entry" || name == "add_solution"
	if writeTool && !p.canWrite() {
		return "", fmt.Errorf("this token is not allowed to write (needs the write scope and an editor role)")
	}
	switch name {
	case "search_knowledge":
		limit := a.Limit
		if limit <= 0 || limit > 50 {
			limit = 10
		}
		q := store.Query{Viewer: p.ID, Q: a.Query, Kind: a.Kind, Status: a.Status, Limit: limit}
		if a.Tag != "" {
			q.Tags = []string{a.Tag}
		}
		res, err := s.st.Search(ctx, q)
		if err != nil {
			return "", err
		}
		if len(res.Items) == 0 {
			return "No entries match.", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "%d match(es), showing %d:\n\n", res.Total, len(res.Items))
		for _, e := range res.Items {
			fmt.Fprintf(&b, "- [%d] %s (%s", e.ID, e.Title, e.Kind)
			if e.Status != "" {
				fmt.Fprintf(&b, ", %s", e.Status)
			}
			if e.SolutionCount > 0 {
				fmt.Fprintf(&b, ", %d solution(s)", e.SolutionCount)
			}
			b.WriteString(")")
			if len(e.Tags) > 0 {
				b.WriteString(" #" + strings.Join(e.Tags, " #"))
			}
			b.WriteString("\n")
			snip := strings.NewReplacer("<mark>", "**", "</mark>", "**", "&quot;", `"`, "&#39;", "'", "&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(e.Snippet)
			if snip == "" {
				snip = e.Summary
			}
			if snip != "" {
				b.WriteString("  " + snip + "\n")
			}
		}
		b.WriteString("\nUse get_entry with an id for the full text.")
		return b.String(), nil

	case "get_entry":
		e, err := s.st.GetEntry(ctx, p.ID, a.ID)
		if err != nil || e.DeletedAt != nil {
			return "", fmt.Errorf("entry %d not found", a.ID)
		}
		return s.entryMarkdown(ctx, r, e, p.ID), nil

	case "find_similar":
		t, et := "", ""
		if a.Title != nil {
			t = *a.Title
		}
		if a.ErrorText != nil {
			et = *a.ErrorText
		}
		list, err := s.st.Similar(ctx, p.ID, t, et, nil, 0, 8)
		if err != nil {
			return "", err
		}
		if len(list) == 0 {
			return "Nothing similar found.", nil
		}
		var b strings.Builder
		for _, e := range list {
			fmt.Fprintf(&b, "- [%d] %s (%s%s) score %.2f\n", e.ID, e.Title, e.Kind, prefixed(", ", e.Status), e.Rank)
		}
		return b.String(), nil

	case "list_taxonomy":
		kinds, err := s.st.ListKinds(ctx)
		if err != nil {
			return "", err
		}
		spaces, _ := s.st.ListSpaces(ctx)
		tags, _ := s.st.ListTags(ctx, "", 60)
		var b strings.Builder
		b.WriteString("Kinds:\n")
		for _, k := range kinds {
			fmt.Fprintf(&b, "- %s (%s): %s", k.Key, k.Label, k.Description)
			if len(k.Statuses) > 0 {
				fmt.Fprintf(&b, " Statuses: %s.", strings.Join(k.Statuses, ", "))
			}
			if len(k.Fields) > 0 {
				var f []string
				for _, x := range k.Fields {
					f = append(f, x.Key)
				}
				fmt.Fprintf(&b, " Fields: %s.", strings.Join(f, ", "))
			}
			b.WriteString("\n")
		}
		b.WriteString("\nSpaces:\n")
		for _, sp := range spaces {
			fmt.Fprintf(&b, "- %s: %s (%d entries)\n", sp.Slug, sp.Name, sp.EntryCount)
		}
		b.WriteString("\nTop tags: ")
		var tl []string
		for _, t := range tags {
			tl = append(tl, fmt.Sprintf("%s (%d)", t.Key, t.Count))
		}
		b.WriteString(strings.Join(tl, ", "))
		return b.String(), nil

	case "create_entry":
		kind := a.Kind
		if kind == "" {
			kind = "problem"
		}
		in := store.EntryInput{Kind: &kind, Title: a.Title, Summary: a.Summary, Body: a.Body, ErrorText: a.ErrorText,
			Tags: a.Tags, Note: "Created via MCP"}
		if a.StatusPtr != nil {
			in.Status = a.StatusPtr
		}
		if a.Fields != nil {
			in.Fields = &a.Fields
		}
		if a.Space != "" {
			sp, err := s.st.GetSpace(ctx, a.Space)
			if err != nil {
				return "", fmt.Errorf("unknown space %q", a.Space)
			}
			in.SpaceID = &sp.ID
		}
		if err := s.validateEntry(ctx, &in, nil); err != nil {
			return "", err
		}
		e, err := s.st.CreateEntry(ctx, p.ID, in)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(a.Solution) != "" {
			if _, err := s.st.CreateSolution(ctx, p.ID, e.ID, "", a.Solution, true); err != nil {
				return "", err
			}
		}
		s.st.Audit(ctx, p.ID, "entry.created", strconv.FormatInt(e.ID, 10), e.Title+" (MCP)", clientIP(r))
		s.hooks.emit("entry.created", e)
		return fmt.Sprintf("Created entry %d: %s/e/%d", e.ID, base, e.ID), nil

	case "update_entry":
		cur, err := s.st.GetEntry(ctx, p.ID, a.ID)
		if err != nil || cur.DeletedAt != nil {
			return "", fmt.Errorf("entry %d not found", a.ID)
		}
		in := store.EntryInput{Kind: a.KindPtr, Title: a.Title, Summary: a.Summary, Body: a.Body, ErrorText: a.ErrorText,
			Status: a.StatusPtr, Tags: a.Tags, Note: a.Note}
		if in.Note == "" {
			in.Note = "Updated via MCP"
		}
		if a.Fields != nil {
			in.Fields = &a.Fields
		}
		if a.Space != "" {
			sp, err := s.st.GetSpace(ctx, a.Space)
			if err != nil {
				return "", fmt.Errorf("unknown space %q", a.Space)
			}
			in.SpaceID = &sp.ID
		}
		if err := s.validateEntry(ctx, &in, &cur); err != nil {
			return "", err
		}
		e, err := s.st.UpdateEntry(ctx, p.ID, a.ID, in)
		if err != nil {
			return "", err
		}
		s.hooks.emit("entry.updated", e)
		return fmt.Sprintf("Updated entry %d (now version %d): %s/e/%d", e.ID, e.Version, base, e.ID), nil

	case "add_solution":
		e, err := s.st.GetEntry(ctx, p.ID, a.EntryID)
		if err != nil || e.DeletedAt != nil {
			return "", fmt.Errorf("entry %d not found", a.EntryID)
		}
		if a.Body == nil || strings.TrimSpace(*a.Body) == "" {
			return "", fmt.Errorf("body is required")
		}
		title := ""
		if a.Title != nil {
			title = *a.Title
		}
		sol, err := s.st.CreateSolution(ctx, p.ID, e.ID, title, *a.Body, a.Accepted)
		if err != nil {
			return "", err
		}
		s.hooks.emit("solution.created", map[string]any{"entry_id": e.ID, "entry_title": e.Title, "solution": sol})
		return fmt.Sprintf("Added solution %d to entry %d: %s/e/%d", sol.ID, e.ID, base, e.ID), nil
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func prefixed(sep, s string) string {
	if s == "" {
		return ""
	}
	return sep + s
}
