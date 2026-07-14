// Tests for the apply pipeline: continuity, conflicts, staged landing,
// pruning, and post-apply verification. Every scenario here is a thing
// that actually happens to trees synced over removable media.
package apply

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/sneakpack/internal/bundle"
	"github.com/JaydenCJ/sneakpack/internal/snapshot"
)

// write creates a file (and parents) under dir.
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

func read(t *testing.T, dir, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func take(t *testing.T, dir string) snapshot.Snapshot {
	t.Helper()
	s, _, err := snapshot.Take(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// pack writes a bundle moving base → current state of dir.
func pack(t *testing.T, dir string, base snapshot.Snapshot) string {
	t.Helper()
	m := bundle.New(base, take(t, dir))
	out := filepath.Join(t.TempDir(), "bundle.spk")
	if _, err := bundle.Write(out, m, dir); err != nil {
		t.Fatal(err)
	}
	return out
}

func emptySnap() snapshot.Snapshot {
	return snapshot.Snapshot{Format: snapshot.Format, ID: snapshot.EmptyID()}
}

// setupSync builds a source tree, applies its full bundle to a fresh
// mirror, and returns (source, mirror, source snapshot at hand-off).
func setupSync(t *testing.T) (string, string, snapshot.Snapshot) {
	t.Helper()
	src, dst := t.TempDir(), t.TempDir()
	write(t, src, "notes/day1.md", "day one\n")
	write(t, src, "readings.csv", "id,temp\n1,20.5\n")
	full := pack(t, src, emptySnap())
	if _, err := Run(full, dst, Options{}); err != nil {
		t.Fatalf("full apply: %v", err)
	}
	return src, dst, take(t, src)
}

func TestFullBundleToFreshDir(t *testing.T) {
	_, dst, base := setupSync(t)
	if got := read(t, dst, "notes/day1.md"); got != "day one\n" {
		t.Fatalf("content wrong after full apply: %q", got)
	}
	cur, err := LoadCursor(dst)
	if err != nil {
		t.Fatal(err)
	}
	if cur.ID != base.ID {
		t.Fatalf("cursor after apply %s, want %s", cur.ID, base.ID)
	}
}

func TestIncrementalAddModifyDelete(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, src, "notes/day2.md", "day two\n")
	write(t, src, "readings.csv", "id,temp\n1,20.5\n2,21.0\n")
	if err := os.Remove(filepath.Join(src, "notes/day1.md")); err != nil {
		t.Fatal(err)
	}
	inc := pack(t, src, base)
	res, err := Run(inc, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 || res.Modified != 1 || res.Deleted != 1 || !res.Verified {
		t.Fatalf("unexpected result: %+v", res)
	}
	if read(t, dst, "notes/day2.md") != "day two\n" {
		t.Fatal("added file missing")
	}
	if read(t, dst, "readings.csv") != "id,temp\n1,20.5\n2,21.0\n" {
		t.Fatal("modification not applied")
	}
	if _, err := os.Stat(filepath.Join(dst, "notes/day1.md")); !os.IsNotExist(err) {
		t.Fatal("deleted file survived")
	}
}

func TestApplyChainsAcrossGenerations(t *testing.T) {
	// Three hops: full, then two incrementals, each packed against the
	// previous target — the everyday courier loop.
	src, dst, base := setupSync(t)
	write(t, src, "gen2.txt", "2")
	inc1 := pack(t, src, base)
	if _, err := Run(inc1, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	gen2 := take(t, src)
	write(t, src, "gen3.txt", "3")
	inc2 := pack(t, src, gen2)
	res, err := Run(inc2, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Verified || read(t, dst, "gen3.txt") != "3" {
		t.Fatalf("chain broke at generation 3: %+v", res)
	}
}

func TestOutOfOrderBundleIsRefused(t *testing.T) {
	// Skipping a generation must fail loudly, not corrupt silently.
	src, dst, base := setupSync(t)
	write(t, src, "gen2.txt", "2")
	_ = pack(t, src, base) // generation 2 never applied
	gen2 := take(t, src)
	write(t, src, "gen3.txt", "3")
	inc2 := pack(t, src, gen2)
	_, err := Run(inc2, dst, Options{})
	var ce *ContinuityError
	if !errors.As(err, &ce) {
		t.Fatalf("want ContinuityError, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "gen3.txt")); !os.IsNotExist(statErr) {
		t.Fatal("refused bundle must not touch the tree")
	}
}

func TestReplayedBundleIsRefused(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, src, "gen2.txt", "2")
	inc := pack(t, src, base)
	if _, err := Run(inc, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	_, err := Run(inc, dst, Options{})
	var ce *ContinuityError
	if !errors.As(err, &ce) {
		t.Fatalf("applying the same bundle twice must break continuity, got %v", err)
	}
}

func TestForceRecoversFromLostStateDir(t *testing.T) {
	// The realistic --force story: the destination's .sneakpack dir was
	// deleted, so continuity fails even though the files are fine.
	src, dst, base := setupSync(t)
	if err := os.RemoveAll(filepath.Join(dst, snapshot.StateDir)); err != nil {
		t.Fatal(err)
	}
	write(t, src, "gen2.txt", "2")
	inc := pack(t, src, base)
	if _, err := Run(inc, dst, Options{}); err == nil {
		t.Fatal("lost cursor must fail without --force")
	}
	res, err := Run(inc, dst, Options{Force: true})
	if err != nil {
		t.Fatalf("--force should recover: %v", err)
	}
	if !res.Verified {
		t.Fatalf("recovered tree should verify: %+v", res)
	}
}

func TestLocalEditBlocksModification(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, dst, "readings.csv", "id,temp\n1,999\n") // local edit on the mirror
	write(t, src, "readings.csv", "id,temp\n1,20.5\n2,21.0\n")
	inc := pack(t, src, base)
	_, err := Run(inc, dst, Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) {
		t.Fatalf("want ConflictError, got %v", err)
	}
	if len(ce.Conflicts) != 1 || ce.Conflicts[0].Path != "readings.csv" {
		t.Fatalf("wrong conflicts: %+v", ce.Conflicts)
	}
	if read(t, dst, "readings.csv") != "id,temp\n1,999\n" {
		t.Fatal("conflicting apply must leave local edits intact")
	}
}

func TestLocallyDeletedFileBlocksModification(t *testing.T) {
	src, dst, base := setupSync(t)
	if err := os.Remove(filepath.Join(dst, "readings.csv")); err != nil {
		t.Fatal(err)
	}
	write(t, src, "readings.csv", "id,temp\n1,20.5\n2,21.0\n")
	inc := pack(t, src, base)
	_, err := Run(inc, dst, Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) || !strings.Contains(ce.Conflicts[0].Reason, "missing locally") {
		t.Fatalf("want missing-locally conflict, got %v", err)
	}
}

func TestLocalEditBlocksDeletion(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, dst, "notes/day1.md", "my local additions\n")
	if err := os.Remove(filepath.Join(src, "notes/day1.md")); err != nil {
		t.Fatal(err)
	}
	inc := pack(t, src, base)
	_, err := Run(inc, dst, Options{})
	var ce *ConflictError
	if !errors.As(err, &ce) || !strings.Contains(ce.Conflicts[0].Reason, "wants to delete") {
		t.Fatalf("want delete conflict, got %v", err)
	}
	if read(t, dst, "notes/day1.md") != "my local additions\n" {
		t.Fatal("local edit must survive the refused apply")
	}
}

func TestForceOverwritesConflicts(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, dst, "readings.csv", "id,temp\n1,999\n")
	write(t, src, "readings.csv", "id,temp\n1,20.5\n2,21.0\n")
	inc := pack(t, src, base)
	res, err := Run(inc, dst, Options{Force: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conflicts) != 1 || !res.Verified {
		t.Fatalf("force result should report the overwritten conflict: %+v", res)
	}
	if read(t, dst, "readings.csv") != "id,temp\n1,20.5\n2,21.0\n" {
		t.Fatal("--force must land the bundle's content")
	}
}

func TestDryRunChangesNothing(t *testing.T) {
	src, dst, base := setupSync(t)
	before, err := LoadCursor(dst)
	if err != nil {
		t.Fatal(err)
	}
	write(t, src, "gen2.txt", "2")
	inc := pack(t, src, base)
	res, err := Run(inc, dst, Options{DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Added != 1 {
		t.Fatalf("dry run should report the plan: %+v", res)
	}
	if _, statErr := os.Stat(filepath.Join(dst, "gen2.txt")); !os.IsNotExist(statErr) {
		t.Fatal("dry run wrote a file")
	}
	after, err := LoadCursor(dst)
	if err != nil {
		t.Fatal(err)
	}
	if after.ID != before.ID {
		t.Fatal("dry run moved the cursor")
	}
}

func TestDryRunReportsConflictsWithoutFailing(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, dst, "readings.csv", "local\n")
	write(t, src, "readings.csv", "id,temp\n1,20.5\n2,21.0\n")
	inc := pack(t, src, base)
	res, err := Run(inc, dst, Options{DryRun: true})
	if err != nil {
		t.Fatalf("dry run should report, not fail: %v", err)
	}
	if len(res.Conflicts) != 1 {
		t.Fatalf("dry run must surface conflicts: %+v", res)
	}
}

func TestIdenticalPreexistingFileIsNotAConflict(t *testing.T) {
	// The destination already has the exact file the bundle adds (someone
	// copied it by hand). Apply must be idempotent for it, not fail.
	src, dst, base := setupSync(t)
	write(t, src, "shared.txt", "same bytes\n")
	write(t, dst, "shared.txt", "same bytes\n")
	inc := pack(t, src, base)
	res, err := Run(inc, dst, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Verified {
		t.Fatalf("idempotent add should verify: %+v", res)
	}
}

func TestPostVerifyCatchesStrayFileUnlessSkipped(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, dst, "stray.txt", "not from any bundle\n")
	write(t, src, "gen2.txt", "2")
	inc := pack(t, src, base)
	_, err := Run(inc, dst, Options{})
	var ve *VerifyError
	if !errors.As(err, &ve) {
		t.Fatalf("want VerifyError for the stray file, got %v", err)
	}
	if len(ve.Extra) != 1 || ve.Extra[0] != "stray.txt" {
		t.Fatalf("stray file not identified: %+v", ve)
	}
	// With NoVerify the same situation is tolerated and honestly reported.
	dst2 := t.TempDir()
	full := pack(t, src, emptySnap())
	res, err := Run(full, dst2, Options{NoVerify: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Verified {
		t.Fatal("NoVerify must not claim verification")
	}
	if read(t, dst2, "gen2.txt") != "2" {
		t.Fatal("apply itself should still land")
	}
}

func TestExecBitApplied(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, src, "tool.sh", "#!/bin/sh\necho hi\n")
	if err := os.Chmod(filepath.Join(src, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	inc := pack(t, src, base)
	if _, err := Run(inc, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dst, "tool.sh"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("executable bit not applied: %v %v", info, err)
	}
}

func TestDeletePrunesEmptyDirs(t *testing.T) {
	src, dst, base := setupSync(t)
	if err := os.Remove(filepath.Join(src, "notes/day1.md")); err != nil {
		t.Fatal(err)
	}
	inc := pack(t, src, base)
	if _, err := Run(inc, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dst, "notes")); !os.IsNotExist(err) {
		t.Fatal("emptied directory should be pruned")
	}
}

func TestDeleteKeepsNonEmptyDirs(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, src, "notes/keep.md", "stays\n")
	if err := os.Remove(filepath.Join(src, "notes/day1.md")); err != nil {
		t.Fatal(err)
	}
	inc := pack(t, src, base)
	if _, err := Run(inc, dst, Options{}); err != nil {
		t.Fatal(err)
	}
	if read(t, dst, "notes/keep.md") != "stays\n" {
		t.Fatal("sibling file must survive the prune")
	}
}

func TestApplyRefusesDamagedBundle(t *testing.T) {
	src, dst, base := setupSync(t)
	write(t, src, "gen2.txt", "2")
	inc := pack(t, src, base)
	data, err := os.ReadFile(inc)
	if err != nil {
		t.Fatal(err)
	}
	// Truncate: simulates the classic half-copied file on a USB stick.
	if err := os.WriteFile(inc, data[:len(data)-20], 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(inc, dst, Options{}); err == nil {
		t.Fatal("truncated bundle must be refused")
	}
	if _, statErr := os.Stat(filepath.Join(dst, "gen2.txt")); !os.IsNotExist(statErr) {
		t.Fatal("damaged bundle must not touch the tree")
	}
}

func TestLoadCursorOnFreshDirIsEmptyTree(t *testing.T) {
	cur, err := LoadCursor(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cur.ID != snapshot.EmptyID() || len(cur.Files) != 0 {
		t.Fatalf("fresh dir must start at the empty-tree cursor: %+v", cur)
	}
}

func TestApplyToMissingDirFails(t *testing.T) {
	src, _, _ := setupSync(t)
	full := pack(t, src, emptySnap())
	if _, err := Run(full, filepath.Join(t.TempDir(), "nope"), Options{}); err == nil {
		t.Fatal("nonexistent destination must be an error")
	}
}
