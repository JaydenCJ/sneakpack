// Tests for snapshot capture, canonical IDs, cursor persistence and the
// path-safety rules everything downstream relies on.
package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/sneakpack/internal/ignore"
)

// write creates a file (and parents) under dir with the given content.
func write(t *testing.T, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func take(t *testing.T, dir string) Snapshot {
	t.Helper()
	s, _, err := Take(dir, nil)
	if err != nil {
		t.Fatalf("Take(%s): %v", dir, err)
	}
	return s
}

func TestTakeEmptyDirHasEmptyID(t *testing.T) {
	s := take(t, t.TempDir())
	if len(s.Files) != 0 {
		t.Fatalf("want no files, got %d", len(s.Files))
	}
	if s.ID != EmptyID() {
		t.Fatalf("empty tree must hash to EmptyID, got %s", s.ID)
	}
}

func TestComputeIDIsOrderIndependent(t *testing.T) {
	a := FileEntry{Path: "a.txt", Size: 1, SHA256: strings.Repeat("a", 64)}
	b := FileEntry{Path: "b.txt", Size: 2, SHA256: strings.Repeat("b", 64)}
	if ComputeID([]FileEntry{a, b}) != ComputeID([]FileEntry{b, a}) {
		t.Fatal("ComputeID must canonicalize order before hashing")
	}
}

func TestIDChangesWithContentAndExecBit(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "f.txt", "one")
	id1 := take(t, dir).ID
	write(t, dir, "f.txt", "two")
	id2 := take(t, dir).ID
	if id1 == id2 {
		t.Fatal("different content must produce a different cursor ID")
	}
	if err := os.Chmod(filepath.Join(dir, "f.txt"), 0o755); err != nil {
		t.Fatal(err)
	}
	if take(t, dir).ID == id2 {
		t.Fatal("the executable bit is part of the tree identity")
	}
}

func TestIDIsStableAcrossIdenticalTrees(t *testing.T) {
	// The whole cursor model rests on this: two machines with the same
	// bytes compute the same ID with no shared state.
	d1, d2 := t.TempDir(), t.TempDir()
	for _, d := range []string{d1, d2} {
		write(t, d, "a/one.txt", "1")
		write(t, d, "b/two.txt", "2")
	}
	if take(t, d1).ID != take(t, d2).ID {
		t.Fatal("identical trees must produce identical cursor IDs")
	}
}

func TestTakeSortsCanonically(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "z.txt", "z")
	write(t, dir, "a/nested.txt", "n")
	write(t, dir, "b.txt", "b")
	s := take(t, dir)
	want := []string{"a/nested.txt", "b.txt", "z.txt"}
	for i, w := range want {
		if s.Files[i].Path != w {
			t.Fatalf("position %d: want %q, got %q", i, w, s.Files[i].Path)
		}
	}
}

func TestTakeSkipsStateDir(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "real.txt", "x")
	write(t, dir, StateDir+"/cursor.json", "{}")
	s := take(t, dir)
	if len(s.Files) != 1 || s.Files[0].Path != "real.txt" {
		t.Fatalf("the %s state dir must never be part of a snapshot: %+v", StateDir, s.Files)
	}
}

func TestTakeSkipsSymlinksAndReportsThem(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "real.txt", "x")
	if err := os.Symlink(filepath.Join(dir, "real.txt"), filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("cannot create symlinks here: %v", err)
	}
	s, skipped, err := Take(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 1 {
		t.Fatalf("symlink must not be snapshotted: %+v", s.Files)
	}
	if len(skipped) != 1 || skipped[0] != "link.txt" {
		t.Fatalf("skipped symlink must be reported, got %v", skipped)
	}
}

func TestTakeHonorsIgnoreRulesAndPrunesDirs(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "keep.txt", "k")
	write(t, dir, "scratch.tmp", "t")
	write(t, dir, "cache/deep/huge.bin", "b")
	rules, err := ignore.Parse("*.tmp\ncache/\n")
	if err != nil {
		t.Fatal(err)
	}
	s, _, err := Take(dir, rules)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Files) != 1 || s.Files[0].Path != "keep.txt" {
		t.Fatalf("ignored files and pruned dirs must not appear: %+v", s.Files)
	}
}

func TestTakeRecordsSizeAndHash(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "hello.txt", "hello")
	s := take(t, dir)
	f := s.Files[0]
	// Well-known digest: sha256("hello").
	const want = "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	if f.Size != 5 || f.SHA256 != want {
		t.Fatalf("got size=%d sha=%s", f.Size, f.SHA256)
	}
}

func TestUnicodeAndSpacesInPaths(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "field notes/研究 データ.txt", "ok")
	s := take(t, dir)
	if len(s.Files) != 1 || s.Files[0].Path != "field notes/研究 データ.txt" {
		t.Fatalf("unicode path mangled: %+v", s.Files)
	}
	if got := Short(s.ID); len(got) != 12 {
		t.Fatalf("Short should abbreviate to 12 chars, got %q", got)
	}
	if got := Short(""); got != "(empty)" {
		t.Fatalf("Short empty: %q", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "a")
	write(t, dir, "b/c.txt", "c")
	s := take(t, dir)
	cursor := filepath.Join(t.TempDir(), "cursor.json")
	if err := Save(s, cursor); err != nil {
		t.Fatal(err)
	}
	got, err := Load(cursor)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != s.ID || len(got.Files) != len(s.Files) {
		t.Fatalf("round trip lost data: %+v vs %+v", got, s)
	}
}

func TestLoadRejectsTamperedCursor(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "original")
	s := take(t, dir)
	cursor := filepath.Join(t.TempDir(), "cursor.json")
	if err := Save(s, cursor); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(cursor)
	tampered := strings.Replace(string(data), s.Files[0].SHA256, strings.Repeat("0", 64), 1)
	if err := os.WriteFile(cursor, []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cursor); err == nil || !strings.Contains(err.Error(), "ID mismatch") {
		t.Fatalf("edited cursor must be refused with an ID mismatch, got %v", err)
	}
}

func TestLoadRejectsUnknownFormat(t *testing.T) {
	cursor := filepath.Join(t.TempDir(), "cursor.json")
	if err := os.WriteFile(cursor, []byte(`{"format": 99, "id": "x", "files": []}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cursor); err == nil || !strings.Contains(err.Error(), "format 99") {
		t.Fatalf("unknown format must be refused by name, got %v", err)
	}
}

func TestLoadRejectsNonCanonicalOrder(t *testing.T) {
	files := []FileEntry{
		{Path: "b.txt", Size: 1, SHA256: strings.Repeat("b", 64)},
		{Path: "a.txt", Size: 1, SHA256: strings.Repeat("a", 64)},
	}
	s := Snapshot{Format: Format, ID: ComputeID(files), Files: files}
	cursor := filepath.Join(t.TempDir(), "cursor.json")
	if err := Save(s, cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cursor); err == nil || !strings.Contains(err.Error(), "canonical order") {
		t.Fatalf("out-of-order entries must be refused, got %v", err)
	}
}

func TestLoadRejectsEscapingPath(t *testing.T) {
	files := []FileEntry{{Path: "../evil.txt", Size: 1, SHA256: strings.Repeat("a", 64)}}
	s := Snapshot{Format: Format, ID: ComputeID(files), Files: files}
	cursor := filepath.Join(t.TempDir(), "cursor.json")
	if err := Save(s, cursor); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(cursor); err == nil {
		t.Fatal("a cursor naming ../ paths must be refused")
	}
}

func TestValidatePathSeparatesSafeFromDangerous(t *testing.T) {
	for _, p := range []string{"a.txt", "a/b/c.bin", "field notes/日誌.md", "a-b_c.1"} {
		if err := ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil", p, err)
		}
	}
	// This is the zip-slip guard; every case here is an attack or a bug.
	bad := []string{
		"", "/etc/passwd", "../up.txt", "a/../../b", "a/./b", "a//b",
		"..", ".sneakpack", ".sneakpack/cursor.json", `a\b`,
	}
	for _, p := range bad {
		if err := ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) accepted a dangerous path", p)
		}
	}
}
