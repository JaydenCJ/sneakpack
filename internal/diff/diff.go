// Package diff computes the change set between two snapshots — the exact
// payload a bundle has to carry to move a destination from the base
// cursor to the target cursor.
//
// Every change records enough to be applied safely on the other side:
// added and modified files carry the new content identity, and modified
// and deleted files also carry the hash the destination is expected to
// have (OldSHA256), which is what lets apply detect local edits that
// would otherwise be silently overwritten.
package diff

import (
	"sort"

	"github.com/JaydenCJ/sneakpack/internal/snapshot"
)

// Added is a file present in the target but not the base.
type Added struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Exec   bool   `json:"exec,omitempty"`
	SHA256 string `json:"sha256"`
}

// Modified is a file present in both whose content or executable bit
// differs. OldSHA256 pins what the destination should currently hold.
type Modified struct {
	Path      string `json:"path"`
	Size      int64  `json:"size"`
	Exec      bool   `json:"exec,omitempty"`
	SHA256    string `json:"sha256"`
	OldSHA256 string `json:"old_sha256"`
}

// Deleted is a file present in the base but not the target. OldSHA256
// pins what the destination should currently hold before removal.
type Deleted struct {
	Path      string `json:"path"`
	OldSHA256 string `json:"old_sha256"`
}

// Changes is a complete, canonically ordered change set.
type Changes struct {
	Added    []Added    `json:"added"`
	Modified []Modified `json:"modified"`
	Deleted  []Deleted  `json:"deleted"`
}

// Compute diffs base → target. Both inputs are snapshots, so the result
// is deterministic and sorted by path within each category.
func Compute(base, target snapshot.Snapshot) Changes {
	var c Changes
	baseIdx := base.Index()
	targetIdx := target.Index()

	for _, f := range target.Files {
		old, ok := baseIdx[f.Path]
		switch {
		case !ok:
			c.Added = append(c.Added, Added{Path: f.Path, Size: f.Size, Exec: f.Exec, SHA256: f.SHA256})
		case old.SHA256 != f.SHA256 || old.Exec != f.Exec:
			// An exec-bit flip with identical bytes is still a Modified:
			// the receiving side must re-stage the file to change its mode,
			// and the content is in the bundle so apply stays one code path.
			c.Modified = append(c.Modified, Modified{
				Path: f.Path, Size: f.Size, Exec: f.Exec,
				SHA256: f.SHA256, OldSHA256: old.SHA256,
			})
		}
	}
	for _, f := range base.Files {
		if _, ok := targetIdx[f.Path]; !ok {
			c.Deleted = append(c.Deleted, Deleted{Path: f.Path, OldSHA256: f.SHA256})
		}
	}
	sort.Slice(c.Added, func(i, j int) bool { return c.Added[i].Path < c.Added[j].Path })
	sort.Slice(c.Modified, func(i, j int) bool { return c.Modified[i].Path < c.Modified[j].Path })
	sort.Slice(c.Deleted, func(i, j int) bool { return c.Deleted[i].Path < c.Deleted[j].Path })
	return c
}

// Empty reports whether there is nothing to carry.
func (c Changes) Empty() bool {
	return len(c.Added) == 0 && len(c.Modified) == 0 && len(c.Deleted) == 0
}

// Count returns the total number of changed paths.
func (c Changes) Count() int {
	return len(c.Added) + len(c.Modified) + len(c.Deleted)
}

// PayloadFiles returns the entries whose content must travel in the
// bundle (adds and modifications), in canonical path order.
func (c Changes) PayloadFiles() []snapshot.FileEntry {
	out := make([]snapshot.FileEntry, 0, len(c.Added)+len(c.Modified))
	for _, a := range c.Added {
		out = append(out, snapshot.FileEntry{Path: a.Path, Size: a.Size, Exec: a.Exec, SHA256: a.SHA256})
	}
	for _, m := range c.Modified {
		out = append(out, snapshot.FileEntry{Path: m.Path, Size: m.Size, Exec: m.Exec, SHA256: m.SHA256})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// PayloadBytes returns the total content size that will travel in the
// bundle, before compression.
func (c Changes) PayloadBytes() int64 {
	var n int64
	for _, a := range c.Added {
		n += a.Size
	}
	for _, m := range c.Modified {
		n += m.Size
	}
	return n
}
