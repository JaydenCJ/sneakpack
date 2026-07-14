// Package apply lands a verified bundle on a destination tree.
//
// The pipeline is deliberately paranoid, in order:
//
//  1. Full bundle verification (never trust the courier).
//  2. Chain continuity — the bundle's base cursor must equal the
//     destination's current cursor, so bundles can only be applied in
//     order and never twice.
//  3. Conflict detection — every file the bundle would overwrite or
//     delete must still carry the hash the bundle expects; local edits
//     stop the apply instead of being destroyed.
//  4. Staged extraction — payloads are written and hash-checked inside
//     .sneakpack/ first, then renamed into place, so a bundle that dies
//     halfway through extraction leaves the tree untouched.
//  5. Post-apply verification — the tree is re-walked and its cursor must
//     equal the bundle's target cursor, proving the destination is now
//     byte-identical to the source at pack time.
package apply

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JaydenCJ/sneakpack/internal/bundle"
	"github.com/JaydenCJ/sneakpack/internal/diff"
	"github.com/JaydenCJ/sneakpack/internal/ignore"
	"github.com/JaydenCJ/sneakpack/internal/snapshot"
)

// Options tunes an apply run.
type Options struct {
	DryRun   bool // report the plan and conflicts, change nothing
	Force    bool // proceed despite continuity or conflict findings
	NoVerify bool // skip the post-apply tree walk (large trees, slow media)
}

// Conflict is one local obstacle found before anything was changed.
type Conflict struct {
	Path   string
	Reason string
}

// Result summarizes a completed (or dry-run) apply.
type Result struct {
	Added     int
	Modified  int
	Deleted   int
	Conflicts []Conflict // non-empty only with --force or --dry-run
	Verified  bool       // post-apply cursor matched the target
	Cursor    string     // destination cursor after the run
}

// ContinuityError means the bundle does not chain onto this destination.
type ContinuityError struct {
	Have string // destination's current cursor
	Want string // bundle's base cursor
}

func (e *ContinuityError) Error() string {
	return fmt.Sprintf("bundle does not chain: it was packed against cursor %s but this tree is at %s (apply intermediate bundles first, or --force to override)",
		snapshot.Short(e.Want), snapshot.Short(e.Have))
}

// ConflictError means local files block the apply.
type ConflictError struct {
	Conflicts []Conflict
}

func (e *ConflictError) Error() string {
	lines := make([]string, 0, len(e.Conflicts)+1)
	lines = append(lines, fmt.Sprintf("%d local conflict(s) block this bundle (--force to overwrite):", len(e.Conflicts)))
	for _, c := range e.Conflicts {
		lines = append(lines, fmt.Sprintf("  %s: %s", c.Path, c.Reason))
	}
	return strings.Join(lines, "\n")
}

// VerifyError means the tree does not match the target cursor after apply.
type VerifyError struct {
	Want    string
	Got     string
	Missing []string // in target, absent or different on disk
	Extra   []string // on disk, absent from target
}

func (e *VerifyError) Error() string {
	msg := fmt.Sprintf("post-apply verification failed: tree is %s, bundle promised %s", snapshot.Short(e.Got), snapshot.Short(e.Want))
	if len(e.Missing) > 0 {
		msg += fmt.Sprintf("; %d path(s) missing or differing (e.g. %s)", len(e.Missing), e.Missing[0])
	}
	if len(e.Extra) > 0 {
		msg += fmt.Sprintf("; %d untracked path(s) present (e.g. %s)", len(e.Extra), e.Extra[0])
	}
	return msg
}

// CursorPath returns where a destination tree stores its cursor.
func CursorPath(dir string) string {
	return filepath.Join(dir, snapshot.StateDir, "cursor.json")
}

// LoadCursor returns the destination's current cursor, or the empty-tree
// snapshot when the directory has never received a bundle.
func LoadCursor(dir string) (snapshot.Snapshot, error) {
	p := CursorPath(dir)
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return snapshot.Snapshot{Format: snapshot.Format, ID: snapshot.EmptyID()}, nil
	}
	return snapshot.Load(p)
}

// Run applies the bundle at bundlePath to dir. See the package comment
// for the pipeline; errors are typed so the CLI can pick exit codes.
func Run(bundlePath, dir string, opts Options) (Result, error) {
	var res Result

	// 1. Verify the bundle end to end before reading anything else.
	m, rep, err := bundle.Verify(bundlePath)
	if err != nil {
		return res, err
	}
	if !rep.OK() {
		return res, fmt.Errorf("bundle failed verification: %s", strings.Join(rep.Problems, "; "))
	}

	info, err := os.Stat(dir)
	if err != nil {
		return res, err
	}
	if !info.IsDir() {
		return res, fmt.Errorf("%s: not a directory", dir)
	}

	// 2. Chain continuity.
	cur, err := LoadCursor(dir)
	if err != nil {
		return res, err
	}
	if cur.ID != m.BaseCursor && !opts.Force {
		return res, &ContinuityError{Have: cur.ID, Want: m.BaseCursor}
	}

	// 3. Conflict detection against the actual files on disk.
	conflicts, err := findConflicts(dir, m.Changes)
	if err != nil {
		return res, err
	}
	res.Added = len(m.Changes.Added)
	res.Modified = len(m.Changes.Modified)
	res.Deleted = len(m.Changes.Deleted)
	res.Conflicts = conflicts
	res.Cursor = m.TargetCursor
	if len(conflicts) > 0 && !opts.Force && !opts.DryRun {
		return res, &ConflictError{Conflicts: conflicts}
	}
	if opts.DryRun {
		return res, nil
	}

	// 4. Stage, then land.
	stateDir := filepath.Join(dir, snapshot.StateDir)
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		return res, err
	}
	stage, err := os.MkdirTemp(stateDir, "stage-")
	if err != nil {
		return res, err
	}
	defer os.RemoveAll(stage)
	if _, err := bundle.ExtractTo(bundlePath, stage, m); err != nil {
		return res, fmt.Errorf("staging failed, tree untouched: %v", err)
	}
	for _, e := range m.Changes.PayloadFiles() {
		dst := filepath.Join(dir, filepath.FromSlash(e.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return res, err
		}
		if err := os.Rename(filepath.Join(stage, filepath.FromSlash(e.Path)), dst); err != nil {
			return res, err
		}
	}
	for _, d := range m.Changes.Deleted {
		p := filepath.Join(dir, filepath.FromSlash(d.Path))
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return res, err
		}
		pruneEmptyParents(dir, filepath.Dir(p))
	}

	// 5. Record the new cursor, then prove the tree matches it.
	if err := snapshot.Save(m.Target, CursorPath(dir)); err != nil {
		return res, err
	}
	if !opts.NoVerify {
		rules, err := ignore.Load(filepath.Join(dir, ".sneakpackignore"))
		if err != nil {
			return res, err
		}
		got, _, err := snapshot.Take(dir, rules)
		if err != nil {
			return res, err
		}
		if got.ID != m.TargetCursor {
			return res, verifyError(m.Target, got)
		}
		res.Verified = true
	}
	return res, nil
}

// findConflicts checks, without modifying anything, that every path the
// bundle will touch is in the state the bundle expects. An added file
// already present with the exact target content is not a conflict — the
// apply is idempotent for it.
func findConflicts(dir string, c diff.Changes) ([]Conflict, error) {
	var out []Conflict
	hashIfExists := func(rel string) (string, bool, error) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Lstat(p)
		if os.IsNotExist(err) {
			return "", false, nil
		}
		if err != nil {
			return "", false, err
		}
		if !info.Mode().IsRegular() {
			return "", true, nil // present but not hashable as a regular file
		}
		sum, _, err := snapshot.HashFile(p)
		if err != nil {
			return "", true, err
		}
		return sum, true, nil
	}
	for _, a := range c.Added {
		sum, exists, err := hashIfExists(a.Path)
		if err != nil {
			return nil, err
		}
		if exists && sum != a.SHA256 {
			out = append(out, Conflict{Path: a.Path, Reason: "exists locally with different content"})
		}
	}
	for _, m := range c.Modified {
		sum, exists, err := hashIfExists(m.Path)
		if err != nil {
			return nil, err
		}
		switch {
		case !exists:
			out = append(out, Conflict{Path: m.Path, Reason: "missing locally (bundle expects to modify it)"})
		case sum != m.OldSHA256 && sum != m.SHA256:
			out = append(out, Conflict{Path: m.Path, Reason: "modified locally since the base cursor"})
		}
	}
	for _, d := range c.Deleted {
		sum, exists, err := hashIfExists(d.Path)
		if err != nil {
			return nil, err
		}
		if exists && sum != d.OldSHA256 {
			out = append(out, Conflict{Path: d.Path, Reason: "modified locally, bundle wants to delete it"})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// pruneEmptyParents removes now-empty directories left behind by deletes,
// walking up until root or the first non-empty directory. Removal of a
// non-empty directory fails and stops the walk, which is exactly the
// behavior we want, so errors are intentionally ignored.
func pruneEmptyParents(root, p string) {
	root = filepath.Clean(root)
	for {
		p = filepath.Clean(p)
		if p == root || !strings.HasPrefix(p, root+string(filepath.Separator)) {
			return
		}
		if err := os.Remove(p); err != nil {
			return
		}
		p = filepath.Dir(p)
	}
}

// verifyError builds the detailed mismatch report for a failed
// post-apply verification.
func verifyError(want, got snapshot.Snapshot) *VerifyError {
	e := &VerifyError{Want: want.ID, Got: got.ID}
	wantIdx := want.Index()
	gotIdx := got.Index()
	for _, f := range want.Files {
		g, ok := gotIdx[f.Path]
		if !ok || g.SHA256 != f.SHA256 || g.Exec != f.Exec {
			e.Missing = append(e.Missing, f.Path)
		}
	}
	for _, f := range got.Files {
		if _, ok := wantIdx[f.Path]; !ok {
			e.Extra = append(e.Extra, f.Path)
		}
	}
	sort.Strings(e.Missing)
	sort.Strings(e.Extra)
	return e
}
