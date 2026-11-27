package tiles

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
)

// A custom tile's data cites the review with citations: any object in the
// data that has the shape of the tile schema's $defs/citation, a review
// anchor or a reference to a risk, evidence item, change or tile of the
// document. The data is walked in a fixed order (object members by name,
// array items in order), and a citation's own members are not searched.

// citation is a citation found in a custom tile's data.
type citation struct {
	at      *dataPath
	kind    string
	id      string
	path    string
	session string
	lines   []float64
}

// citationKinds are the type values a citation may have: the review anchor
// types and the item reference types.
var citationKinds = map[string]string{
	"hunk": "hunk", "file": "file", "commit": "commit", "pr": "pull request", "pr_body": "pull request body",
	"pr_comment": "pull request comment", "review_thread": "review thread", "prompt": "prompt",
	"session_event": "session event", "check": "check", "bot_report": "bot report", "issue": "issue",
	"spec": "spec", "external_url": "link", "attachment": "attachment",
	"risk": "risk", "evidence": "evidence item", "change": "change", "tile": "tile",
}

// findCitations returns the citations in decoded data, at most
// MaxCitations+1 of them. It fails only when the budget runs out.
func findCitations(value any, v *validation) ([]citation, error) {
	shared, err := reserved()
	if err != nil {
		return nil, err
	}
	var found []citation
	var walk func(value any, at *dataPath) error
	walk = func(value any, at *dataPath) error {
		if len(found) > MaxCitations {
			return nil
		}
		if err := v.spend(1); err != nil {
			return err
		}
		switch value := value.(type) {
		case []any:
			for i, item := range value {
				if err := walk(item, &dataPath{parent: at, index: i}); err != nil {
					return err
				}
			}
		case map[string]any:
			if kind, ok := value["type"].(string); ok && shared.citations[kind] != nil {
				switch err := v.validate(shared.citations[kind], value, at); err {
				case nil:
					found = append(found, newCitation(kind, value, at))
					return nil
				case errBudget:
					return err
				}
			}
			names := make([]string, 0, len(value))
			for name := range value {
				names = append(names, name)
			}
			sort.Strings(names)
			for _, name := range names {
				if err := walk(value[name], &dataPath{parent: at, name: name}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(value, nil); err != nil {
		return nil, err
	}
	return found, nil
}

func newCitation(kind string, value map[string]any, at *dataPath) citation {
	c := citation{at: at, kind: kind}
	c.id, _ = value["id"].(string)
	c.session, _ = value["session"].(string)
	c.path, _ = value["path"].(string)
	if lines, ok := value["lines"].([]any); ok {
		for _, line := range lines {
			if f, ok := line.(float64); ok {
				c.lines = append(c.lines, f)
			}
		}
	}
	return c
}

// customRefs is what a citation may resolve to in a document.
type customRefs struct {
	checks, threads, risks, evidence, changes, tiles map[string]bool
	// hunks maps a hunk ID to its right side's lines, first and last; a
	// hunk with an empty right side has none, and one the index gives no
	// range for has [0, 0].
	hunks map[string][]int
	// files are the paths of the index's hunks, old paths included.
	files map[string]bool
	// sessions maps a session ID to whether it is restricted: an ID the
	// index lists twice is restricted when either entry is.
	sessions map[string]bool
}

func newCustomRefs(doc *Document) *customRefs {
	refs := &customRefs{
		hunks: map[string][]int{}, files: map[string]bool{}, checks: map[string]bool{}, threads: map[string]bool{},
		risks: map[string]bool{}, evidence: map[string]bool{}, changes: map[string]bool{},
		tiles: map[string]bool{}, sessions: map[string]bool{},
	}
	type entry struct {
		ID         string `json:"id"`
		Restricted bool   `json:"restricted"`
	}
	var index struct {
		Hunks []struct {
			ID       string `json:"id"`
			Path     string `json:"path"`
			OldPath  string `json:"old_path"`
			NewRange []int  `json:"new_range"`
		} `json:"hunks"`
		Checks   []entry `json:"checks"`
		Threads  []entry `json:"threads"`
		Sessions []entry `json:"sessions"`
	}
	_ = json.Unmarshal(doc.Index, &index)
	for _, list := range []struct {
		entries []entry
		set     map[string]bool
	}{{index.Checks, refs.checks}, {index.Threads, refs.threads}} {
		for _, e := range list.entries {
			list.set[e.ID] = true
		}
	}
	for _, hunk := range index.Hunks {
		switch {
		case len(hunk.NewRange) != 2:
			refs.hunks[hunk.ID] = []int{0, 0}
		case hunk.NewRange[1] > 0:
			refs.hunks[hunk.ID] = []int{hunk.NewRange[0], hunk.NewRange[0] + hunk.NewRange[1] - 1}
		default:
			refs.hunks[hunk.ID] = nil
		}
		refs.files[hunk.Path] = true
		if hunk.OldPath != "" {
			refs.files[hunk.OldPath] = true
		}
	}
	for _, session := range index.Sessions {
		refs.sessions[session.ID] = refs.sessions[session.ID] || session.Restricted
	}
	for _, risk := range doc.Risks {
		refs.risks[risk.ID] = true
	}
	for _, item := range doc.Evidence {
		refs.evidence[item.ID] = true
	}
	for _, change := range doc.Changes {
		refs.changes[change.ID] = true
	}
	for _, tile := range doc.Tiles {
		refs.tiles[tile.ID] = true
	}
	return refs
}

// maxCitedLine bounds a cited line where the index gives no hunk range.
const maxCitedLine = 10_000_000

// maxCitationProblems bounds the unresolved citations a tile reports.
const maxCitationProblems = 5

// citationProblems checks that every citation resolves against the
// document and returns the cited hunks, sorted and without repeats. The
// document decides hunks (and their lines), files (the paths of its hunks),
// checks, review threads, sessions, risks, evidence, changes and tiles. It
// lists no commits, and the other kinds carry what they name (a URL, a
// number, a key), so the page resolves those from the document it has
// loaded, as best it can.
func citationProblems(citations []citation, refs *customRefs) ([]string, []string) {
	var problems, hunks []string
	unresolved := 0
	report := func(c citation, format string, args ...any) {
		unresolved++
		if unresolved <= maxCitationProblems {
			problems = append(problems, fmt.Sprintf("the citation at %s ", c.at)+fmt.Sprintf(format, args...))
		}
	}
	missing := func(c citation) {
		label := citationKinds[c.kind]
		article := "a"
		if strings.ContainsRune("aeiou", rune(label[0])) {
			article = "an"
		}
		report(c, "names %s %s this review does not have; cite one from the review", article, label)
	}
	for _, c := range citations {
		switch c.kind {
		case "hunk":
			lines, ok := refs.hunks[c.id]
			switch {
			case !ok:
				missing(c)
			case len(c.lines) == 2 && c.lines[0] > c.lines[1]:
				report(c, "has lines that end before they start; write [first, last]")
			case len(c.lines) == 2 && c.lines[1] > maxCitedLine:
				report(c, "has lines past any file's; cite lines of the hunk")
			case len(c.lines) == 2 && lines == nil:
				report(c, "has lines, but the hunk adds no lines to cite; cite the hunk without lines")
			case len(c.lines) == 2 && lines[1] > 0 && (c.lines[0] < float64(lines[0]) || c.lines[1] > float64(lines[1])):
				report(c, "has lines outside the hunk, which has lines %d to %d", lines[0], lines[1])
			default:
				hunks = append(hunks, c.id)
			}
		case "file":
			if !refs.files[c.path] {
				report(c, "names a file this review does not change; cite a file from the review")
			}
		case "check":
			if !refs.checks[c.id] {
				missing(c)
			}
		case "review_thread":
			if !refs.threads[c.id] {
				missing(c)
			}
		case "prompt", "session_event":
			restricted, ok := refs.sessions[c.session]
			switch {
			case !ok:
				report(c, "names a session this review does not have; cite one from the review")
			case restricted:
				report(c, "names a restricted session, which nothing may cite")
			}
		case "risk":
			if !refs.risks[c.id] {
				missing(c)
			}
		case "evidence":
			if !refs.evidence[c.id] {
				missing(c)
			}
		case "change":
			if !refs.changes[c.id] {
				missing(c)
			}
		case "tile":
			if !refs.tiles[c.id] {
				missing(c)
			}
		}
	}
	if unresolved > maxCitationProblems {
		problems = append(problems, fmt.Sprintf("and %d more citations do not resolve", unresolved-maxCitationProblems))
	}
	slices.Sort(hunks)
	return problems, slices.Compact(hunks)
}
