// Package snapshot captures the content state of a directory tree as a
// canonical, hashable manifest — the "cursor" that sneakpack bundles are
// packed against and verified with.
//
// A Snapshot lists every regular file (path, size, executable bit,
// SHA-256) in a canonical order. Its ID is the SHA-256 of a canonical
// serialization of that list, which gives cursors two load-bearing
// properties:
//
//   - Content-addressed: two trees with identical contents produce the
//     same cursor ID on any machine, so "does the destination match what
//     the source packed?" is a single string comparison.
//   - Tamper-evident: a cursor file whose ID no longer matches its own
//     file list is rejected on load, so a corrupted or hand-edited cursor
//     can never cause a silently wrong incremental bundle.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/JaydenCJ/sneakpack/internal/ignore"
)

// Format is the cursor/snapshot format version this build reads and writes.
const Format = 1

// StateDir is the per-directory state folder maintained on the receiving
// side (holds cursor.json and apply staging). It is never part of a
// snapshot and bundles are forbidden from writing into it.
const StateDir = ".sneakpack"

// FileEntry describes one regular file in a snapshot.
type FileEntry struct {
	Path   string `json:"path"`           // slash-separated, relative to the tree root
	Size   int64  `json:"size"`           // bytes
	Exec   bool   `json:"exec,omitempty"` // any executable bit set
	SHA256 string `json:"sha256"`         // lowercase hex content hash
}

// Snapshot is the full manifest of a directory tree at one point in time.
type Snapshot struct {
	Format int         `json:"format"`
	ID     string      `json:"id"`
	Files  []FileEntry `json:"files"`
}

// ComputeID returns the canonical SHA-256 identity of a file list. The
// serialization is one NUL-separated record per file in path order; it is
// independent of JSON formatting so re-encoding a cursor never changes
// its identity.
func ComputeID(files []FileEntry) string {
	sorted := make([]FileEntry, len(files))
	copy(sorted, files)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	h := sha256.New()
	for _, f := range sorted {
		exec := "-"
		if f.Exec {
			exec = "x"
		}
		fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\n", f.Path, strconv.FormatInt(f.Size, 10), exec, f.SHA256)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// EmptyID is the cursor ID of an empty tree — the implicit starting point
// of every destination that has never received a bundle.
func EmptyID() string {
	return ComputeID(nil)
}

// Take walks dir and returns its snapshot plus the relative paths of
// entries that were skipped because they are not regular files (symlinks,
// sockets, devices). Skips are returned rather than silently dropped so
// the CLI can warn: a field kit that quietly omits files is worse than
// one that says so.
//
// The .sneakpack state directory at the root and anything matched by
// rules are excluded. Ignored directories are pruned without descending.
func Take(dir string, rules *ignore.Rules) (Snapshot, []string, error) {
	var files []FileEntry
	var skipped []string
	root := filepath.Clean(dir)
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		if d.IsDir() {
			if rel == StateDir || rules.Match(rel, true) {
				return filepath.SkipDir
			}
			return nil
		}
		if rules.Match(rel, false) {
			return nil
		}
		if !d.Type().IsRegular() {
			skipped = append(skipped, rel)
			return nil
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		sum, size, hashErr := HashFile(p)
		if hashErr != nil {
			return hashErr
		}
		files = append(files, FileEntry{
			Path:   rel,
			Size:   size,
			Exec:   info.Mode()&0o111 != 0,
			SHA256: sum,
		})
		return nil
	})
	if err != nil {
		return Snapshot{}, nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	sort.Strings(skipped)
	return Snapshot{Format: Format, ID: ComputeID(files), Files: files}, skipped, nil
}

// HashFile returns the SHA-256 and byte size of one file, streaming so
// arbitrarily large payloads never need to fit in memory.
func HashFile(p string) (string, int64, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// Save writes the snapshot as a cursor file (indented JSON, trailing
// newline) suitable for committing, carrying on removable media, or
// diffing by hand.
func Save(s Snapshot, file string) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(file, append(data, '\n'), 0o644)
}

// Load reads a cursor file and validates it: format version, entry
// well-formedness, canonical ordering, and — critically — that the stored
// ID matches the file list. A cursor that fails any check is refused,
// because packing against a wrong cursor produces a bundle that will
// corrupt the destination's chain.
func Load(file string) (Snapshot, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return Snapshot{}, err
	}
	var s Snapshot
	if err := json.Unmarshal(data, &s); err != nil {
		return Snapshot{}, fmt.Errorf("%s: not a cursor file: %v", file, err)
	}
	if s.Format != Format {
		return Snapshot{}, fmt.Errorf("%s: unsupported cursor format %d (this build reads format %d)", file, s.Format, Format)
	}
	for i, f := range s.Files {
		if err := validateEntry(f); err != nil {
			return Snapshot{}, fmt.Errorf("%s: file entry %d: %v", file, i, err)
		}
		if i > 0 && s.Files[i-1].Path >= f.Path {
			return Snapshot{}, fmt.Errorf("%s: file entries not in canonical order at %q", file, f.Path)
		}
	}
	if got := ComputeID(s.Files); got != s.ID {
		return Snapshot{}, fmt.Errorf("%s: cursor ID mismatch: stored %s, computed %s (file corrupted or edited)", file, Short(s.ID), Short(got))
	}
	return s, nil
}

// validateEntry rejects malformed manifest entries early so every later
// stage (diff, pack, apply) can trust the invariants.
func validateEntry(f FileEntry) error {
	if err := ValidatePath(f.Path); err != nil {
		return err
	}
	if f.Size < 0 {
		return fmt.Errorf("negative size %d", f.Size)
	}
	if len(f.SHA256) != 64 || strings.ToLower(f.SHA256) != f.SHA256 {
		return fmt.Errorf("malformed sha256 %q", f.SHA256)
	}
	if _, err := hex.DecodeString(f.SHA256); err != nil {
		return fmt.Errorf("malformed sha256 %q", f.SHA256)
	}
	return nil
}

// ValidatePath enforces the rules for every path that may be written to a
// destination tree: relative, slash-separated, already clean, and never
// escaping the root or reaching into the .sneakpack state directory. This
// is the zip-slip guard — it runs on cursor load, bundle verify and apply.
func ValidatePath(p string) error {
	if p == "" {
		return fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "/") {
		return fmt.Errorf("absolute path %q", p)
	}
	if strings.Contains(p, "\\") {
		return fmt.Errorf("backslash in path %q (paths are slash-separated)", p)
	}
	clean := filepath.ToSlash(filepath.Clean(p))
	if clean != p {
		return fmt.Errorf("non-canonical path %q", p)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." || seg == "." || seg == "" {
			return fmt.Errorf("path %q escapes the tree root", p)
		}
	}
	if p == StateDir || strings.HasPrefix(p, StateDir+"/") {
		return fmt.Errorf("path %q reaches into the %s state directory", p, StateDir)
	}
	return nil
}

// Short abbreviates a cursor or bundle ID for human-facing output. Twelve
// hex characters are what people are used to reading from VCS tooling.
func Short(id string) string {
	if id == "" {
		return "(empty)"
	}
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// Index returns the snapshot's files keyed by path for O(1) lookups in
// diffing and verification.
func (s Snapshot) Index() map[string]FileEntry {
	m := make(map[string]FileEntry, len(s.Files))
	for _, f := range s.Files {
		m[f.Path] = f
	}
	return m
}
