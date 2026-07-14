// Package cli implements the sneakpack command-line interface.
//
// The entry point is Run, which takes argv and explicit streams and
// returns a process exit code. Keeping the CLI a pure function of its
// inputs (no os.Exit, no global state) is what lets the integration tests
// drive every subcommand in-process, deterministically, with no PATH or
// working-directory coupling.
//
// Exit codes:
//
//	0  success — and for status, "tree matches the cursor"
//	1  a real difference — changes pending (status), verification failed,
//	   chain continuity broken, or local conflicts blocking an apply
//	2  usage or I/O error
package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/JaydenCJ/sneakpack/internal/apply"
	"github.com/JaydenCJ/sneakpack/internal/bundle"
	"github.com/JaydenCJ/sneakpack/internal/diff"
	"github.com/JaydenCJ/sneakpack/internal/ignore"
	"github.com/JaydenCJ/sneakpack/internal/snapshot"
	"github.com/JaydenCJ/sneakpack/internal/version"
)

const usageText = `sneakpack — offline sync via bundle files

Usage:
  sneakpack snapshot <dir> [-o cursor.json]        write a cursor for a tree
  sneakpack status   <dir> [--since cursor.json]   list changes since a cursor
  sneakpack pack     <dir> -o bundle.spk (--since cursor.json | --full)
                     [--cursor-out next.json] [--allow-empty]
  sneakpack inspect  <bundle.spk>                  print a bundle's contents
  sneakpack verify   <bundle.spk>                  hash-check a bundle offline
  sneakpack apply    <bundle.spk> <dir> [--dry-run] [--force] [--no-verify]
  sneakpack cursor   <dir> [-o cursor.json]        export a destination's cursor
  sneakpack --version

Exit codes: 0 ok/clean, 1 differences or verification failure, 2 usage/I-O error.
`

// Run executes argv and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}
	switch args[0] {
	case "--version", "-V", "version":
		fmt.Fprintf(stdout, "sneakpack %s\n", version.Version)
		return 0
	case "--help", "-h", "help":
		fmt.Fprint(stdout, usageText)
		return 0
	case "snapshot":
		return cmdSnapshot(args[1:], stdout, stderr)
	case "status":
		return cmdStatus(args[1:], stdout, stderr)
	case "pack":
		return cmdPack(args[1:], stdout, stderr)
	case "inspect":
		return cmdInspect(args[1:], stdout, stderr)
	case "verify":
		return cmdVerify(args[1:], stdout, stderr)
	case "apply":
		return cmdApply(args[1:], stdout, stderr)
	case "cursor":
		return cmdCursor(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "sneakpack: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

// fail prints an error and returns the usage/I-O exit code.
func fail(stderr io.Writer, err error) int {
	fmt.Fprintf(stderr, "sneakpack: %v\n", err)
	return 2
}

// newFlagSet builds a silent FlagSet whose errors we render ourselves.
func newFlagSet(name string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	return fs
}

// parseMixed parses args allowing flags before or after positional
// arguments ("pack dir --full" and "pack --full dir" both work — people
// type both, and the stdlib flag package only accepts the latter). It
// returns the positional arguments in order.
func parseMixed(fs *flag.FlagSet, args []string) ([]string, error) {
	boolFlag := map[string]bool{}
	fs.VisitAll(func(f *flag.Flag) {
		if bv, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bv.IsBoolFlag() {
			boolFlag[f.Name] = true
		}
	})
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if len(a) > 1 && strings.HasPrefix(a, "-") {
			flags = append(flags, a)
			name := strings.TrimLeft(a, "-")
			if eq := strings.Index(name, "="); eq >= 0 {
				continue // value inline
			}
			if !boolFlag[name] && i+1 < len(args) {
				i++
				flags = append(flags, args[i]) // value in the next token
			}
			continue
		}
		pos = append(pos, a)
	}
	if err := fs.Parse(flags); err != nil {
		return nil, err
	}
	return pos, nil
}

// takeTree walks dir honoring its .sneakpackignore and warns (to stderr)
// about skipped non-regular files.
func takeTree(dir string, stderr io.Writer) (snapshot.Snapshot, error) {
	rules, err := ignore.Load(filepath.Join(dir, ".sneakpackignore"))
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	snap, skipped, err := snapshot.Take(dir, rules)
	if err != nil {
		return snapshot.Snapshot{}, err
	}
	for _, s := range skipped {
		fmt.Fprintf(stderr, "warning: skipped non-regular file: %s\n", s)
	}
	return snap, nil
}

// resolveBase loads the base cursor for status/pack: an explicit --since
// file wins; otherwise the tree's own stored cursor (empty tree if none).
func resolveBase(dir, since string) (snapshot.Snapshot, string, error) {
	if since != "" {
		s, err := snapshot.Load(since)
		return s, since, err
	}
	s, err := apply.LoadCursor(dir)
	return s, "stored cursor", err
}

func cmdSnapshot(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("snapshot", stderr)
	out := fs.String("o", "", "write the cursor to this file instead of stdout")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(stderr, "usage: sneakpack snapshot <dir> [-o cursor.json]\n")
		return 2
	}
	dir := pos[0]
	snap, err := takeTree(dir, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	if *out == "" {
		if err := writeSnapshotTo(stdout, snap); err != nil {
			return fail(stderr, err)
		}
		return 0
	}
	if err := snapshot.Save(snap, *out); err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "snapshot ok: %d file(s), cursor %s -> %s\n", len(snap.Files), snapshot.Short(snap.ID), *out)
	return 0
}

// writeSnapshotTo streams the cursor JSON to a writer (stdout mode), in
// the same shape snapshot.Save writes to disk.
func writeSnapshotTo(w io.Writer, s snapshot.Snapshot) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}

func cmdStatus(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("status", stderr)
	since := fs.String("since", "", "cursor file to diff against (default: the tree's stored cursor)")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(stderr, "usage: sneakpack status <dir> [--since cursor.json]\n")
		return 2
	}
	dir := pos[0]
	base, baseName, err := resolveBase(dir, *since)
	if err != nil {
		return fail(stderr, err)
	}
	snap, err := takeTree(dir, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	c := diff.Compute(base, snap)
	if c.Empty() {
		fmt.Fprintf(stdout, "clean: tree matches cursor %s (%s)\n", snapshot.Short(base.ID), baseName)
		return 0
	}
	printChanges(stdout, c, true)
	fmt.Fprintf(stdout, "%d change(s) since cursor %s: %d added, %d modified, %d deleted\n",
		c.Count(), snapshot.Short(base.ID), len(c.Added), len(c.Modified), len(c.Deleted))
	return 1
}

// printChanges lists a change set in canonical A/M/D order.
func printChanges(w io.Writer, c diff.Changes, withSize bool) {
	for _, a := range c.Added {
		if withSize {
			fmt.Fprintf(w, "A  %s (%s)\n", a.Path, humanSize(a.Size))
		} else {
			fmt.Fprintf(w, "A  %s\n", a.Path)
		}
	}
	for _, m := range c.Modified {
		if withSize {
			fmt.Fprintf(w, "M  %s (%s)\n", m.Path, humanSize(m.Size))
		} else {
			fmt.Fprintf(w, "M  %s\n", m.Path)
		}
	}
	for _, d := range c.Deleted {
		fmt.Fprintf(w, "D  %s\n", d.Path)
	}
}

func cmdPack(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("pack", stderr)
	out := fs.String("o", "", "bundle file to write (required)")
	since := fs.String("since", "", "cursor file the destination is currently at")
	full := fs.Bool("full", false, "pack the whole tree (first transfer, no cursor yet)")
	cursorOut := fs.String("cursor-out", "", "also write the resulting cursor to this file")
	allowEmpty := fs.Bool("allow-empty", false, "write a bundle even when nothing changed")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 || *out == "" {
		fmt.Fprint(stderr, "usage: sneakpack pack <dir> -o bundle.spk (--since cursor.json | --full) [--cursor-out next.json] [--allow-empty]\n")
		return 2
	}
	if (*since == "") == !*full {
		fmt.Fprint(stderr, "sneakpack: pack needs exactly one of --since <cursor> or --full\n")
		return 2
	}
	dir := pos[0]
	base := snapshot.Snapshot{Format: snapshot.Format, ID: snapshot.EmptyID()}
	if *since != "" {
		var err error
		base, err = snapshot.Load(*since)
		if err != nil {
			return fail(stderr, err)
		}
	}
	target, err := takeTree(dir, stderr)
	if err != nil {
		return fail(stderr, err)
	}
	m := bundle.New(base, target)
	if m.Changes.Empty() && !*allowEmpty {
		fmt.Fprintf(stderr, "sneakpack: nothing changed since cursor %s; not writing a bundle (--allow-empty to force)\n", snapshot.Short(base.ID))
		return 1
	}
	size, err := bundle.Write(*out, m, dir)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "packed %d change(s) -> %s\n", m.Changes.Count(), *out)
	fmt.Fprintf(stdout, "  base   %s\n", snapshot.Short(m.BaseCursor))
	fmt.Fprintf(stdout, "  target %s\n", snapshot.Short(m.TargetCursor))
	fmt.Fprintf(stdout, "  %d payload file(s), %s raw, bundle %s\n",
		len(m.Changes.PayloadFiles()), humanSize(m.Changes.PayloadBytes()), humanSize(size))
	if *cursorOut != "" {
		if err := snapshot.Save(target, *cursorOut); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "  cursor -> %s\n", *cursorOut)
	}
	return 0
}

func cmdInspect(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("inspect", stderr)
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(stderr, "usage: sneakpack inspect <bundle.spk>\n")
		return 2
	}
	path := pos[0]
	m, err := bundle.ReadManifest(path)
	if err != nil {
		return fail(stderr, err)
	}
	fmt.Fprintf(stdout, "bundle %s (format %d, written by %s)\n", filepath.Base(path), m.Format, m.Tool)
	fmt.Fprintf(stdout, "  base   %s\n", snapshot.Short(m.BaseCursor))
	fmt.Fprintf(stdout, "  target %s (%d file(s) in target tree)\n", snapshot.Short(m.TargetCursor), len(m.Target.Files))
	fmt.Fprintf(stdout, "  changes: %d added, %d modified, %d deleted\n",
		len(m.Changes.Added), len(m.Changes.Modified), len(m.Changes.Deleted))
	printChanges(stdout, m.Changes, true)
	return 0
}

func cmdVerify(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("verify", stderr)
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(stderr, "usage: sneakpack verify <bundle.spk>\n")
		return 2
	}
	path := pos[0]
	m, rep, err := bundle.Verify(path)
	if err != nil {
		return fail(stderr, err)
	}
	if !rep.OK() {
		fmt.Fprintf(stdout, "verify %s: FAIL\n", filepath.Base(path))
		for _, p := range rep.Problems {
			fmt.Fprintf(stdout, "  %s\n", p)
		}
		return 1
	}
	fmt.Fprintf(stdout, "verify %s: ok\n", filepath.Base(path))
	fmt.Fprintf(stdout, "  manifest consistent, %d payload file(s) hash-checked\n", rep.DataFiles)
	fmt.Fprintf(stdout, "  base %s -> target %s\n", snapshot.Short(m.BaseCursor), snapshot.Short(m.TargetCursor))
	return 0
}

func cmdApply(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("apply", stderr)
	dryRun := fs.Bool("dry-run", false, "report the plan and conflicts, change nothing")
	force := fs.Bool("force", false, "apply despite continuity or conflict findings")
	noVerify := fs.Bool("no-verify", false, "skip the post-apply tree verification")
	pos, perr := parseMixed(fs, args)
	if perr != nil || len(pos) != 2 {
		fmt.Fprint(stderr, "usage: sneakpack apply <bundle.spk> <dir> [--dry-run] [--force] [--no-verify]\n")
		return 2
	}
	bundlePath, dir := pos[0], pos[1]
	res, err := apply.Run(bundlePath, dir, apply.Options{
		DryRun: *dryRun, Force: *force, NoVerify: *noVerify,
	})
	if err != nil {
		fmt.Fprintf(stderr, "sneakpack: %v\n", err)
		switch err.(type) {
		case *apply.ContinuityError, *apply.ConflictError, *apply.VerifyError:
			return 1
		}
		return 2
	}
	if *dryRun {
		fmt.Fprintf(stdout, "dry-run: %s -> %s\n", filepath.Base(bundlePath), dir)
		fmt.Fprintf(stdout, "  would add %d, modify %d, delete %d\n", res.Added, res.Modified, res.Deleted)
		for _, c := range res.Conflicts {
			fmt.Fprintf(stdout, "  conflict %s: %s\n", c.Path, c.Reason)
		}
		if len(res.Conflicts) > 0 {
			return 1
		}
		return 0
	}
	fmt.Fprintf(stdout, "applied %s -> %s\n", filepath.Base(bundlePath), dir)
	fmt.Fprintf(stdout, "  %d added, %d modified, %d deleted\n", res.Added, res.Modified, res.Deleted)
	for _, c := range res.Conflicts {
		fmt.Fprintf(stdout, "  overwrote conflict %s: %s\n", c.Path, c.Reason)
	}
	if res.Verified {
		fmt.Fprintf(stdout, "  verified: tree matches cursor %s\n", snapshot.Short(res.Cursor))
	} else {
		fmt.Fprintf(stdout, "  cursor now %s (post-apply verification skipped)\n", snapshot.Short(res.Cursor))
	}
	return 0
}

func cmdCursor(args []string, stdout, stderr io.Writer) int {
	fs := newFlagSet("cursor", stderr)
	out := fs.String("o", "", "export the full cursor file for the trip back to the source")
	pos, err := parseMixed(fs, args)
	if err != nil || len(pos) != 1 {
		fmt.Fprint(stderr, "usage: sneakpack cursor <dir> [-o cursor.json]\n")
		return 2
	}
	dir := pos[0]
	cur, err := apply.LoadCursor(dir)
	if err != nil {
		return fail(stderr, err)
	}
	if *out != "" {
		if err := snapshot.Save(cur, *out); err != nil {
			return fail(stderr, err)
		}
		fmt.Fprintf(stdout, "cursor %s (%d file(s)) -> %s\n", snapshot.Short(cur.ID), len(cur.Files), *out)
		return 0
	}
	fmt.Fprintf(stdout, "%s\n", cur.ID)
	return 0
}

// humanSize renders a byte count the way a human on a boat wants to read
// it: exact bytes below 1 KiB, one decimal above.
func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	for _, u := range units {
		v /= unit
		if v < unit || u == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%d B", n)
}
