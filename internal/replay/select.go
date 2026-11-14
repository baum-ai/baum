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
	"time"

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
		if sel.Images != nil && e.Attachments == nil || sel.Threads != nil && e.Threads == nil {
			return nil, fmt.Errorf("index entry %s has no attachments or threads count, which --with-images and --with-threads need; rebuild the index or leave the filter out", e.ID)
		}
		if sel.Images != nil && (*e.Attachments > 0) != *sel.Images ||
			sel.Threads != nil && (*e.Threads > 0) != *sel.Threads ||
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
	// Warnings say where the order rests on a guess.
	Warnings []string
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._~-]+`)

// Name is the chain's directory name: the repository with its slash written
// as ~ (which a repository name cannot hold, so no two chains share a name),
// and the pull request number.
func (c Chain) Name() string {
	return unsafeName.ReplaceAllString(strings.ReplaceAll(c.Repository, "/", "~"), "-") + "-" + strconv.Itoa(c.Number)
}

// pullFacts are the facts of a case that place it in a pull request's history.
type pullFacts struct {
	Repository string
	Number     int
	// Previous is the version number of the previous review in the bundle, 0
	// when it has none.
	Previous   int
	NoPrevious bool
	CapturedAt time.Time
	Revision   string
}

// parseCapture reads a capture time (RFC 3339, with any offset); one that does
// not parse is the zero time, which sorts first.
func parseCapture(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// facts reads them from the index entry, or from the bundle when the dataset
// has no index.
func (d *Dataset) facts(id string) (pullFacts, error) {
	for i := range d.Index {
		e := &d.Index[i]
		if e.ID == id && e.Repository != "" && e.Number > 0 {
			f := pullFacts{Repository: e.Repository, Number: e.Number, CapturedAt: parseCapture(e.CapturedAt), Revision: cmp.Or(e.Revision, e.ID), NoPrevious: e.PreviousVersion == nil}
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
	f := pullFacts{Repository: header.Repository, Number: header.PullRequest.Number, Revision: cmp.Or(header.PullRequest.HeadSHA, id), NoPrevious: header.Previous == nil}
	if header.Previous != nil {
		f.Previous = header.Previous.Version
	}
	return f, nil
}

// SelectChains groups the selection by pull request and orders each group's
// revisions. A revision comes after the ones with a lower previous version
// number in its bundle (the version of the review the app held when the case
// was captured; none counts as 0), and among equal numbers after the one
// captured earlier (capture times are parsed, so offsets count); the case id
// settles the rest.
//
// Named cases (IDs) are the revisions exactly as given. Otherwise the filters
// choose pull requests: a pull request is taken when it has two or more
// revisions and at least one of its primary cases passes every filter; all its
// revisions are then replayed, one primary case each. When the index marks
// several primary cases of a revision, the earliest captured wins, then the
// lowest id. Limit counts cases and takes whole chains; a chain that does not
// fit is left out, and when none fits the error says so.
func (d *Dataset) SelectChains(sel Selection) ([]Chain, error) {
	groups := map[string][]string{}
	add := func(id string, f pullFacts) {
		key := f.Repository + "#" + strconv.Itoa(f.Number)
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
		// The primary case of each revision.
		type pick struct {
			id string
			f  pullFacts
		}
		best := map[string]pick{}
		for _, e := range d.Index {
			if !e.Primary || e.Repository == "" || e.Number <= 0 {
				continue
			}
			f, _ := d.facts(e.ID)
			key := f.Repository + "#" + strconv.Itoa(f.Number) + "@" + f.Revision
			if old, ok := best[key]; ok && (old.f.CapturedAt.Before(f.CapturedAt) || old.f.CapturedAt.Equal(f.CapturedAt) && old.id < e.ID) {
				continue
			}
			best[key] = pick{e.ID, f}
		}
		all := map[string][]pick{}
		for _, p := range best {
			key := p.f.Repository + "#" + strconv.Itoa(p.f.Number)
			all[key] = append(all[key], p)
		}
		for _, picks := range all {
			if len(picks) >= 2 && slices.ContainsFunc(picks, func(p pick) bool { return matched[p.id] }) {
				for _, p := range picks {
					add(p.id, p.f)
				}
			}
		}
	}
	var chains []Chain
	for _, ids := range groups {
		byID := map[string]pullFacts{}
		for _, id := range ids {
			f, err := d.facts(id)
			if err != nil {
				return nil, err
			}
			byID[id] = f
		}
		slices.SortFunc(ids, func(a, b string) int {
			return cmp.Or(cmp.Compare(byID[a].Previous, byID[b].Previous), byID[a].CapturedAt.Compare(byID[b].CapturedAt), cmp.Compare(a, b))
		})
		f := byID[ids[0]]
		chain := Chain{Repository: f.Repository, Number: f.Number, IDs: ids}
		// A revision with no previous version number sorts first as version 0;
		// that is a guess when an earlier-captured revision has a number.
		for i, id := range ids {
			if byID[id].NoPrevious && i > 0 && slices.ContainsFunc(ids[:i], func(o string) bool { return !byID[o].NoPrevious }) {
				chain.Warnings = append(chain.Warnings, fmt.Sprintf("case %s has no previous version number but follows one that has", id))
			}
			if i > 0 && byID[id].CapturedAt.Before(byID[ids[i-1]].CapturedAt) {
				chain.Warnings = append(chain.Warnings, fmt.Sprintf("case %s was captured before the case ordered ahead of it", id))
			}
		}
		chains = append(chains, chain)
	}
	slices.SortFunc(chains, func(a, b Chain) int {
		return cmp.Or(cmp.Compare(a.Repository, b.Repository), cmp.Compare(a.Number, b.Number))
	})
	if sel.ShuffleSeed != nil {
		shuffle(chains, *sel.ShuffleSeed)
	}
	if sel.Limit > 0 && len(chains) > 0 {
		var kept []Chain
		total := 0
		for _, c := range chains {
			if total+len(c.IDs) <= sel.Limit {
				kept = append(kept, c)
				total += len(c.IDs)
			}
		}
		if len(kept) == 0 {
			shortest := len(chains[0].IDs)
			for _, c := range chains {
				shortest = min(shortest, len(c.IDs))
			}
			return nil, fmt.Errorf("--limit %d is below the %d revisions of the shortest of the %d selected chains: a chain is replayed whole, so raise --limit (it counts cases)", sel.Limit, shortest, len(chains))
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
	// ("images threads previous", each yes, no or unknown).
	Combinations map[string]int
	// Detectors counts them by what the detectors matched: schema, api, any,
	// none.
	Detectors map[string]int
	// Evidence counts the matches by kind of evidence (a case can have several);
	// TextOnly counts, per detector, the matches no path rule supports: they
	// rest on the text of changed lines alone. Blind counts the cases whose every
	// hunk lacks patch text, which the detectors can only read by path.
	Evidence map[string]int
	TextOnly map[string]int
	Blind    int
	// Matching also pass the images, threads, previous and detector filters.
	Matching int
}

var pathEvidence = map[string]bool{EvidenceSchemaPath: true, EvidenceAPISchemaFile: true, EvidenceAPIRoutesFile: true}

// textOnly reports the detector kinds a case matches by text alone.
func (f Detections) textOnly() (schema, api bool) {
	var schemaPath, apiPath bool
	for _, e := range f.Evidence {
		if pathEvidence[e] {
			schemaPath = schemaPath || strings.HasPrefix(e, "schema:")
			apiPath = apiPath || strings.HasPrefix(e, "api:")
		}
	}
	return f.Schema && !schemaPath, f.API && !apiPath
}

// Break counts the candidates of the selection. It reads every candidate's
// diff for the detectors.
func (d *Dataset) Break(sel Selection) (Breakdown, error) {
	entries, err := d.base(sel)
	if err != nil {
		return Breakdown{}, err
	}
	b := Breakdown{Candidates: len(entries), Combinations: map[string]int{}, Detectors: map[string]int{}, Evidence: map[string]int{}, TextOnly: map[string]int{}}
	answer := func(known, yes bool) string {
		switch {
		case !known:
			return "unknown"
		case yes:
			return "yes"
		}
		return "no"
	}
	for _, e := range entries {
		images := answer(e.Attachments != nil, e.Attachments != nil && *e.Attachments > 0)
		threads := answer(e.Threads != nil, e.Threads != nil && *e.Threads > 0)
		b.Combinations[strings.Join([]string{images, threads, answer(true, e.PreviousVersion != nil)}, " ")]++
		found, err := d.detections(e.ID)
		if err != nil {
			return Breakdown{}, err
		}
		for _, name := range []Detector{DetectSchema, DetectAPI, DetectAny, DetectNone} {
			if found.Matches(name) {
				b.Detectors[string(name)]++
			}
		}
		for _, kind := range found.Evidence {
			b.Evidence[kind]++
		}
		if schema, api := found.textOnly(); schema || api {
			if schema {
				b.TextOnly["schema"]++
			}
			if api {
				b.TextOnly["api"]++
			}
		}
		if found.Blind {
			b.Blind++
		}
	}
	matching, err := d.facets(entries, sel)
	b.Matching = len(matching)
	return b, err
}

// Print writes the counts: the candidates by images, threads and previous
// version, by detector and what the detectors rest on, and the cases that pass
// every filter.
func (b Breakdown) Print(w io.Writer) {
	_, _ = fmt.Fprintf(w, "%d case(s) pass the index filters; counts by images, threads, previous version:\n", b.Candidates)
	for _, key := range slices.Sorted(maps.Keys(b.Combinations)) {
		parts := strings.Fields(key)
		_, _ = fmt.Fprintf(w, "  images %s, threads %s, previous %s: %d\n", parts[0], parts[1], parts[2], b.Combinations[key])
	}
	_, _ = fmt.Fprintf(w, "by detector (heuristics, not ground truth): schema %d, api %d, either %d, neither %d\n", b.Detectors["schema"], b.Detectors["api"], b.Detectors["any"], b.Detectors["none"])
	_, _ = fmt.Fprintf(w, "  matches resting on text alone, no path rule: schema %d, api %d; cases whose every hunk lacks patch text: %d\n", b.TextOnly["schema"], b.TextOnly["api"], b.Blind)
	_, _ = fmt.Fprintf(w, "  by evidence: %s\n", countsText(b.Evidence))
	_, _ = fmt.Fprintf(w, "%d case(s) also pass the images, threads, previous and detector filters\n", b.Matching)
}

// DetectorReport lists, for the candidates of the selection, the case ids by
// kind of evidence (and "schema-text-only", "api-text-only" and "blind"), so a
// person can label a sample of each. Ids only.
func (d *Dataset) DetectorReport(sel Selection) (map[string][]string, error) {
	entries, err := d.base(sel)
	if err != nil {
		return nil, err
	}
	report := map[string][]string{}
	for _, e := range entries {
		found, err := d.detections(e.ID)
		if err != nil {
			return nil, err
		}
		for _, kind := range found.Evidence {
			report[kind] = append(report[kind], e.ID)
		}
		schema, api := found.textOnly()
		if schema {
			report["schema-text-only"] = append(report["schema-text-only"], e.ID)
		}
		if api {
			report["api-text-only"] = append(report["api-text-only"], e.ID)
		}
		if found.Blind {
			report["blind"] = append(report["blind"], e.ID)
		}
		if !found.Schema && !found.API {
			report["none"] = append(report["none"], e.ID)
		}
	}
	return report, nil
}

// WriteDetectorReport writes a DetectorReport as one file of ids per kind into dir.
func WriteDetectorReport(dir string, report map[string][]string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	for kind, ids := range report {
		name := strings.ReplaceAll(kind, ":", "-") + ".txt"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strings.Join(ids, "\n")+"\n"), 0o600); err != nil {
			return err
		}
	}
	return nil
}
