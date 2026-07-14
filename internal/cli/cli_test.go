// In-process integration tests for the sneakpack CLI: every subcommand,
// every exit code, driven through Run with captured streams — no PATH,
// no subprocesses, no working-directory coupling.
package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JaydenCJ/sneakpack/internal/snapshot"
	"github.com/JaydenCJ/sneakpack/internal/version"
)

// run executes the CLI in-process and returns exit code, stdout, stderr.
func run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := Run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

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

// mustRun asserts exit code 0 and returns stdout.
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := run(t, args...)
	if code != 0 {
		t.Fatalf("sneakpack %v: exit %d\nstdout: %s\nstderr: %s", args, code, out, errOut)
	}
	return out
}

// srcTree builds a small source directory.
func srcTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "notes/day1.md", "day one\n")
	write(t, dir, "readings.csv", "id,temp\n1,20.5\n")
	return dir
}

func TestVersionAndHelp(t *testing.T) {
	code, out, _ := run(t, "--version")
	if code != 0 || out != "sneakpack "+version.Version+"\n" {
		t.Fatalf("version: exit %d, out %q", code, out)
	}
	code, out, _ = run(t, "--help")
	if code != 0 || !strings.Contains(out, "sneakpack apply") {
		t.Fatalf("help broken: exit %d, out %q", code, out)
	}
}

func TestUsageErrorsExitTwo(t *testing.T) {
	code, _, errOut := run(t)
	if code != 2 || !strings.Contains(errOut, "Usage:") {
		t.Fatalf("no args: exit %d, stderr %q", code, errOut)
	}
	code, _, errOut = run(t, "teleport")
	if code != 2 || !strings.Contains(errOut, `unknown command "teleport"`) {
		t.Fatalf("unknown command: exit %d, stderr %q", code, errOut)
	}
}

func TestSnapshotWritesCursorFile(t *testing.T) {
	dir := srcTree(t)
	cursor := filepath.Join(t.TempDir(), "base.cursor")
	out := mustRun(t, "snapshot", dir, "-o", cursor)
	if !strings.Contains(out, "2 file(s)") {
		t.Fatalf("summary wrong: %q", out)
	}
	s, err := snapshot.Load(cursor)
	if err != nil || len(s.Files) != 2 {
		t.Fatalf("cursor unusable: %v %+v", err, s)
	}
	if code, _, _ := run(t, "snapshot", filepath.Join(t.TempDir(), "gone")); code != 2 {
		t.Fatalf("snapshot of a missing dir should exit 2, got %d", code)
	}
}

func TestSnapshotStdoutIsValidCursorJSON(t *testing.T) {
	dir := srcTree(t)
	out := mustRun(t, "snapshot", dir)
	var s snapshot.Snapshot
	if err := json.Unmarshal([]byte(out), &s); err != nil {
		t.Fatalf("stdout is not cursor JSON: %v\n%s", err, out)
	}
	if s.ID != snapshot.ComputeID(s.Files) {
		t.Fatal("stdout cursor has an inconsistent ID")
	}
}

func TestStatusCleanExitsZero(t *testing.T) {
	dir := srcTree(t)
	cursor := filepath.Join(t.TempDir(), "c.json")
	mustRun(t, "snapshot", dir, "-o", cursor)
	code, out, _ := run(t, "status", dir, "--since", cursor)
	if code != 0 || !strings.Contains(out, "clean:") {
		t.Fatalf("exit %d, out %q", code, out)
	}
}

func TestStatusDirtyExitsOneAndListsChanges(t *testing.T) {
	dir := srcTree(t)
	cursor := filepath.Join(t.TempDir(), "c.json")
	mustRun(t, "snapshot", dir, "-o", cursor)
	write(t, dir, "new.txt", "n")
	write(t, dir, "readings.csv", "id,temp\n1,20.5\n2,21.0\n")
	if err := os.Remove(filepath.Join(dir, "notes/day1.md")); err != nil {
		t.Fatal(err)
	}
	code, out, _ := run(t, "status", dir, "--since", cursor)
	if code != 1 {
		t.Fatalf("dirty status must exit 1, got %d", code)
	}
	for _, want := range []string{"A  new.txt", "M  readings.csv", "D  notes/day1.md", "1 added, 1 modified, 1 deleted"} {
		if !strings.Contains(out, want) {
			t.Errorf("status output missing %q:\n%s", want, out)
		}
	}
}

func TestPackNeedsExactlyOneBaseMode(t *testing.T) {
	dir := srcTree(t)
	out := filepath.Join(t.TempDir(), "b.spk")
	code, _, errOut := run(t, "pack", dir, "-o", out)
	if code != 2 || !strings.Contains(errOut, "exactly one of") {
		t.Fatalf("neither mode: exit %d, %q", code, errOut)
	}
	cursor := filepath.Join(t.TempDir(), "c.json")
	mustRun(t, "snapshot", dir, "-o", cursor)
	code, _, _ = run(t, "pack", dir, "-o", out, "--full", "--since", cursor)
	if code != 2 {
		t.Fatalf("both modes must be a usage error, got %d", code)
	}
}

func TestPackWithNoChangesExitsOne(t *testing.T) {
	dir := srcTree(t)
	cursor := filepath.Join(t.TempDir(), "c.json")
	mustRun(t, "snapshot", dir, "-o", cursor)
	out := filepath.Join(t.TempDir(), "b.spk")
	code, _, errOut := run(t, "pack", dir, "--since", cursor, "-o", out)
	if code != 1 || !strings.Contains(errOut, "nothing changed") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Fatal("no bundle file may be written when nothing changed")
	}
	// --allow-empty turns the refusal into a valid zero-payload bundle.
	mustRun(t, "pack", dir, "--since", cursor, "-o", out, "--allow-empty")
	mustRun(t, "verify", out)
}

func TestFullRoundTripThroughCLI(t *testing.T) {
	// The complete courier loop, exactly as the README tells it.
	src := srcTree(t)
	dst := t.TempDir()
	tmp := t.TempDir()
	full := filepath.Join(tmp, "full.spk")
	base := filepath.Join(tmp, "base.cursor")

	mustRun(t, "pack", src, "--full", "-o", full, "--cursor-out", base)
	mustRun(t, "verify", full)
	mustRun(t, "apply", full, dst)

	write(t, src, "notes/day2.md", "day two\n")
	inc := filepath.Join(tmp, "day2.spk")
	out := mustRun(t, "pack", src, "--since", base, "-o", inc)
	if !strings.Contains(out, "packed 1 change(s)") {
		t.Fatalf("pack summary wrong: %q", out)
	}
	applyOut := mustRun(t, "apply", inc, dst)
	if !strings.Contains(applyOut, "verified: tree matches cursor") {
		t.Fatalf("apply must verify: %q", applyOut)
	}
	data, err := os.ReadFile(filepath.Join(dst, "notes/day2.md"))
	if err != nil || string(data) != "day two\n" {
		t.Fatalf("mirror wrong: %q %v", data, err)
	}
}

func TestInspectListsChanges(t *testing.T) {
	src := srcTree(t)
	full := filepath.Join(t.TempDir(), "full.spk")
	mustRun(t, "pack", src, "--full", "-o", full)
	out := mustRun(t, "inspect", full)
	for _, want := range []string{"format 1", "2 added, 0 modified, 0 deleted", "A  notes/day1.md", "A  readings.csv"} {
		if !strings.Contains(out, want) {
			t.Errorf("inspect missing %q:\n%s", want, out)
		}
	}
}

func TestVerifyFailsOnTamperedBundle(t *testing.T) {
	src := srcTree(t)
	full := filepath.Join(t.TempDir(), "full.spk")
	mustRun(t, "pack", src, "--full", "-o", full)
	// Truncate the bundle mid-stream — exactly what a half-finished copy
	// to a USB stick that was yanked too early really produces.
	data, err := os.ReadFile(full)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, data[:len(data)-16], 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut := run(t, "verify", full)
	if code == 0 {
		t.Fatalf("tampered bundle verified clean:\n%s", errOut)
	}
}

func TestApplyOutOfOrderExitsOne(t *testing.T) {
	src := srcTree(t)
	dst := t.TempDir()
	tmp := t.TempDir()
	base := filepath.Join(tmp, "base.cursor")
	mustRun(t, "snapshot", src, "-o", base)
	write(t, src, "later.txt", "later\n")
	inc := filepath.Join(tmp, "inc.spk")
	mustRun(t, "pack", src, "--since", base, "-o", inc)
	code, _, errOut := run(t, "apply", inc, dst) // dst never saw the full bundle
	if code != 1 || !strings.Contains(errOut, "does not chain") {
		t.Fatalf("exit %d, stderr %q", code, errOut)
	}
}

func TestApplyDryRunWithConflictExitsOne(t *testing.T) {
	src := srcTree(t)
	dst := t.TempDir()
	tmp := t.TempDir()
	full := filepath.Join(tmp, "full.spk")
	base := filepath.Join(tmp, "base.cursor")
	mustRun(t, "pack", src, "--full", "-o", full, "--cursor-out", base)
	mustRun(t, "apply", full, dst)
	write(t, dst, "readings.csv", "locally edited\n")
	write(t, src, "readings.csv", "id,temp\n1,20.5\n2,21.0\n")
	inc := filepath.Join(tmp, "inc.spk")
	mustRun(t, "pack", src, "--since", base, "-o", inc)
	code, out, _ := run(t, "apply", inc, dst, "--dry-run")
	if code != 1 || !strings.Contains(out, "conflict readings.csv") {
		t.Fatalf("exit %d, out %q", code, out)
	}
	if data, _ := os.ReadFile(filepath.Join(dst, "readings.csv")); string(data) != "locally edited\n" {
		t.Fatal("dry run modified the tree")
	}
}

func TestCursorPrintsFullID(t *testing.T) {
	src := srcTree(t)
	dst := t.TempDir()
	full := filepath.Join(t.TempDir(), "full.spk")
	mustRun(t, "pack", src, "--full", "-o", full)
	mustRun(t, "apply", full, dst)
	out := mustRun(t, "cursor", dst)
	id := strings.TrimSpace(out)
	if len(id) != 64 {
		t.Fatalf("cursor should print the full 64-char ID, got %q", id)
	}
}

func TestCursorExportFeedsNextPack(t *testing.T) {
	// The round trip that closes the loop: the destination exports its
	// cursor, the source packs against it, and a clean tree packs nothing.
	src := srcTree(t)
	dst := t.TempDir()
	tmp := t.TempDir()
	full := filepath.Join(tmp, "full.spk")
	mustRun(t, "pack", src, "--full", "-o", full)
	mustRun(t, "apply", full, dst)
	back := filepath.Join(tmp, "back.cursor")
	mustRun(t, "cursor", dst, "-o", back)
	code, _, errOut := run(t, "pack", src, "--since", back, "-o", filepath.Join(tmp, "no.spk"))
	if code != 1 || !strings.Contains(errOut, "nothing changed") {
		t.Fatalf("cursor round trip broken: exit %d, %q", code, errOut)
	}
}

func TestIgnoreFileHonoredEndToEnd(t *testing.T) {
	src := srcTree(t)
	write(t, src, ".sneakpackignore", "*.tmp\n")
	write(t, src, "scratch.tmp", "junk")
	dst := t.TempDir()
	full := filepath.Join(t.TempDir(), "full.spk")
	mustRun(t, "pack", src, "--full", "-o", full)
	mustRun(t, "apply", full, dst)
	if _, err := os.Stat(filepath.Join(dst, "scratch.tmp")); !os.IsNotExist(err) {
		t.Fatal("ignored file leaked into the bundle")
	}
	if _, err := os.Stat(filepath.Join(dst, ".sneakpackignore")); err != nil {
		t.Fatal("the ignore file itself must be synced, like .gitignore in git")
	}
}

func TestFlagsAfterPositionalsAccepted(t *testing.T) {
	dir := srcTree(t)
	out := filepath.Join(t.TempDir(), "b.spk")
	// Both orders must work; people type both.
	mustRun(t, "pack", dir, "--full", "-o", out)
	mustRun(t, "pack", "--full", "-o", out, dir)
}

func TestApplyNoVerifyReportsSkip(t *testing.T) {
	src := srcTree(t)
	dst := t.TempDir()
	full := filepath.Join(t.TempDir(), "full.spk")
	mustRun(t, "pack", src, "--full", "-o", full)
	out := mustRun(t, "apply", full, dst, "--no-verify")
	if !strings.Contains(out, "verification skipped") {
		t.Fatalf("skip must be stated, got %q", out)
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		512:     "512 B",
		1024:    "1.0 KiB",
		1536:    "1.5 KiB",
		1048576: "1.0 MiB",
	}
	for n, want := range cases {
		if got := humanSize(n); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", n, got, want)
		}
	}
}
