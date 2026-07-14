// Tests for change-set computation between two snapshots.
package diff

import (
	"strings"
	"testing"

	"github.com/JaydenCJ/sneakpack/internal/snapshot"
)

// snap builds a snapshot from path→"sha marker" pairs. The marker is
// padded to a syntactically valid digest so entries compare like real
// ones; a trailing "*" marks the file executable.
func snap(files map[string]string) snapshot.Snapshot {
	var entries []snapshot.FileEntry
	for p, marker := range files {
		exec := strings.HasSuffix(marker, "*")
		marker = strings.TrimSuffix(marker, "*")
		sha := marker + strings.Repeat("0", 64-len(marker))
		entries = append(entries, snapshot.FileEntry{Path: p, Size: int64(len(marker)), Exec: exec, SHA256: sha})
	}
	return snapshot.Snapshot{Format: snapshot.Format, ID: snapshot.ComputeID(entries), Files: entries}
}

func TestAllAddedFromEmptyBase(t *testing.T) {
	c := Compute(snap(nil), snap(map[string]string{"a.txt": "a1", "b/c.txt": "c1"}))
	if len(c.Added) != 2 || len(c.Modified) != 0 || len(c.Deleted) != 0 {
		t.Fatalf("want 2 adds only, got %+v", c)
	}
}

func TestAllDeletedToEmptyTarget(t *testing.T) {
	c := Compute(snap(map[string]string{"a.txt": "a1"}), snap(nil))
	if len(c.Deleted) != 1 || c.Deleted[0].Path != "a.txt" {
		t.Fatalf("want one delete, got %+v", c)
	}
}

func TestModifiedByContent(t *testing.T) {
	c := Compute(
		snap(map[string]string{"a.txt": "a1"}),
		snap(map[string]string{"a.txt": "a2"}),
	)
	if len(c.Modified) != 1 || c.Modified[0].Path != "a.txt" {
		t.Fatalf("want one modification, got %+v", c)
	}
}

func TestExecBitFlipAloneIsModified(t *testing.T) {
	// Same bytes, different mode: the destination still has to re-stage
	// the file, so it must be carried as a modification.
	c := Compute(
		snap(map[string]string{"run.sh": "s1"}),
		snap(map[string]string{"run.sh": "s1*"}),
	)
	if len(c.Modified) != 1 || !c.Modified[0].Exec {
		t.Fatalf("exec flip must be a modification, got %+v", c)
	}
}

func TestNoChangesIsEmpty(t *testing.T) {
	s := snap(map[string]string{"a.txt": "a1", "b.txt": "b1"})
	c := Compute(s, s)
	if !c.Empty() || c.Count() != 0 {
		t.Fatalf("identical snapshots must diff empty, got %+v", c)
	}
}

func TestOldHashesRecorded(t *testing.T) {
	// OldSHA256 is what apply's conflict detection compares against;
	// dropping it would make local-edit detection impossible.
	base := snap(map[string]string{"mod.txt": "m1", "del.txt": "d1"})
	target := snap(map[string]string{"mod.txt": "m2"})
	c := Compute(base, target)
	if c.Modified[0].OldSHA256 != "m1"+strings.Repeat("0", 62) {
		t.Fatalf("modification lost the old hash: %+v", c.Modified[0])
	}
	if c.Deleted[0].OldSHA256 != "d1"+strings.Repeat("0", 62) {
		t.Fatalf("deletion lost the old hash: %+v", c.Deleted[0])
	}
}

func TestChangesSortedByPath(t *testing.T) {
	base := snap(map[string]string{"z.txt": "z1", "m.txt": "m1"})
	target := snap(map[string]string{"a.txt": "a1", "b.txt": "b1", "m.txt": "m2"})
	c := Compute(base, target)
	if c.Added[0].Path != "a.txt" || c.Added[1].Path != "b.txt" {
		t.Fatalf("adds not sorted: %+v", c.Added)
	}
	if c.Deleted[0].Path != "z.txt" {
		t.Fatalf("unexpected deletes: %+v", c.Deleted)
	}
}

func TestPayloadCoversAddsAndModsWithSizes(t *testing.T) {
	base := snap(map[string]string{"mod.txt": "m1", "del.txt": "d1"})
	target := snap(map[string]string{"mod.txt": "m2", "new.txt": "n1"})
	c := Compute(base, target)
	pf := c.PayloadFiles()
	if len(pf) != 2 || pf[0].Path != "mod.txt" || pf[1].Path != "new.txt" {
		t.Fatalf("payload must be adds+mods in path order, got %+v", pf)
	}
	if got := c.PayloadBytes(); got != 4 { // "m2" + "n1"
		t.Fatalf("PayloadBytes = %d, want 4", got)
	}
	if got := c.Count(); got != 3 {
		t.Fatalf("Count = %d, want 3", got)
	}
}
