// Tests for the .spk wire format: determinism, round-trips, and — most
// importantly — that every category of damaged or hostile bundle is
// detected before anything could act on it.
package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func take(t *testing.T, dir string) snapshot.Snapshot {
	t.Helper()
	s, _, err := snapshot.Take(dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// packDir writes a full bundle of dir and returns its path and manifest.
func packDir(t *testing.T, dir string) (string, Manifest) {
	t.Helper()
	empty := snapshot.Snapshot{Format: snapshot.Format, ID: snapshot.EmptyID()}
	m := New(empty, take(t, dir))
	out := filepath.Join(t.TempDir(), "b.spk")
	if _, err := Write(out, m, dir); err != nil {
		t.Fatalf("Write: %v", err)
	}
	return out, m
}

// rawEntry is one tar entry for hand-crafted (usually invalid) bundles.
type rawEntry struct {
	name string
	data []byte
}

// writeRaw builds a gzip+tar stream with exactly the given entries, so
// tests can produce bundles that Write itself would refuse to create.
func writeRaw(t *testing.T, path string, entries []rawEntry) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.data)), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestWriteReadManifestRoundTrip(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "alpha")
	write(t, dir, "sub/b.txt", "beta")
	path, m := packDir(t, dir)
	got, err := ReadManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.TargetCursor != m.TargetCursor || len(got.Changes.Added) != 2 {
		t.Fatalf("manifest round trip lost data: %+v", got)
	}
}

func TestBundlesAreByteIdentical(t *testing.T) {
	// Determinism is a shipped feature: couriers dedupe bundles by hash,
	// so the same tree against the same cursor must serialize identically.
	dir := t.TempDir()
	write(t, dir, "a.txt", "alpha")
	write(t, dir, "b.txt", "beta")
	p1, _ := packDir(t, dir)
	p2, _ := packDir(t, dir)
	b1, _ := os.ReadFile(p1)
	b2, _ := os.ReadFile(p2)
	if !bytes.Equal(b1, b2) {
		t.Fatal("packing twice must produce byte-identical bundles")
	}
}

func TestVerifyAcceptsSoundBundle(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "a.txt", "alpha")
	write(t, dir, "b/c.txt", "gamma")
	path, _ := packDir(t, dir)
	_, rep, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	if !rep.OK() || rep.DataFiles != 2 {
		t.Fatalf("sound bundle failed verification: %+v", rep)
	}
}

// craftedManifest builds a valid one-file manifest whose payload the
// individual tests then supply correctly or incorrectly.
func craftedManifest(content string) Manifest {
	entry := snapshot.FileEntry{Path: "data.txt", Size: int64(len(content)), SHA256: sha(content)}
	target := snapshot.Snapshot{Format: snapshot.Format, Files: []snapshot.FileEntry{entry}}
	target.ID = snapshot.ComputeID(target.Files)
	empty := snapshot.Snapshot{Format: snapshot.Format, ID: snapshot.EmptyID()}
	return New(empty, target)
}

func TestVerifyDetectsCorruptedPayload(t *testing.T) {
	m := craftedManifest("genuine content")
	path := filepath.Join(t.TempDir(), "bad.spk")
	writeRaw(t, path, []rawEntry{
		{ManifestName, mustJSON(t, m)},
		{"data/data.txt", []byte("twisted content")}, // same length, wrong bytes
	})
	_, rep, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || !strings.Contains(strings.Join(rep.Problems, ";"), "hash mismatch") {
		t.Fatalf("corrupted payload must be reported, got %+v", rep)
	}
	// A truncated payload is reported as a size problem, not a hash one.
	writeRaw(t, path, []rawEntry{
		{ManifestName, mustJSON(t, m)},
		{"data/data.txt", []byte("short")},
	})
	_, rep, err = Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || !strings.Contains(strings.Join(rep.Problems, ";"), "size") {
		t.Fatalf("size mismatch must be reported, got %+v", rep)
	}
}

func TestVerifyDetectsMissingPayload(t *testing.T) {
	m := craftedManifest("genuine content")
	path := filepath.Join(t.TempDir(), "bad.spk")
	writeRaw(t, path, []rawEntry{{ManifestName, mustJSON(t, m)}})
	_, rep, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	if rep.OK() || !strings.Contains(strings.Join(rep.Problems, ";"), "missing from archive") {
		t.Fatalf("missing payload must be reported, got %+v", rep)
	}
}

func TestVerifyDetectsUnknownAndDuplicateEntries(t *testing.T) {
	content := "genuine content"
	m := craftedManifest(content)
	path := filepath.Join(t.TempDir(), "bad.spk")
	writeRaw(t, path, []rawEntry{
		{ManifestName, mustJSON(t, m)},
		{"data/data.txt", []byte(content)},
		{"data/data.txt", []byte(content)}, // duplicate
		{"stowaway.bin", []byte("??")},     // outside data/
	})
	_, rep, err := Verify(path)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(rep.Problems, ";")
	if rep.OK() || !strings.Contains(joined, "twice") || !strings.Contains(joined, "unexpected entry") {
		t.Fatalf("duplicate and stowaway entries must be reported, got %+v", rep)
	}
}

func TestReadManifestRejectsForeignFiles(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not.spk")
	if err := os.WriteFile(path, []byte("plain text, not gzip"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadManifest(path); err == nil || !strings.Contains(err.Error(), "not a sneakpack bundle") {
		t.Fatalf("plain file must be rejected clearly, got %v", err)
	}
	// A gzip+tar file that is not a bundle must be named as such too.
	odd := filepath.Join(t.TempDir(), "odd.spk")
	writeRaw(t, odd, []rawEntry{{"data/x", []byte("x")}})
	if _, err := ReadManifest(odd); err == nil || !strings.Contains(err.Error(), "first entry") {
		t.Fatalf("manifest-less archive must be rejected, got %v", err)
	}
}

func TestManifestRejectsPathTraversal(t *testing.T) {
	// A hostile bundle must not be able to write outside the tree root.
	m := craftedManifest("x")
	m.Changes.Added[0].Path = "../../etc/evil"
	m.Target.Files[0].Path = "../../etc/evil"
	m.Target.ID = snapshot.ComputeID(m.Target.Files)
	m.TargetCursor = m.Target.ID
	path := filepath.Join(t.TempDir(), "evil.spk")
	writeRaw(t, path, []rawEntry{{ManifestName, mustJSON(t, m)}})
	if _, err := ReadManifest(path); err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Fatalf("path traversal must be rejected at manifest load, got %v", err)
	}
}

func TestManifestRejectsStateDirWrites(t *testing.T) {
	// Writing into .sneakpack/ would let a bundle forge the destination's
	// cursor; it is rejected as a path violation.
	m := craftedManifest("x")
	m.Changes.Added[0].Path = ".sneakpack/cursor.json"
	m.Target.Files[0].Path = ".sneakpack/cursor.json"
	m.Target.ID = snapshot.ComputeID(m.Target.Files)
	m.TargetCursor = m.Target.ID
	path := filepath.Join(t.TempDir(), "forge.spk")
	writeRaw(t, path, []rawEntry{{ManifestName, mustJSON(t, m)}})
	if _, err := ReadManifest(path); err == nil || !strings.Contains(err.Error(), "state directory") {
		t.Fatalf("state-dir write must be rejected, got %v", err)
	}
}

func TestManifestRejectsCursorInconsistency(t *testing.T) {
	m := craftedManifest("x")
	m.TargetCursor = strings.Repeat("1", 64) // no longer matches the target snapshot
	path := filepath.Join(t.TempDir(), "lying.spk")
	writeRaw(t, path, []rawEntry{{ManifestName, mustJSON(t, m)}})
	if _, err := ReadManifest(path); err == nil || !strings.Contains(err.Error(), "target cursor") {
		t.Fatalf("cursor/snapshot disagreement must be rejected, got %v", err)
	}
}

func TestManifestRejectsChangeSnapshotDisagreement(t *testing.T) {
	m := craftedManifest("x")
	m.Changes.Added[0].SHA256 = strings.Repeat("2", 64) // claims different content than the target
	path := filepath.Join(t.TempDir(), "split.spk")
	writeRaw(t, path, []rawEntry{{ManifestName, mustJSON(t, m)}})
	if _, err := ReadManifest(path); err == nil || !strings.Contains(err.Error(), "disagrees") {
		t.Fatalf("change/target disagreement must be rejected, got %v", err)
	}
}

func TestWriteFailsWhenFileChangesAfterSnapshot(t *testing.T) {
	// The race everyone hits eventually: a file is edited between the
	// walk and the pack. Sealing a bundle whose manifest is already wrong
	// would poison the chain, so Write must fail instead.
	dir := t.TempDir()
	write(t, dir, "live.txt", "before")
	empty := snapshot.Snapshot{Format: snapshot.Format, ID: snapshot.EmptyID()}
	m := New(empty, take(t, dir))
	write(t, dir, "live.txt", "after!") // same length, different bytes
	_, err := Write(filepath.Join(t.TempDir(), "race.spk"), m, dir)
	if err == nil || !strings.Contains(err.Error(), "changed while packing") {
		t.Fatalf("mid-pack change must abort the write, got %v", err)
	}
	// Growth is caught separately from content drift.
	write(t, dir, "live.txt", "before and considerably more")
	_, err = Write(filepath.Join(t.TempDir(), "race2.spk"), m, dir)
	if err == nil || !strings.Contains(err.Error(), "grew while packing") {
		t.Fatalf("mid-pack growth must abort the write, got %v", err)
	}
}

func TestExtractToWritesVerifiedPayloads(t *testing.T) {
	dir := t.TempDir()
	write(t, dir, "plain.txt", "content")
	write(t, dir, "tool.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "tool.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	path, m := packDir(t, dir)
	stage := t.TempDir()
	n, err := ExtractTo(path, stage, m)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("extracted %d files, want 2", n)
	}
	data, err := os.ReadFile(filepath.Join(stage, "plain.txt"))
	if err != nil || string(data) != "content" {
		t.Fatalf("payload content wrong: %q, %v", data, err)
	}
	info, err := os.Stat(filepath.Join(stage, "tool.sh"))
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("executable bit lost in extraction: %v %v", info, err)
	}
}

func TestExtractToRejectsCorruptPayload(t *testing.T) {
	m := craftedManifest("genuine content")
	path := filepath.Join(t.TempDir(), "bad.spk")
	writeRaw(t, path, []rawEntry{
		{ManifestName, mustJSON(t, m)},
		{"data/data.txt", []byte("twisted content")},
	})
	if _, err := ExtractTo(path, t.TempDir(), m); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("extraction must re-verify hashes, got %v", err)
	}
}

func TestLongPathsSurviveRoundTrip(t *testing.T) {
	// Paths beyond the 100-byte USTAR name field exercise PAX headers.
	dir := t.TempDir()
	long := strings.Repeat("verylongsegment/", 8) + "leaf.txt"
	write(t, dir, long, "deep")
	path, m := packDir(t, dir)
	stage := t.TempDir()
	if _, err := ExtractTo(path, stage, m); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(stage, filepath.FromSlash(long)))
	if err != nil || string(data) != "deep" {
		t.Fatalf("long path lost: %v", err)
	}
}

func TestWriteRefusesInvalidManifest(t *testing.T) {
	m := craftedManifest("x")
	m.Changes.Added = append(m.Changes.Added, m.Changes.Added[0]) // duplicate change
	_, err := Write(filepath.Join(t.TempDir(), "dup.spk"), m, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "more than one change") {
		t.Fatalf("duplicate change paths must be refused at write time, got %v", err)
	}
}

func TestEmptyChangeBundleRoundTrips(t *testing.T) {
	// --allow-empty produces a bundle with no payload; it must still be
	// structurally valid and verify clean (useful as a signed heartbeat).
	dir := t.TempDir()
	write(t, dir, "a.txt", "same")
	s := take(t, dir)
	m := New(s, s)
	path := filepath.Join(t.TempDir(), "empty.spk")
	if _, err := Write(path, m, dir); err != nil {
		t.Fatal(err)
	}
	_, rep, err := Verify(path)
	if err != nil || !rep.OK() || rep.DataFiles != 0 {
		t.Fatalf("empty bundle should verify clean: %+v %v", rep, err)
	}
}
