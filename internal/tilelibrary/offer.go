package tilelibrary

import (
	"cmp"
	"context"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/baum-ai/baum/tiles"
	"github.com/baum-ai/baum/tiles/library"
)

// MaxOffered is how many saved types a review's outline is offered beside
// the built-in ones, pinned types aside.
const MaxOffered = 6

// Offering is what Offer chooses for: one review of a pull request.
type Offering struct {
	// Repository is the pull request's repository, owner/name.
	Repository string
	// DiffPaths are the paths the diff changes, old paths included.
	DiffPaths []string
	// Pinned are the saved types pinned to the repository
	// (repos.TileSettings.Types): they are filled whatever the outline
	// answers.
	Pinned []string
	// Taken are the library types the run offers already: the built-in
	// ones and those the person added to the pull request's review. They
	// count against tiles.MaxLibraryTiles and are never offered twice.
	Taken []string
}

// Offer chooses the saved types a review fills or offers, from the types
// in the library (List):
//
//   - always: the types pinned to the repository, in pin order;
//   - offered: the other types whose paths are empty or match a path of
//     the diff, used in this repository most recently first, then used
//     anywhere most recently, then the newest, at most MaxOffered.
//
// A deleted type, or one "Not checked yet" (imported without a render
// check), is neither offered nor filled until it is checked. Together with
// Taken, the two stay within tiles.MaxLibraryTiles: pins first, then the
// offered types.
func Offer(types []Type, o Offering) (always, offered []library.Entry) {
	room := tiles.MaxLibraryTiles - len(o.Taken)
	usable := map[string]Type{}
	for _, t := range types {
		if t.Deleted == "" && !t.Unchecked && !slices.Contains(o.Taken, t.Name) {
			usable[t.Name] = t
		}
	}
	chosen := map[string]bool{}
	for _, name := range o.Pinned {
		t, ok := usable[name]
		if !ok || chosen[name] || room <= 0 {
			continue
		}
		always = append(always, t.Entry)
		chosen[name] = true
		room--
	}
	var eligible []Type
	for _, t := range types {
		if _, ok := usable[t.Name]; ok && !chosen[t.Name] && Applies(t.Paths, o.DiffPaths) {
			eligible = append(eligible, t)
		}
	}
	slices.SortStableFunc(eligible, func(a, b Type) int {
		return cmp.Or(
			// Used in this repository, most recently first.
			stamp(b.UsedIn[o.Repository]).Compare(stamp(a.UsedIn[o.Repository])),
			stamp(b.UsedAt).Compare(stamp(a.UsedAt)),
			stamp(b.Created).Compare(stamp(a.Created)),
			cmp.Compare(a.Name, b.Name),
		)
	})
	for _, t := range eligible {
		if len(offered) == MaxOffered || room <= 0 {
			break
		}
		offered = append(offered, t.Entry)
		room--
	}
	return always, offered
}

// stamp is a stored time; one that does not parse, or none, is the zero
// time, which sorts last.
func stamp(text string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, text)
	return t
}

// Offer reads the library and chooses from it (Offer). A library that
// cannot be read offers nothing: the review goes on with the built-in types.
func (s *Store) Offer(ctx context.Context, o Offering) (always, offered []library.Entry, err error) {
	types, err := s.List(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	always, offered = Offer(types, o)
	return always, offered, nil
}

// Applies reports whether a type with these path globs is for a diff that
// changes these paths: a type with no globs is for every pull request, and
// one with globs for a diff that changes a path one of them matches
// (MatchPath).
func Applies(globs, paths []string) bool {
	if len(globs) == 0 {
		return true
	}
	for _, g := range globs {
		for _, p := range paths {
			if MatchPath(g, p) {
				return true
			}
		}
	}
	return false
}

// maxBraceAlternatives bounds what a glob's braces expand to.
const maxBraceAlternatives = 64

// MatchPath matches a repository path against a type's glob, as doublestar
// does: segments are separated by "/", each a path.Match pattern, "**"
// matches any number of directories, and {a,b} matches either alternative.
// A glob with no "/" also matches a file of that name in any directory, so
// "*.sql" is every SQL file, as a person writing it means.
func MatchPath(glob, name string) bool {
	glob, name = strings.TrimPrefix(glob, "/"), strings.TrimPrefix(name, "/")
	for _, g := range expandBraces(glob, maxBraceAlternatives) {
		if !strings.Contains(g, "/") {
			g = "**/" + g
		}
		if matchSegments(strings.Split(g, "/"), strings.Split(name, "/")) {
			return true
		}
	}
	return false
}

// expandBraces expands the first {a,b,...} of a glob, recursively, into at
// most limit alternatives. A brace that does not close is literal.
func expandBraces(glob string, limit int) []string {
	open := strings.IndexByte(glob, '{')
	if open < 0 {
		return []string{glob}
	}
	depth, end := 0, -1
	var commas []int
	for i := open; i < len(glob) && end < 0; i++ {
		switch glob[i] {
		case '{':
			depth++
		case '}':
			if depth--; depth == 0 {
				end = i
			}
		case ',':
			if depth == 1 {
				commas = append(commas, i)
			}
		}
	}
	if end < 0 {
		return []string{glob}
	}
	var parts []string
	start := open + 1
	for _, c := range commas {
		parts = append(parts, glob[start:c])
		start = c + 1
	}
	parts = append(parts, glob[start:end])
	var out []string
	for _, part := range parts {
		for _, rest := range expandBraces(glob[:open]+part+glob[end+1:], limit) {
			if len(out) == limit {
				return out
			}
			out = append(out, rest)
		}
	}
	return out
}

// matchSegments decides a match by dynamic programming over the pattern's
// and the path's segments, so its time is their product whatever the
// pattern: reach[j] says the segments so far match the path's first j.
func matchSegments(pattern, name []string) bool {
	reach := make([]bool, len(name)+1)
	reach[0] = true
	next := make([]bool, len(name)+1)
	for _, segment := range pattern {
		clear(next)
		if segment == "**" {
			seen := false
			for j := range reach {
				seen = seen || reach[j]
				next[j] = seen
			}
		} else {
			for j := range name {
				if reach[j] {
					next[j+1], _ = path.Match(segment, name[j])
				}
			}
		}
		reach, next = next, reach
	}
	return reach[len(name)]
}
