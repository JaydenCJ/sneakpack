// Package ignore implements .sneakpackignore pattern matching.
//
// The syntax is a deliberately small, well-specified subset of gitignore:
//
//   - Blank lines and lines starting with '#' are skipped.
//   - A pattern containing no '/' matches the basename of any file or
//     directory at any depth ("*.log" ignores every log file in the tree).
//   - A pattern containing a '/' is anchored at the tree root and matched
//     segment by segment; a leading '/' is accepted and stripped.
//   - '**' as a whole segment matches zero or more path segments.
//   - Within a segment, '*' matches any run of non-separator characters
//     and '?' matches a single one (path.Match semantics).
//   - A trailing '/' restricts the pattern to directories, ignoring the
//     directory and everything beneath it.
//   - A leading '!' re-includes paths excluded by an earlier pattern.
//     The last matching pattern wins, exactly like gitignore.
//
// The subset is intentionally conservative: every construct above has a
// single unambiguous meaning, so a .sneakpackignore synced to another
// machine ignores exactly the same paths there.
package ignore

import (
	"bufio"
	"fmt"
	"os"
	"path"
	"strings"
)

// pattern is one parsed .sneakpackignore line.
type pattern struct {
	segments []string // split on '/', already cleaned
	negate   bool     // leading '!'
	dirOnly  bool     // trailing '/'
	anchored bool     // contained a '/' → root-anchored
}

// Rules is an ordered .sneakpackignore rule set. The zero value (or nil)
// ignores nothing.
type Rules struct {
	patterns []pattern
}

// Parse builds a rule set from .sneakpackignore content. It never fails on
// blank or comment lines; a syntactically invalid glob (e.g. an unclosed
// character class) is reported with its line number so the user can fix
// the file instead of silently syncing the wrong set of paths.
func Parse(content string) (*Rules, error) {
	r := &Rules{}
	scanner := bufio.NewScanner(strings.NewReader(content))
	lineno := 0
	for scanner.Scan() {
		lineno++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		p := pattern{}
		if strings.HasPrefix(line, "!") {
			p.negate = true
			line = line[1:]
		}
		if strings.HasSuffix(line, "/") {
			p.dirOnly = true
			line = strings.TrimSuffix(line, "/")
		}
		rooted := strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		if line == "" {
			continue
		}
		p.anchored = rooted || strings.Contains(line, "/")
		p.segments = strings.Split(line, "/")
		for _, seg := range p.segments {
			if seg == "**" {
				continue
			}
			if _, err := path.Match(seg, "probe"); err != nil {
				return nil, fmt.Errorf("ignore pattern line %d: bad glob %q", lineno, seg)
			}
		}
		r.patterns = append(r.patterns, p)
	}
	return r, nil
}

// Load reads and parses a .sneakpackignore file. A missing file yields an
// empty rule set — not having the file is the common case, not an error.
func Load(file string) (*Rules, error) {
	data, err := os.ReadFile(file)
	if os.IsNotExist(err) {
		return &Rules{}, nil
	}
	if err != nil {
		return nil, err
	}
	return Parse(string(data))
}

// Match reports whether relPath (slash-separated, relative to the tree
// root) is ignored. isDir must be true when the path is a directory so
// that dir-only patterns ("build/") apply. The last matching pattern in
// the file decides, so later "!keep" lines can carve exceptions out of
// earlier wildcard excludes.
func (r *Rules) Match(relPath string, isDir bool) bool {
	if r == nil || len(r.patterns) == 0 || relPath == "" || relPath == "." {
		return false
	}
	segs := strings.Split(relPath, "/")
	ignored := false
	for _, p := range r.patterns {
		if p.dirOnly && !isDir {
			continue
		}
		if p.matches(segs) {
			ignored = !p.negate
		}
	}
	return ignored
}

// matches reports whether the pattern matches the path split into segs.
func (p *pattern) matches(segs []string) bool {
	if !p.anchored {
		// Basename pattern: match the final segment against the single
		// pattern segment. (Directories are handled by the walker calling
		// Match on the directory itself before descending.)
		ok, _ := path.Match(p.segments[0], segs[len(segs)-1])
		return ok
	}
	return matchSegments(p.segments, segs)
}

// matchSegments matches pattern segments against path segments with '**'
// spanning zero or more segments. Both slices are short in practice, so
// the simple recursive form is clearer than a table-driven matcher.
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for skip := 0; skip <= len(segs); skip++ {
			if matchSegments(pat[1:], segs[skip:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	if ok, _ := path.Match(pat[0], segs[0]); !ok {
		return false
	}
	return matchSegments(pat[1:], segs[1:])
}

// Len returns the number of active patterns; used by the CLI to mention
// whether an ignore file participated in a snapshot.
func (r *Rules) Len() int {
	if r == nil {
		return 0
	}
	return len(r.patterns)
}
