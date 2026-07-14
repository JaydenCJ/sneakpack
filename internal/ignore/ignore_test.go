// Tests for .sneakpackignore parsing and matching. The ignore subset is
// small on purpose; these tests pin its exact semantics so a rule file
// synced between machines can never mean two different things.
package ignore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustParse(t *testing.T, content string) *Rules {
	t.Helper()
	r, err := Parse(content)
	if err != nil {
		t.Fatalf("Parse(%q): %v", content, err)
	}
	return r
}

func TestEmptyAndNilRulesMatchNothing(t *testing.T) {
	r := mustParse(t, "")
	if r.Match("anything.txt", false) || r.Match("dir/other", true) {
		t.Fatal("empty rule set must not ignore anything")
	}
	var nilRules *Rules
	if nilRules.Match("file.txt", false) {
		t.Fatal("nil rules must not ignore anything")
	}
}

func TestCommentsAndBlankLinesAreSkipped(t *testing.T) {
	r := mustParse(t, "# a comment\n\n   \n*.log\n")
	if r.Len() != 1 {
		t.Fatalf("want 1 active pattern, got %d", r.Len())
	}
	if !r.Match("x.log", false) {
		t.Fatal("*.log should still apply after comments")
	}
}

func TestBasenamePatternMatchesAtAnyDepth(t *testing.T) {
	r := mustParse(t, "*.log\n")
	for _, p := range []string{"x.log", "a/x.log", "a/b/c/deep.log"} {
		if !r.Match(p, false) {
			t.Errorf("*.log should match %q", p)
		}
	}
	if r.Match("x.log.txt", false) {
		t.Error("*.log must not match x.log.txt")
	}
	dirs := mustParse(t, "node_cache\n")
	if !dirs.Match("vendor/node_cache", true) {
		t.Fatal("a bare name should also match a directory at any depth")
	}
	q := mustParse(t, "data?.csv\n")
	if !q.Match("data1.csv", false) || q.Match("data12.csv", false) {
		t.Fatal("? must match exactly one character")
	}
}

func TestAnchoredPatternMatchesFromRootOnly(t *testing.T) {
	r := mustParse(t, "src/*.gen\n")
	if !r.Match("src/x.gen", false) {
		t.Fatal("anchored pattern should match at the root")
	}
	if r.Match("other/src/x.gen", false) {
		t.Fatal("anchored pattern must not float to deeper levels")
	}
}

func TestLeadingSlashIsAcceptedAndAnchors(t *testing.T) {
	r := mustParse(t, "/secret.txt\n")
	if !r.Match("secret.txt", false) {
		t.Fatal("/secret.txt should match the root file")
	}
	// A single leading slash on a bare name still means "anchored".
	if r.Match("sub/secret.txt", false) {
		t.Fatal("/secret.txt must not match nested copies")
	}
}

func TestDirOnlyPatternSkipsPlainFiles(t *testing.T) {
	r := mustParse(t, "build/\n")
	if !r.Match("build", true) {
		t.Fatal("build/ should match the build directory")
	}
	if r.Match("build", false) {
		t.Fatal("build/ must not match a regular file named build")
	}
}

func TestDoubleStarSpansSegments(t *testing.T) {
	r := mustParse(t, "docs/**/draft.md\n")
	for _, p := range []string{"docs/draft.md", "docs/a/draft.md", "docs/a/b/draft.md"} {
		if !r.Match(p, false) {
			t.Errorf("docs/**/draft.md should match %q (** spans zero or more segments)", p)
		}
	}
	if r.Match("notes/docs/draft.md", false) {
		t.Error("anchored ** pattern must not float off the root")
	}
	sub := mustParse(t, "cache/**\n")
	if !sub.Match("cache/a", false) || !sub.Match("cache/a/b/c", false) {
		t.Fatal("cache/** should match everything beneath cache")
	}
}

func TestNegationReincludes(t *testing.T) {
	r := mustParse(t, "*.log\n!keep.log\n")
	if r.Match("keep.log", false) {
		t.Fatal("!keep.log should carve keep.log out of *.log")
	}
	if !r.Match("other.log", false) {
		t.Fatal("other .log files stay ignored")
	}
	// Re-ignoring after a negation must stick: the last match wins.
	again := mustParse(t, "*.log\n!keep.log\nkeep.log\n")
	if !again.Match("keep.log", false) {
		t.Fatal("the final keep.log pattern should win over the earlier negation")
	}
}

func TestBadGlobReportsLineNumber(t *testing.T) {
	_, err := Parse("ok.txt\n[unclosed\n")
	if err == nil {
		t.Fatal("unclosed character class must be rejected")
	}
	if !strings.Contains(err.Error(), "line 2") {
		t.Fatalf("error should name line 2, got: %v", err)
	}
}

func TestLoadMissingAndRealFiles(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "nope", ".sneakpackignore"))
	if err != nil {
		t.Fatalf("missing ignore file must not be an error: %v", err)
	}
	if r.Len() != 0 {
		t.Fatalf("want empty rules, got %d patterns", r.Len())
	}
	dir := t.TempDir()
	file := filepath.Join(dir, ".sneakpackignore")
	if err := os.WriteFile(file, []byte("*.tmp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err = Load(file)
	if err != nil {
		t.Fatal(err)
	}
	if !r.Match("scratch.tmp", false) {
		t.Fatal("pattern from disk should apply")
	}
}
