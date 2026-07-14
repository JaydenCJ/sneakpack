// Package bundle reads and writes .spk bundle files — the artifact that
// physically travels between machines.
//
// A bundle is a gzip-compressed tar stream with a strict layout:
//
//	sneakpack.json   the manifest: format version, base and target
//	                 cursors, the full change set, and the complete
//	                 target snapshot (so the receiving side can rebuild
//	                 its cursor without trusting its own filesystem walk)
//	data/<path>      one entry per added or modified file, in canonical
//	                 path order, mode 0644 or 0755 (executable)
//
// Two properties are deliberate:
//
//   - Deterministic: all timestamps are zeroed and entries are written in
//     canonical order, so packing the same tree against the same cursor
//     twice produces byte-identical bundles. Couriers can dedupe by hash.
//   - Verifiable offline: every payload entry's SHA-256 is pinned in the
//     manifest and the manifest is internally cross-checked, so a bundle
//     can be fully validated on an airgapped machine before it touches
//     the destination tree.
package bundle

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/JaydenCJ/sneakpack/internal/diff"
	"github.com/JaydenCJ/sneakpack/internal/snapshot"
	"github.com/JaydenCJ/sneakpack/internal/version"
)

// Format is the bundle format version this build reads and writes.
const Format = 1

// ManifestName is the tar entry name of the manifest; it must be the
// first entry so readers can reject foreign files without scanning them.
const ManifestName = "sneakpack.json"

// dataPrefix namespaces payload entries inside the tar stream.
const dataPrefix = "data/"

// Manifest is the bundle's self-description.
type Manifest struct {
	Format       int               `json:"format"`
	Tool         string            `json:"tool"`
	BaseCursor   string            `json:"base_cursor"`
	TargetCursor string            `json:"target_cursor"`
	Changes      diff.Changes      `json:"changes"`
	Target       snapshot.Snapshot `json:"target"`
}

// New builds the manifest that moves a destination from base to target.
func New(base, target snapshot.Snapshot) Manifest {
	c := diff.Compute(base, target)
	// Keep the JSON explicit: empty categories serialize as [] not null.
	if c.Added == nil {
		c.Added = []diff.Added{}
	}
	if c.Modified == nil {
		c.Modified = []diff.Modified{}
	}
	if c.Deleted == nil {
		c.Deleted = []diff.Deleted{}
	}
	return Manifest{
		Format:       Format,
		Tool:         "sneakpack " + version.Version,
		BaseCursor:   base.ID,
		TargetCursor: target.ID,
		Changes:      c,
		Target:       target,
	}
}

// Write creates the bundle file at path, streaming payload content from
// srcDir. Each payload file is re-hashed while it is copied; if anything
// changed between the snapshot walk and the pack, Write fails rather than
// sealing a bundle whose manifest lies about its own contents. The
// returned size is the final bundle size in bytes.
func Write(path string, m Manifest, srcDir string) (int64, error) {
	if err := validateManifest(m); err != nil {
		return 0, fmt.Errorf("refusing to write invalid bundle: %v", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	gz := gzip.NewWriter(f) // zero-valued header: no name, no mtime → deterministic
	tw := tar.NewWriter(gz)

	manifestJSON, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return 0, err
	}
	manifestJSON = append(manifestJSON, '\n')
	if err := writeEntry(tw, ManifestName, 0o644, manifestJSON); err != nil {
		return 0, err
	}

	for _, entry := range m.Changes.PayloadFiles() {
		if err := writePayload(tw, srcDir, entry); err != nil {
			return 0, err
		}
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// writeEntry writes one fully in-memory tar entry with zeroed metadata.
func writeEntry(tw *tar.Writer, name string, mode int64, data []byte) error {
	hdr := &tar.Header{
		Name:     name,
		Mode:     mode,
		Size:     int64(len(data)),
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	_, err := tw.Write(data)
	return err
}

// writePayload streams one source file into the tar, verifying that its
// size and hash still match the manifest entry.
func writePayload(tw *tar.Writer, srcDir string, e snapshot.FileEntry) error {
	mode := int64(0o644)
	if e.Exec {
		mode = 0o755
	}
	hdr := &tar.Header{
		Name:     dataPrefix + e.Path,
		Mode:     mode,
		Size:     e.Size,
		Typeflag: tar.TypeReg,
		Format:   tar.FormatPAX,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	src, err := os.Open(filepath.Join(srcDir, filepath.FromSlash(e.Path)))
	if err != nil {
		return err
	}
	defer src.Close()
	h := sha256.New()
	n, err := io.Copy(tw, io.TeeReader(io.LimitReader(src, e.Size), h))
	if err != nil {
		return err
	}
	if n != e.Size {
		return fmt.Errorf("%s: file shrank while packing (want %d bytes, read %d)", e.Path, e.Size, n)
	}
	// One extra read distinguishes "exactly e.Size" from "grew meanwhile".
	var probe [1]byte
	if m, _ := src.Read(probe[:]); m != 0 {
		return fmt.Errorf("%s: file grew while packing", e.Path)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
		return fmt.Errorf("%s: file changed while packing (hash mismatch)", e.Path)
	}
	return nil
}

// ReadManifest opens a bundle and returns its validated manifest without
// reading the payload. Used by inspect and as the first step of verify
// and apply.
func ReadManifest(path string) (Manifest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Manifest{}, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: not a sneakpack bundle (bad gzip: %v)", path, err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	hdr, err := tr.Next()
	if err != nil {
		return Manifest{}, fmt.Errorf("%s: not a sneakpack bundle (empty archive)", path)
	}
	if hdr.Name != ManifestName {
		return Manifest{}, fmt.Errorf("%s: not a sneakpack bundle (first entry is %q, want %q)", path, hdr.Name, ManifestName)
	}
	var m Manifest
	dec := json.NewDecoder(tr)
	if err := dec.Decode(&m); err != nil {
		return Manifest{}, fmt.Errorf("%s: bad manifest: %v", path, err)
	}
	if err := validateManifest(m); err != nil {
		return Manifest{}, fmt.Errorf("%s: %v", path, err)
	}
	return m, nil
}

// Report is the outcome of a full verification pass.
type Report struct {
	DataFiles int      // payload entries checked byte-for-byte
	Problems  []string // human-readable findings; empty means the bundle is sound
}

// OK reports whether verification found no problems.
func (r Report) OK() bool { return len(r.Problems) == 0 }

// Verify reads the entire bundle and cross-checks everything that can be
// checked offline: manifest self-consistency, payload completeness, and
// the byte-for-byte hash of every data entry. It never modifies anything
// and collects all findings instead of stopping at the first, so a courier
// can report the full damage after a bad transfer.
func Verify(path string) (Manifest, Report, error) {
	var rep Report
	m, err := ReadManifest(path)
	if err != nil {
		return Manifest{}, rep, err
	}

	want := make(map[string]snapshot.FileEntry)
	for _, e := range m.Changes.PayloadFiles() {
		want[e.Path] = e
	}

	f, err := os.Open(path)
	if err != nil {
		return m, rep, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return m, rep, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)

	seen := make(map[string]bool)
	first := true
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, rep, fmt.Errorf("%s: corrupt archive: %v", path, err)
		}
		if first {
			first = false
			if hdr.Name == ManifestName {
				if _, err := io.Copy(io.Discard, tr); err != nil {
					return m, rep, err
				}
				continue
			}
		}
		name := hdr.Name
		if !strings.HasPrefix(name, dataPrefix) {
			rep.Problems = append(rep.Problems, fmt.Sprintf("unexpected entry %q", name))
			continue
		}
		rel := strings.TrimPrefix(name, dataPrefix)
		e, ok := want[rel]
		if !ok {
			rep.Problems = append(rep.Problems, fmt.Sprintf("payload %q is not in the manifest", rel))
			continue
		}
		if seen[rel] {
			rep.Problems = append(rep.Problems, fmt.Sprintf("payload %q appears twice", rel))
			continue
		}
		seen[rel] = true
		h := sha256.New()
		n, err := io.Copy(h, tr)
		if err != nil {
			return m, rep, fmt.Errorf("%s: corrupt archive at %q: %v", path, rel, err)
		}
		if n != e.Size {
			rep.Problems = append(rep.Problems, fmt.Sprintf("payload %q: size %d, manifest says %d", rel, n, e.Size))
			continue
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
			rep.Problems = append(rep.Problems, fmt.Sprintf("payload %q: content hash mismatch", rel))
			continue
		}
		rep.DataFiles++
	}
	missing := make([]string, 0)
	for p := range want {
		if !seen[p] {
			missing = append(missing, p)
		}
	}
	sort.Strings(missing)
	for _, p := range missing {
		rep.Problems = append(rep.Problems, fmt.Sprintf("payload %q missing from archive", p))
	}
	sort.Strings(rep.Problems)
	return m, rep, nil
}

// ExtractTo streams every payload entry into stageDir, re-verifying each
// hash on the way out. It returns the number of files written. Paths were
// validated by the manifest checks, so nothing can land outside stageDir.
func ExtractTo(path, stageDir string, m Manifest) (int, error) {
	want := make(map[string]snapshot.FileEntry)
	for _, e := range m.Changes.PayloadFiles() {
		want[e.Path] = e
	}
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return 0, err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	written := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return written, fmt.Errorf("corrupt archive: %v", err)
		}
		if !strings.HasPrefix(hdr.Name, dataPrefix) {
			continue
		}
		rel := strings.TrimPrefix(hdr.Name, dataPrefix)
		e, ok := want[rel]
		if !ok {
			return written, fmt.Errorf("payload %q is not in the manifest", rel)
		}
		if err := snapshot.ValidatePath(rel); err != nil {
			return written, err
		}
		dst := filepath.Join(stageDir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return written, err
		}
		mode := os.FileMode(0o644)
		if e.Exec {
			mode = 0o755
		}
		out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
		if err != nil {
			return written, err
		}
		h := sha256.New()
		n, err := io.Copy(out, io.TeeReader(tr, h))
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return written, fmt.Errorf("extracting %q: %v", rel, err)
		}
		if n != e.Size {
			return written, fmt.Errorf("payload %q: size %d, manifest says %d", rel, n, e.Size)
		}
		if got := hex.EncodeToString(h.Sum(nil)); got != e.SHA256 {
			return written, fmt.Errorf("payload %q: content hash mismatch", rel)
		}
		written++
	}
	if written != len(want) {
		return written, fmt.Errorf("archive holds %d payload file(s), manifest lists %d", written, len(want))
	}
	return written, nil
}

// validateManifest cross-checks every internal claim a manifest makes.
// A bundle that passes is safe to reason about: its target snapshot is
// self-consistent, its change set agrees with that snapshot, and every
// path is safe to write beneath a destination root.
func validateManifest(m Manifest) error {
	if m.Format != Format {
		return fmt.Errorf("unsupported bundle format %d (this build reads format %d)", m.Format, Format)
	}
	if !isHexID(m.BaseCursor) || !isHexID(m.TargetCursor) {
		return fmt.Errorf("malformed cursor IDs in manifest")
	}
	if m.Target.Format != snapshot.Format {
		return fmt.Errorf("unsupported target snapshot format %d", m.Target.Format)
	}
	targetIdx := make(map[string]snapshot.FileEntry, len(m.Target.Files))
	for i, e := range m.Target.Files {
		if err := snapshot.ValidatePath(e.Path); err != nil {
			return fmt.Errorf("target snapshot: %v", err)
		}
		if i > 0 && m.Target.Files[i-1].Path >= e.Path {
			return fmt.Errorf("target snapshot not in canonical order at %q", e.Path)
		}
		targetIdx[e.Path] = e
	}
	if got := snapshot.ComputeID(m.Target.Files); got != m.Target.ID {
		return fmt.Errorf("target snapshot ID mismatch (stored %s, computed %s)", snapshot.Short(m.Target.ID), snapshot.Short(got))
	}
	if m.TargetCursor != m.Target.ID {
		return fmt.Errorf("target cursor %s does not match target snapshot %s", snapshot.Short(m.TargetCursor), snapshot.Short(m.Target.ID))
	}
	seen := make(map[string]bool)
	claim := func(p string) error {
		if err := snapshot.ValidatePath(p); err != nil {
			return err
		}
		if seen[p] {
			return fmt.Errorf("path %q appears in more than one change", p)
		}
		seen[p] = true
		return nil
	}
	for _, a := range m.Changes.Added {
		if err := claim(a.Path); err != nil {
			return err
		}
		t, ok := targetIdx[a.Path]
		if !ok || t.SHA256 != a.SHA256 || t.Size != a.Size || t.Exec != a.Exec {
			return fmt.Errorf("added %q disagrees with the target snapshot", a.Path)
		}
	}
	for _, mo := range m.Changes.Modified {
		if err := claim(mo.Path); err != nil {
			return err
		}
		if !isHexID(mo.OldSHA256) {
			return fmt.Errorf("modified %q: malformed old hash", mo.Path)
		}
		t, ok := targetIdx[mo.Path]
		if !ok || t.SHA256 != mo.SHA256 || t.Size != mo.Size || t.Exec != mo.Exec {
			return fmt.Errorf("modified %q disagrees with the target snapshot", mo.Path)
		}
	}
	for _, d := range m.Changes.Deleted {
		if err := claim(d.Path); err != nil {
			return err
		}
		if !isHexID(d.OldSHA256) {
			return fmt.Errorf("deleted %q: malformed old hash", d.Path)
		}
		if _, ok := targetIdx[d.Path]; ok {
			return fmt.Errorf("deleted %q is still present in the target snapshot", d.Path)
		}
	}
	return nil
}

// isHexID reports whether s is a lowercase 64-char hex digest.
func isHexID(s string) bool {
	if len(s) != 64 || strings.ToLower(s) != s {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
