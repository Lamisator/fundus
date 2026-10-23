// Package markdown renders entry bodies to sanitised HTML.
package markdown

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"

	chromahtml "github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/microcosm-cc/bluemonday"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
)

var md = goldmark.New(
	goldmark.WithExtensions(
		extension.GFM,
		extension.Footnote,
		extension.DefinitionList,
		highlighting.NewHighlighting(
			highlighting.WithFormatOptions(chromahtml.WithClasses(true)),
			highlighting.WithGuessLanguage(false),
		),
	),
	goldmark.WithParserOptions(parser.WithAutoHeadingID()),
)

var policy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").Matching(regexp.MustCompile(`^[\w\- ]+$`)).OnElements("code", "span", "pre", "div", "a", "mark")
	p.AllowAttrs("id").Matching(regexp.MustCompile(`^[\w\-]+$`)).OnElements("h1", "h2", "h3", "h4", "h5", "h6", "li", "sup", "div")
	p.AllowAttrs("type").Matching(regexp.MustCompile(`^checkbox$`)).OnElements("input")
	p.AllowAttrs("checked", "disabled").OnElements("input")
	p.AllowElements("mark", "kbd", "details", "summary")
	p.AllowAttrs("open").OnElements("details")
	p.AllowAttrs("data-entry").Matching(regexp.MustCompile(`^\d+$`)).OnElements("a")
	p.AllowAttrs("start").OnElements("ol")
	return p
}()

// Options tune a render.
type Options struct {
	// Titles resolves [[id]] references to entry titles; missing ids are
	// rendered as broken links.
	Titles func(ids []int64) map[int64]string
	// FileQuery is appended to /files/ URLs (used by public share pages).
	FileQuery string
	// EntryBase prefixes entry links (default "/e/").
	EntryBase string
}

var (
	refRe   = regexp.MustCompile(`\[\[(\d+)(?:\|([^\]\n]*))?\]\]`)
	fenceRe = regexp.MustCompile("(?m)^(```|~~~)")
	fileRe  = regexp.MustCompile(`(src|href)="(/files/[A-Za-z0-9_\-]+(?:/[^"?]*)?)"`)
)

// labelEscaper makes an entry title safe to embed as Markdown link text.
var labelEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "[", `\[`, "]", `\]`,
	"*", `\*`, "_", `\_`, "`", "\\`", `\`, `\\`)

// Render converts markdown to safe HTML.
func Render(src string, opt Options) string {
	if opt.EntryBase == "" {
		opt.EntryBase = "/e/"
	}
	src = expandRefs(src, opt)
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "<pre>" + html.EscapeString(src) + "</pre>"
	}
	out := policy.Sanitize(buf.String())
	// Task list checkboxes come out as <input checked="" disabled="" type="checkbox">.
	if opt.FileQuery != "" {
		out = fileRe.ReplaceAllString(out, `$1="$2?`+opt.FileQuery+`"`)
	}
	return out
}

// expandRefs rewrites [[123]] and [[123|label]] into markdown links,
// leaving fenced code blocks alone.
func expandRefs(src string, opt Options) string {
	if !strings.Contains(src, "[[") {
		return src
	}
	var ids []int64
	for _, m := range refRe.FindAllStringSubmatch(src, -1) {
		if id, err := strconv.ParseInt(m[1], 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	titles := map[int64]string{}
	if opt.Titles != nil && len(ids) > 0 {
		titles = opt.Titles(ids)
	}
	parts := fenceRe.Split(src, -1)
	fences := fenceRe.FindAllString(src, -1)
	var b strings.Builder
	for i, part := range parts {
		if i%2 == 0 { // outside code fences
			part = refRe.ReplaceAllStringFunc(part, func(m string) string {
				sm := refRe.FindStringSubmatch(m)
				id, _ := strconv.ParseInt(sm[1], 10, 64)
				label := strings.TrimSpace(sm[2])
				title, ok := titles[id]
				if label == "" {
					if ok {
						label = title
					} else {
						label = "#" + sm[1]
					}
				}
				label = labelEscaper.Replace(label)
				return fmt.Sprintf("[%s](%s%d)", label, opt.EntryBase, id)
			})
		}
		b.WriteString(part)
		if i < len(fences) {
			b.WriteString(fences[i])
		}
	}
	return b.String()
}

// Plain strips markdown down to text, for previews.
func Plain(src string, max int) string {
	s := regexp.MustCompile("(?s)```.*?```").ReplaceAllString(src, " ")
	s = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`).ReplaceAllString(s, "")
	s = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`).ReplaceAllString(s, "$1")
	s = regexp.MustCompile("(?m)^\\s{0,3}(#{1,6}|>|[-*+]|\\d+\\.)\\s+").ReplaceAllString(s, "")
	s = strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	if max > 0 && len([]rune(s)) > max {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:max])) + "…"
	}
	return s
}
