package markdown

import (
	"strings"
	"testing"
)

func TestRenderSanitises(t *testing.T) {
	for _, in := range []string{
		`<script>alert(1)</script>`,
		`<img src=x onerror=alert(1)>`,
		`[x](javascript:alert(1))`,
		`<a href="/e/1" onclick="alert(1)">x</a>`,
		`<iframe src="https://evil.example"></iframe>`,
	} {
		out := Render(in, Options{})
		for _, bad := range []string{"<script", "onerror", "javascript:", "onclick", "<iframe"} {
			if strings.Contains(strings.ToLower(out), bad) {
				t.Errorf("Render(%q) = %q contains %q", in, out, bad)
			}
		}
	}
}

func TestRefs(t *testing.T) {
	titles := func(ids []int64) map[int64]string { return map[int64]string{7: "Docker <socket>"} }
	out := Render("See [[7]], [[7|the fix]] and [[8]].\n\n```\n[[7]] stays code\n```", Options{Titles: titles})
	for _, want := range []string{`<a href="/e/7"`, `Docker &lt;socket&gt;`, `>the fix</a>`, `>#8</a>`, `[[7]] stays code`} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestHighlightAndTasks(t *testing.T) {
	out := Render("- [x] done\n- [ ] todo\n\n```go\nfunc main() {}\n```", Options{})
	if !strings.Contains(out, `type="checkbox"`) || !strings.Contains(out, `class="chroma"`) {
		t.Errorf("unexpected output: %s", out)
	}
}

func TestShareFileQuery(t *testing.T) {
	out := Render("![s](/files/abcdefghijklmnopqrstuvwx/shot.png)", Options{FileQuery: "share=tok"})
	if !strings.Contains(out, `src="/files/abcdefghijklmnopqrstuvwx/shot.png?share=tok"`) {
		t.Errorf("share query not applied: %s", out)
	}
}
