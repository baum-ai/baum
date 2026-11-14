package replay

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/bundle"
)

// Selection picks cases. Without IDs it filters the dataset's index; the
// result is sorted by id, shuffled when ShuffleSeed is set, then cut to Limit.
type Selection struct {
	IDs            []string
	Split          string
	Representative bool
	Primary        bool
	// Sizes are the size buckets to keep (xs, s, m, l, xl); empty keeps all.
	Sizes []string
	// Sessions is "claude_code", "codex" or "none"; empty keeps all.
	Sessions string
	// Images, Threads and Previous keep the cases whose bundle has (true) or
	// lacks (false) images, review threads and a previous version; nil keeps
	// both.
	Images, Threads, Previous *bool
	// Detector keeps the cases a structural detector matches: "schema", "api",
	// "any" (either) or "none" (neither); empty keeps all.
	Detector    string
	Limit       int
	ShuffleSeed *uint64
}

// filtered reports whether any index filter is set.
func (s Selection) filtered() bool {
	return s.Split != "" || s.Representative || s.Primary || len(s.Sizes) > 0 || s.Sessions != "" ||
		s.Images != nil || s.Threads != nil || s.Previous != nil || s.Detector != ""
}

// base keeps the entries the index facts of the selection allow, before the
// filters that read bundles; it checks the selection's values.
func (d *Dataset) base(sel Selection) ([]Entry, error) {
	if len(d.Index) == 0 {
		return nil, errors.New("the dataset has no index/cases.jsonl: name cases with --case")
	}
	switch sel.Split {
	case "", "calibration", "held_out":
	default:
		return nil, fmt.Errorf("unknown split %q; use calibration or held_out", sel.Split)
	}
	switch sel.Sessions {
	case "", "claude_code", "codex", "none":
	default:
		return nil, fmt.Errorf("unknown --with-sessions %q; use claude_code, codex or none", sel.Sessions)
	}
	if !Detector(sel.Detector).Valid() {
		return nil, fmt.Errorf("unknown --detector %q; use schema, api, any or none", sel.Detector)
	}
	var entries []Entry
	for _, e := range d.Index {
		switch {
		case sel.Split != "" && e.Split != sel.Split,
			sel.Representative && !e.Representative,
			sel.Primary && !e.Primary,
			len(sel.Sizes) > 0 && !slices.Contains(sel.Sizes, e.Size),
			sel.Sessions == "none" && len(e.SessionVendors) > 0,
			sel.Sessions != "" && sel.Sessions != "none" && !slices.Contains(e.SessionVendors, sel.Sessions):
			continue
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// facets keeps the entries that also pass the images, threads, previous and
// detector filters.
func (d *Dataset) facets(entries []Entry, sel Selection) ([]Entry, error) {
	var kept []Entry
	for _, e := range entries {
		if sel.Images != nil && (e.Attachments > 0) != *sel.Images ||
			sel.Threads != nil && (e.Threads > 0) != *sel.Threads ||
			sel.Previous != nil && (e.PreviousVersion != nil) != *sel.Previous {
			continue
		}
		if sel.Detector != "" {
			found, err := d.detections(e.ID)
			if err != nil {
				return nil, err
			}
			if !found.Matches(Detector(sel.Detector)) {
				continue
			}
		}
		kept = append(kept, e)
	}
	return kept, nil
}

// Select returns the ids of the cases the selection names.
func (d *Dataset) Select(sel Selection) ([]string, error) {
	var ids []string
	if len(sel.IDs) > 0 {
		ids = slices.Clone(sel.IDs)
		if sel.filtered() {
			return nil, errors.New("--case and --case-file cannot be combined with index filters")
		}
	} else {
		entries, err := d.base(sel)
		if err != nil {
			return nil, err
		}
		if entries, err = d.facets(entries, sel); err != nil {
			return nil, err
		}
		for _, e := range entries {
			ids = append(ids, e.ID)
		}
	}
	slices.Sort(ids)
	ids = slices.Compact(ids)
	if sel.ShuffleSeed != nil {
		shuffle(ids, *sel.ShuffleSeed)
	}
	if sel.Limit > 0 && len(ids) > sel.Limit {
		ids = ids[:sel.Limit]
	}
	return ids, nil
}

func shuffle[T any](items []T, seed uint64) {
	rng := rand.New(rand.NewPCG(seed, 0))
	rng.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
}

// detections runs the detectors on a case's diff, once.
func (d *Dataset) detections(id string) (Detections, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if found, ok := d.detected[id]; ok {
		return found, nil
	}
	var header struct {
		Diff []bundle.Hunk `json:"diff"`
	}
	raw, err := os.ReadFile(filepath.Join(d.Dir, "cases", id, "bundle.json"))
	if err == nil {
		err = json.Unmarshal(raw, &header)
	}
	if err != nil {
		return Detections{}, fmt.Errorf("case %s: reading the diff: %w", id, err)
	}
	if d.detected == nil {
		d.detected = map[string]Detections{}
	}
	d.detected[id] = Detect(header.Diff)
	return d.detected[id], nil
}

// Chain is the captured revisions of one pull request, in the order a chain
// run replays them.
type Chain struct {
	Repository string
	Number     int
	IDs        []string
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// Name is the chain's directory name.
func (c Chain) Name() string {
	return unsafeName.ReplaceAllString(c.Repository, "-") + "-" + strconv.Itoa(c.Number)
}

// pullFacts are the facts of a case that place it in a pull request's history.
type pullFacts struct {
	Repository string
	Number     int
	// Previous is the version number of the previous review in the bundle, 0
	// when it has none.
	Previous   int
	CapturedAt string
	Revision   string
}

// facts reads them from the index entry, or from the bundle when the dataset
// has no index.
func (d *Dataset) facts(id string) (pullFacts, error) {
	for i := range d.Index {
		e := &d.Index[i]
		if e.ID == id && e.Repository != "" && e.Number > 0 {
			f := pullFacts{Repository: e.Repository, Number: e.Number, CapturedAt: e.CapturedAt, Revision: cmp.Or(e.Revision, e.ID)}
			if e.PreviousVersion != nil {
				f.Previous = *e.PreviousVersion
			}
			return f, nil
		}
	}
	var header struct {
		Repository  string `json:"repository"`
		PullRequest struct {
			Number  int    `json:"number"`
			HeadSHA string `json:"head_sha"`
		} `json:"pull_request"`
		Previous *struct {
			Version int `json:"version"`
		} `json:"previous"`
	}
	raw, err := os.ReadFile(filepath.Join(d.Dir, "cases", id, "bundle.json"))
	if err == nil {
		err = json.Unmarshal(raw, &header)
	}
	if err != nil {
		return pullFacts{}, fmt.Errorf("case %s: reading the pull request: %w", id, err)
	}
	f := pullFacts{Repository: header.Repository, Number: header.PullRequest.Number, Revision: cmp.Or(header.PullRequest.HeadSHA, id)}
	if header.Previous != nil {
		f.Previous = header.Previous.Version
	}
	return f, nil
}

// SelectChains groups the selection by pull request and orders each group's
// revisions. A revision comes after the ones with a lower previous version
// number in its bundle (the version of the review the app had when the case
// was captured), and among equal numbers after the one captured earlier; the
// case id settles the rest.
//
// Named cases (IDs) are the revisions exactly as given. Otherwise the filters
// choose pull requests: a pull request is taken when it has two or more
// primary cases, one per revision, and at least one of them passes every
// filter; all its primary cases are then replayed. Limit counts cases and
// takes whole chains, skipping one that no longer fits.
func (d *Dataset) SelectChains(sel Selection) ([]Chain, error) {
	groups := map[string][]string{}
	var order []string
	add := func(id string, f pullFacts) {
		key := f.Repository + "#" + strconv.Itoa(f.Number)
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], id)
	}
	if len(sel.IDs) > 0 {
		if sel.filtered() {
			return nil, errors.New("--case and --case-file cannot be combined with index filters")
		}
		for _, id := range slices.Compact(slices.Sorted(slices.Values(sel.IDs))) {
			f, err := d.facts(id)
			if err != nil {
				return nil, err
			}
			add(id, f)
		}
	} else {
		sel.Primary = true
		entries, err := d.base(sel)
		if err != nil {
			return nil, err
		}
		if entries, err = d.facets(entries, sel); err != nil {
			return nil, err
		}
		matched := map[string]bool{}
		for _, e := range entries {
			matched[e.ID] = true
		}
		// The pull requests' primary cases, one per revision.
		all := map[string][]string{}
		seen := map[string]bool{}
		for _, e := range d.Index {
			if !e.Primary || e.Repository == "" || e.Number <= 0 {
				continue
			}
			f, _ := d.facts(e.ID)
			key := f.Repository + "#" + strconv.Itoa(f.Number)
			if seen[key+"@"+f.Revision] {
				continue
			}
			seen[key+"@"+f.Revision] = true
			all[key] = append(all[key], e.ID)
		}
		for _, ids := range all {
			if len(ids) >= 2 && slices.ContainsFunc(ids, func(id string) bool { return matched[id] }) {
				for _, id := range ids {
					f, _ := d.facts(id)
					add(id, f)
				}
			}
		}
	}
	var chains []Chain
	for _, key := range order {
		ids := groups[key]
		byID := map[string]pullFacts{}
		for _, id := range ids {
			f, err := d.facts(id)
			if err != nil {
				return nil, err
			}
			byID[id] = f
		}
		slices.SortFunc(ids, func(a, b string) int {
			return cmp.Or(cmp.Compare(byID[a].Previous, byID[b].Previous), cmp.Compare(byID[a].CapturedAt, byID[b].CapturedAt), cmp.Compare(a, b))
		})
		f := byID[ids[0]]
		chains = append(chains, Chain{Repository: f.Repository, Number: f.Number, IDs: ids})
	}
	slices.SortFunc(chains, func(a, b Chain) int {
		return cmp.Or(cmp.Compare(a.Repository, b.Repository), cmp.Compare(a.Number, b.Number))
	})
	if sel.ShuffleSeed != nil {
		shuffle(chains, *sel.ShuffleSeed)
	}
	if sel.Limit > 0 {
		var kept []Chain
		total := 0
		for _, c := range chains {
			if total+len(c.IDs) <= sel.Limit {
				kept = append(kept, c)
				total += len(c.IDs)
			}
		}
		chains = kept
	}
	return chains, nil
}

// Breakdown counts the cases that pass the selection's index filters by each
// bundle fact, so a dry run shows what a filter combination would leave.
type Breakdown struct {
	// Candidates pass the filters that read the index only (split, size,
	// sessions, primary, representative).
	Candidates int
	// Combinations counts them by images, threads and previous version
	// ("images threads previous", each yes or no).
	Combinations map[string]int
	// Detectors counts them by what the detectors matched: schema, api, any,
	// none.
	Detectors map[string]int
	// Matching also pass the images, threads, previous and detector filters.
	Matching int
}

// Break counts the candidates of the selection. It reads every candidate's
// diff for the detectors.
func (d *Dataset) Break(sel Selection) (Breakdown, error) {
	entries, err := d.base(sel)
	if err != nil {
		return Breakdown{}, err
	}
	b := Breakdown{Candidates: len(entries), Combinations: map[string]int{}, Detectors: map[string]int{}}
	yes := func(v bool) string { return map[bool]string{true: "yes", false: "no"}[v] }
	for _, e := range entries {
		b.Combinations[strings.Join([]string{yes(e.Attachments > 0), yes(e.Threads > 0), yes(e.PreviousVersion != nil)}, " ")]++
		found, err := d.detections(e.ID)
		if err != nil {
			return Breakdown{}, err
		}
		for _, name := range []Detector{DetectSchema, DetectAPI, DetectAny, DetectNone} {
			if found.Matches(name) {
				b.Detectors[string(name)]++
			}
		}
	}
	matching, err := d.facets(entries, sel)
	b.Matching = len(matching)
	return b, err
}

// Print writes the counts: the candidates by images, threads and previous
// version, by detector, and the cases that pass every filter.
func (b Breakdown) Print(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%d case(s) pass the index filters; counts by images, threads, previous version:\n", b.Candidates)
	for _, key := range slices.Sorted(maps.Keys(b.Combinations)) {
		parts := strings.Fields(key)
		_, _ = fmt.Fprintf(w, "  images %s, threads %s, previous %s: %d\n", parts[0], parts[1], parts[2], b.Combinations[key])
	}
	_, _ = fmt.Fprintf(w, "by detector: schema %d, api %d, either %d, neither %d\n", b.Detectors["schema"], b.Detectors["api"], b.Detectors["any"], b.Detectors["none"])
	_, _ = fmt.Fprintf(w, "%d case(s) also pass the images, threads, previous and detector filters\n", b.Matching)
}
