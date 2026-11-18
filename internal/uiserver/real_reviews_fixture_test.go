package uiserver_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/baum-ai/baum/agent"
	"github.com/baum-ai/baum/internal/appdb/appdbtest"
	"github.com/baum-ai/baum/internal/assemble/assembletest"
	"github.com/baum-ai/baum/internal/automatic"
	"github.com/baum-ai/baum/internal/config"
	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/generate/generatetest"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/jobs"
	"github.com/baum-ai/baum/internal/localapp"
	"github.com/baum-ai/baum/internal/prupdate"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/reviewindex"
	"github.com/baum-ai/baum/internal/store"
	"github.com/baum-ai/baum/internal/uiserver"
	"github.com/baum-ai/baum/internal/uistate"
)

// The real-reviews fixture serves saved reviews of a private dataset in the
// local app, so a review page can be designed and tuned against real data
// (see "Real reviews" in app/README.md). Its environment:
//
//	BAUM_APP_REAL_REVIEWS  the dataset directory; the fixture is skipped without it
//	BAUM_APP_REAL_PICK     the pull requests to serve: repo#number[@version], separated by commas or white space
//	BAUM_APP_REAL_REPOS    the repositories the default picker draws from, separated by commas (default: the one with the most saved reviews)
//	BAUM_APP_REAL_COUNT    the most reviews the default picker chooses (default 14)
//
// The dataset is read only: stores and the app database are copied, and each
// repository is cloned from its mirror with shared objects.
const (
	realReviewsEnv    = "BAUM_APP_REAL_REVIEWS"
	realDefaultCount  = 14
	realFixturePort   = 4169
	realPicksFileName = "picks.json"
)

// realPick is one review the fixture serves, written to picks.json for the
// browser script. Version is the one to open; 0 is the latest.
type realPick struct {
	Repository string   `json:"repository"`
	Number     int      `json:"number"`
	Title      string   `json:"title"`
	Version    int      `json:"version"`
	Versions   []int    `json:"versions"`
	Clone      string   `json:"clone"`
	Tags       []string `json:"tags,omitempty"`

	machine string
}

func (p realPick) id() string { return fmt.Sprintf("%s#%d", p.Repository, p.Number) }

// realIndexPull is a line of the dataset's index/pulls.jsonl.
type realIndexPull struct {
	Repository     string         `json:"repository"`
	Number         int            `json:"number"`
	Split          string         `json:"split"`
	ReviewVersions map[string]int `json:"review_versions"`
}

// realIndexCase is a line of the dataset's index/cases.jsonl.
type realIndexCase struct {
	Repository     string   `json:"repository"`
	Number         int      `json:"number"`
	Representative bool     `json:"representative"`
	Size           string   `json:"size"`
	SessionVendors []string `json:"session_vendors"`
}

func readJSONLines[T any](t *testing.T, path string, keep func(T) bool) []T {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []T
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var v T
		if err := json.Unmarshal([]byte(line), &v); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if keep == nil || keep(v) {
			out = append(out, v)
		}
	}
	return out
}

func realSlug(repository string) string { return strings.ReplaceAll(repository, "/", "-") }

// realDataset reads the dataset's stores through the store package, from copies
// kept in the temporary directory, so the dataset itself is never opened for
// writing (the mount is read-only) and its files are not locked.
type realDataset struct {
	t      *testing.T
	root   string
	work   string
	stores map[string]*store.Store
	pulls  []realIndexPull
}

// allPulls reads index/pulls.jsonl, once.
func (d *realDataset) allPulls() []realIndexPull {
	if d.pulls == nil {
		d.pulls = readJSONLines[realIndexPull](d.t, filepath.Join(d.root, "index", "pulls.jsonl"), nil)
	}
	return d.pulls
}

// pullsWhere are the index's pull requests that keep returns true for.
func (d *realDataset) pullsWhere(keep func(realIndexPull) bool) []realIndexPull {
	var out []realIndexPull
	for _, p := range d.allPulls() {
		if keep(p) {
			out = append(out, p)
		}
	}
	return out
}

func (d *realDataset) store(machine, repository string) *store.Store {
	d.t.Helper()
	key := machine + "/" + repository
	if s := d.stores[key]; s != nil {
		return s
	}
	dir := filepath.Join(d.work, "stores", machine, realSlug(repository))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		d.t.Fatal(err)
	}
	source := filepath.Join(d.root, "reviews", machine, realSlug(repository)+".versions.sqlite")
	for _, suffix := range []string{"", "-wal"} {
		raw, err := os.ReadFile(source + suffix)
		if errors.Is(err, os.ErrNotExist) || (err == nil && len(raw) == 0) {
			continue
		}
		if err != nil {
			d.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "versions.sqlite"+suffix), raw, 0o600); err != nil {
			d.t.Fatal(err)
		}
	}
	s, err := store.Open("", dir, false)
	if err != nil {
		d.t.Fatal(err)
	}
	d.stores[key] = s
	return s
}

// bestMachine is the machine that kept the most versions of a pull request.
func realBestMachine(p realIndexPull) string {
	best := ""
	for machine, n := range p.ReviewVersions {
		if best == "" || n > p.ReviewVersions[best] || (n == p.ReviewVersions[best] && machine < best) {
			best = machine
		}
	}
	return best
}

// realFacts are the properties of a stored review the picker tags it with.
type realFacts struct {
	sections, unassigned, threads, images int
	risk                                  float64
	vendors                               []string
	repository                            string
	title                                 string
	head                                  string
}

func realFactsOf(document []byte) realFacts {
	var doc struct {
		Review struct {
			Repository string `json:"repository"`
			Head       string `json:"head"`
		} `json:"review"`
		Summary struct {
			Title     string `json:"title"`
			RiskScore struct {
				Score float64 `json:"score"`
			} `json:"risk_score"`
		} `json:"summary"`
		Sections   []json.RawMessage `json:"sections"`
		Unassigned []json.RawMessage `json:"unassigned_hunks"`
		Index      struct {
			Attachments []json.RawMessage `json:"attachments"`
			Threads     json.RawMessage   `json:"threads"`
			Sessions    []struct {
				Vendor string `json:"vendor"`
			} `json:"sessions"`
		} `json:"index"`
	}
	_ = json.Unmarshal(document, &doc)
	f := realFacts{sections: len(doc.Sections), unassigned: len(doc.Unassigned), images: len(doc.Index.Attachments), risk: doc.Summary.RiskScore.Score,
		repository: doc.Review.Repository, title: doc.Summary.Title, head: doc.Review.Head}
	var list []json.RawMessage
	var object map[string]json.RawMessage
	if json.Unmarshal(doc.Index.Threads, &list) == nil {
		f.threads = len(list)
	} else if json.Unmarshal(doc.Index.Threads, &object) == nil {
		f.threads = len(object)
	}
	for _, s := range doc.Index.Sessions {
		if !slices.Contains(f.vendors, s.Vendor) {
			f.vendors = append(f.vendors, s.Vendor)
		}
	}
	return f
}

// versionsOf lists the stored versions of a pull request, oldest first, and
// whether they run from 1 without a gap (the store appends only the next one).
func (d *realDataset) versionsOf(machine string, k store.Key) ([]int, bool) {
	metadata, err := d.store(machine, k.Repository).Versions(context.Background(), k)
	if err != nil || len(metadata) == 0 {
		return nil, false
	}
	var out []int
	for _, m := range slices.Backward(metadata) {
		out = append(out, m.Version)
	}
	for i, v := range out {
		if v != i+1 {
			return out, false
		}
	}
	return out, true
}

func parseRealPicks(t *testing.T, text string) []realPick {
	t.Helper()
	pattern := regexp.MustCompile(`^([^#@\s]+/[^#@\s]+)#([0-9]+)(?:@([0-9]+))?$`)
	var picks []realPick
	for _, field := range strings.FieldsFunc(text, func(r rune) bool { return r == ',' || r == ' ' || r == '\n' || r == '\t' || r == '\r' }) {
		m := pattern.FindStringSubmatch(field)
		if m == nil {
			t.Fatalf("pick %q is not repo#number[@version]", field)
		}
		n, _ := strconv.Atoi(m[2])
		v, _ := strconv.Atoi(m[3])
		picks = append(picks, realPick{Repository: m[1], Number: n, Version: v})
	}
	return picks
}

// busiestRepository is the repository with the most calibration pull requests
// that have a saved review: what the picker draws from unless told otherwise.
func (d *realDataset) busiestRepository() []string {
	counts := map[string]int{}
	for _, p := range d.pullsWhere(func(p realIndexPull) bool {
		return p.Split == "calibration" && len(p.ReviewVersions) > 0
	}) {
		counts[p.Repository]++
	}
	best := ""
	for repository, n := range counts {
		if n > counts[best] || (n == counts[best] && repository < best) {
			best = repository
		}
	}
	if best == "" {
		d.t.Fatal("the dataset has no calibration review")
	}
	return []string{best}
}

// choosePicks is the default picker: calibration pull requests of the allowed
// repositories that have a complete saved review history. In priority order it
// takes the one with the most sections, the highest risk, the most unassigned
// hunks, the most images, the most threads, one with sessions of each agent,
// one of each size bucket (xl, xs, m, s, l), one without images and one
// without threads, then spreads the rest evenly; it never returns more than
// count reviews.
func (d *realDataset) choosePicks(repositories []string, count int) []realPick {
	t := d.t
	pulls := d.pullsWhere(func(p realIndexPull) bool {
		return p.Split == "calibration" && slices.Contains(repositories, p.Repository) && len(p.ReviewVersions) > 0
	})
	cases := map[string]realIndexCase{}
	for _, c := range readJSONLines(t, filepath.Join(d.root, "index", "cases.jsonl"), func(c realIndexCase) bool { return c.Representative }) {
		cases[fmt.Sprintf("%s#%d", c.Repository, c.Number)] = c
	}
	slices.SortFunc(pulls, func(a, b realIndexPull) int {
		if a.Repository != b.Repository {
			return strings.Compare(a.Repository, b.Repository)
		}
		return a.Number - b.Number
	})
	type candidate struct {
		pick  realPick
		facts realFacts
		size  string
	}
	var candidates []candidate
	for _, p := range pulls {
		machine := realBestMachine(p)
		key := store.Key{Repository: p.Repository, Number: p.Number}
		if _, whole := d.versionsOf(machine, key); !whole {
			continue
		}
		latest, err := d.store(machine, p.Repository).Latest(context.Background(), key)
		if err != nil {
			continue
		}
		f := realFactsOf(latest.Document)
		// A review stored under an earlier name of its repository cannot be
		// stored again under the current one.
		if !strings.EqualFold(f.repository, p.Repository) {
			continue
		}
		c := cases[fmt.Sprintf("%s#%d", p.Repository, p.Number)]
		if c.Size == "" {
			continue
		}
		candidates = append(candidates, candidate{pick: realPick{Repository: p.Repository, Number: p.Number, machine: machine}, facts: f, size: c.Size})
		if len(c.SessionVendors) > 0 {
			candidates[len(candidates)-1].facts.vendors = c.SessionVendors
		}
	}
	if len(candidates) == 0 {
		t.Fatal("the dataset has no calibration review to pick")
	}
	chosen := map[int]bool{}
	tags := map[int][]string{}
	take := func(i int, tag string) {
		chosen[i] = true
		tags[i] = append(tags[i], tag)
	}
	// want picks, for a tag, the candidate that satisfies it best; one already
	// chosen is reused when it scores as well, and without a score any one
	// already chosen is. Ties go to the lowest number. Once count reviews are
	// chosen it only tags those, so the rules below are in priority order.
	want := func(tag string, ok func(candidate) bool, score func(candidate) float64) {
		best, bestChosen := -1, -1
		better := func(i, than int) bool {
			return than < 0 || (score != nil && score(candidates[i]) > score(candidates[than]))
		}
		for i, c := range candidates {
			if !ok(c) {
				continue
			}
			if better(i, best) {
				best = i
			}
			if chosen[i] && better(i, bestChosen) {
				bestChosen = i
			}
		}
		switch {
		case bestChosen >= 0 && !better(best, bestChosen):
			take(bestChosen, tag)
		case best >= 0 && len(chosen) < count:
			take(best, tag)
		case bestChosen >= 0:
			take(bestChosen, tag)
		}
	}
	want("many-sections", func(c candidate) bool { return true }, func(c candidate) float64 { return float64(c.facts.sections) })
	want("high-risk", func(c candidate) bool { return c.facts.risk > 0 }, func(c candidate) float64 { return c.facts.risk })
	want("unassigned-hunks", func(c candidate) bool { return c.facts.unassigned > 0 }, func(c candidate) float64 { return float64(c.facts.unassigned) })
	want("images", func(c candidate) bool { return c.facts.images > 0 }, func(c candidate) float64 { return float64(c.facts.images) })
	want("threads", func(c candidate) bool { return c.facts.threads > 0 }, func(c candidate) float64 { return float64(c.facts.threads) })
	for _, vendor := range []string{"claude_code", "codex"} {
		want("sessions-"+vendor, func(c candidate) bool { return slices.Contains(c.facts.vendors, vendor) }, nil)
	}
	for _, size := range []string{"xl", "xs", "m", "s", "l"} {
		want("size-"+size, func(c candidate) bool { return c.size == size }, nil)
	}
	want("no-images", func(c candidate) bool { return c.facts.images == 0 }, nil)
	want("no-threads", func(c candidate) bool { return c.facts.threads == 0 }, nil)
	// The rest is spread evenly over the numbers that remain.
	var rest []int
	for i := range candidates {
		if !chosen[i] {
			rest = append(rest, i)
		}
	}
	for need := count - len(chosen); need > 0 && len(rest) > 0; need-- {
		slot := len(rest) / (need + 1)
		take(rest[slot], "spread")
		rest = slices.Delete(rest, slot, slot+1)
	}
	var out []realPick
	for i, c := range candidates {
		if chosen[i] {
			p := c.pick
			p.Tags = tags[i]
			out = append(out, p)
		}
	}
	return out
}

// realFakeGitHub answers every request with a 404: the fixture serves facts
// from the dataset and must reach nothing.
var realFakeGitHub = browserTransport(func(r *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusNotFound, Header: http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(`{"message":"Not Found"}`)), Request: r}, nil
})

// realPullRequests builds the pull request facts discovery reports for each
// pick: the dataset's own app database rows when it has them, and the saved
// review's header otherwise. The head is the shown review's, so the page
// treats the review as current.
func realPullRequests(t *testing.T, root string, picks []realPick, heads map[string]string, titles map[string]string) map[string][]discovery.PullRequest {
	t.Helper()
	out := map[string][]discovery.PullRequest{}
	databases := map[string]*sql.DB{}
	defer func() {
		for _, db := range databases {
			_ = db.Close()
		}
	}()
	for _, p := range picks {
		db := databases[p.machine]
		if db == nil {
			// Immutable, because the dataset is mounted read-only and may
			// carry a stale shared-memory file.
			uri := (&url.URL{Scheme: "file", Path: filepath.Join(root, "state", p.machine, "app.sqlite"), RawQuery: "mode=ro&immutable=1"}).String()
			var err error
			if db, err = sql.Open("sqlite", uri); err != nil {
				t.Fatal(err)
			}
			databases[p.machine] = db
		}
		pr := discovery.PullRequest{Repository: p.Repository, Number: p.Number, HeadSHA: heads[p.id()], Title: titles[p.id()], State: "OPEN", BaseRef: "main",
			URL: fmt.Sprintf("https://github.com/%s/pull/%d", p.Repository, p.Number), Review: "unreviewed", Checks: "none", Merge: "blocked", TeamMembershipKnown: true}
		var title, state, author, headRef, baseRef string
		var draft, bot int
		var updated int64
		err := db.QueryRow(`SELECT title,state,draft,author,bot,head_ref,base_ref,updated_at FROM pull_request WHERE repository=? AND number=?`, p.Repository, p.Number).
			Scan(&title, &state, &draft, &author, &bot, &headRef, &baseRef, &updated)
		if err == nil {
			pr.Title, pr.Author, pr.AuthorBot, pr.HeadRef, pr.Draft = title, author, bot != 0, headRef, draft != 0
			pr.HeadRepository = p.Repository
			if baseRef != "" {
				pr.BaseRef = baseRef
			}
			pr.UpdatedAt = time.UnixMilli(updated).UTC()
			if state == "open" {
				pr.State = "OPEN"
			}
		}
		write := true
		pr.AuthorHasWrite, pr.ViewerCanPush = &write, &write
		out[p.Repository] = append(out[p.Repository], pr)
	}
	return out
}

// TestLocalAppRealReviews is an opt-in process used by the real-review
// screenshot script. It runs the real daemon, as TestLocalAppBrowserFixture
// does, on saved reviews copied from a private dataset (BAUM_APP_REAL_REVIEWS):
// a clone per repository that shares the dataset mirror's objects, so hunk
// excerpts are read at each version's own commits, the versions and their
// images put into the clone's review store, and pull request facts taken from
// the dataset instead of GitHub. No model, no GitHub and no network.
// BAUM_APP_STATE_DIR receives picks.json, which lists what is served.
func TestLocalAppRealReviews(t *testing.T) {
	root := os.Getenv(realReviewsEnv)
	if root == "" {
		t.Skip("real-reviews fixture process")
	}
	t.Setenv("GITHUB_TOKEN", "")
	t.Setenv("GH_TOKEN", "")
	home := appdbtest.TempDir(t)
	if err := os.Chmod(home, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	state := os.Getenv("BAUM_APP_STATE_DIR")
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dataset := &realDataset{t: t, root: root, work: filepath.Join(state, "dataset"), stores: map[string]*store.Store{}}
	if err := os.RemoveAll(dataset.work); err != nil {
		t.Fatal(err)
	}

	list := os.Getenv("BAUM_APP_REAL_PICK")
	var picks []realPick
	if strings.TrimSpace(list) != "" {
		picks = parseRealPicks(t, list)
		seen := map[string]bool{}
		picks = slices.DeleteFunc(picks, func(p realPick) bool {
			if seen[p.id()] {
				t.Logf("pick %s is listed twice; the first is kept", p.id())
				return true
			}
			seen[p.id()] = true
			return false
		})
		for i := range picks {
			var pull realIndexPull
			for _, p := range dataset.pullsWhere(func(p realIndexPull) bool { return p.Repository == picks[i].Repository && p.Number == picks[i].Number }) {
				pull = p
			}
			if picks[i].machine = realBestMachine(pull); picks[i].machine == "" {
				t.Fatalf("%s has no saved review in the dataset", picks[i].id())
			}
		}
	} else {
		repositories := dataset.busiestRepository()
		if env := os.Getenv("BAUM_APP_REAL_REPOS"); env != "" {
			repositories = strings.Split(env, ",")
		}
		count := realDefaultCount
		if n, err := strconv.Atoi(os.Getenv("BAUM_APP_REAL_COUNT")); err == nil && n > 0 {
			count = n
		}
		picks = dataset.choosePicks(repositories, count)
	}
	for _, p := range picks {
		t.Logf("pick %s %s", p.id(), strings.Join(p.Tags, " "))
	}

	registry, err := repos.Open(filepath.Join(home, "app.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = registry.Close() }()
	clones := map[string]repos.Clone{}
	stores := map[string]*store.Store{}
	heads := map[string]string{}
	titles := map[string]string{}
	for i := range picks {
		p := &picks[i]
		if _, ok := clones[p.Repository]; !ok {
			// Shared objects: the clone reads the mirror's, which hold every
			// pull request's commits, and writes nothing there. Its origin
			// names GitHub so the app matches it to the repository.
			parent := filepath.Join(state, "clones")
			if err := os.MkdirAll(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(parent, realSlug(p.Repository))
			if err := os.RemoveAll(dir); err != nil {
				t.Fatal(err)
			}
			mirror := filepath.Join(root, "repos", realSlug(p.Repository)+".git")
			assembletest.Git(t, parent, "-c", "safe.directory=*", "clone", "--quiet", "--shared", "--no-checkout", mirror, dir)
			assembletest.Git(t, dir, "remote", "set-url", "origin", "https://github.com/"+p.Repository)
			if clones[p.Repository], err = registry.Add(ctx, dir, nil); err != nil {
				t.Fatalf("registering the clone of %s: %v", p.Repository, err)
			}
			if stores[p.Repository], err = store.Open(clones[p.Repository].CommonDir, "", true); err != nil {
				t.Fatal(err)
			}
		}
		p.Clone = clones[p.Repository].ID
		key := store.Key{Repository: p.Repository, Number: p.Number}
		source := dataset.store(p.machine, p.Repository)
		versions, whole := dataset.versionsOf(p.machine, key)
		if !whole {
			t.Fatalf("%s: the saved versions are not numbered from 1 without gaps", p.id())
		}
		if p.Version > 0 && !slices.Contains(versions, p.Version) {
			t.Fatalf("%s has no version %d", p.id(), p.Version)
		}
		var previous store.Version
		for _, n := range versions {
			v, err := source.Get(ctx, key, n)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range realAttachmentIDs(v.Document) {
				a, err := source.Attachment(ctx, key, n, id)
				if err != nil {
					t.Logf("%s v%d: attachment unavailable: %v", p.id(), n, err)
					continue
				}
				v.Attachments = append(v.Attachments, a)
			}
			if err := stores[p.Repository].Put(ctx, key, previous, v); err != nil {
				t.Fatalf("%s v%d: %v", p.id(), n, err)
			}
			previous = v
			facts := realFactsOf(v.Document)
			if n == versions[len(versions)-1] {
				heads[p.id()], titles[p.id()] = facts.head, facts.title
			}
			for _, oid := range []string{v.Metadata.Head, v.Metadata.MergeBase} {
				if oid != "" {
					if out, err := gitCatFile(clones[p.Repository].Path, oid); err != nil {
						t.Logf("%s v%d: commit %.12s is missing from the mirror: %s", p.id(), n, oid, out)
					}
				}
			}
		}
		p.Versions, p.Title = versions, titles[p.id()]
	}
	raw, _ := json.MarshalIndent(picks, "", "  ")
	if err := os.WriteFile(filepath.Join(state, realPicksFileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	disc := videoDiscovery(realPullRequests(t, root, picks, heads, titles))
	cfg := config.Default()
	cfg.Backend = config.BackendClaude
	cfg.Model = "claude-sonnet-4-5"
	client := github.New(cfg.ResolveGitHubToken, github.Options{Transport: realFakeGitHub})
	// A review is never generated here: a model call fails at once.
	backend := &generatetest.Backend{Version: "fixture", RunFunc: func(context.Context, agent.Request, int) (agent.Result, error) {
		return agent.Result{}, errors.New("the real-reviews fixture makes no model calls")
	}}
	index := &reviewindex.Service{Registry: registry, Discovery: disc}
	paths := daemon.Paths{State: state, Socket: filepath.Join(state, "daemon.sock")}
	runCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	err = daemon.Run(runCtx, daemon.Options{Paths: paths, Version: "fixture", Port: realFixturePort, Start: func(appCtx context.Context, server *uiserver.Server) (daemon.App, error) {
		groups, err := reviewgroups.Open(filepath.Join(home, "app.sqlite"))
		if err != nil {
			return daemon.App{}, err
		}
		scheduler := &automatic.Scheduler{Registry: registry, Path: filepath.Join(home, "app.sqlite"), Discover: disc.Poll, Interval: time.Hour,
			Notify: func(status automatic.Status) { _ = server.Publish("automatic", status) }}
		runner, err := jobs.Open(filepath.Join(home, "app.sqlite"), &jobs.Local{Registry: registry, Groups: groups, Config: cfg, Client: client, FixDirectory: filepath.Join(home, "fixes"), Features: fixtureBuilder{},
			Backends: generate.Backends{Claude: func(string) generate.CheckedBackend { return fixtureFixer{backend} }}}, 1, func(job jobs.Job) {
			_ = server.PublishProgress(job)
			_ = server.Publish("index", map[string]any{})
		})
		if err != nil {
			return daemon.App{}, err
		}
		scheduler.Queue = runner
		changed := func() { _ = server.Publish("index", struct{}{}) }
		poll := func(ctx context.Context, repo github.Repository) { _, _ = disc.Poll(ctx, repo) }
		// The first-run tour would cover the page.
		uiState := &uistate.Store{Path: filepath.Join(home, "app.sqlite")}
		if _, err := uiState.Replace("tour", "", 0, "real-reviews-fixture:1", json.RawMessage(`{"seen":true}`)); err != nil {
			return daemon.App{}, err
		}
		api := uiserver.LocalAPI{Registry: registry, Config: cfg, PRs: uiserver.DiscoveryHandler(registry, disc, nil),
			Merge:  uiserver.MergeHandler(registry, client, func(ctx context.Context, repo github.Repository, _ int, _ string) { poll(ctx, repo) }, changed),
			Update: uiserver.UpdateHandler(registry, prupdate.Service{Client: client}, poll, changed),
			Index:  index.Handler(), Jobs: runner.Handler(), Chat: uiserver.ChatHandler(registry, runner), Automatic: scheduler.Handler(), UIState: uiState.Handler(),
			Inboxes: (&inboxes.API{Store: inboxes.Open(filepath.Join(home, "app.sqlite"))}).Handler(),
			Groups:  (&reviewgroups.API{Store: groups, Registry: registry, Index: index.Build, Changed: changed}).Handler()}.Handler()
		return daemon.App{API: api, Assets: localapp.Handler(), Shutdown: runner.Shutdown, Close: func() { api.Close(); _ = groups.Close() }, Drain: runner.Drain, Resume: runner.Resume}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func realAttachmentIDs(document []byte) []string {
	var doc struct {
		Index struct {
			Attachments []struct {
				ID string `json:"id"`
			} `json:"attachments"`
		} `json:"index"`
	}
	_ = json.Unmarshal(document, &doc)
	var ids []string
	for _, a := range doc.Index.Attachments {
		ids = append(ids, a.ID)
	}
	return ids
}

func gitCatFile(dir, oid string) (string, error) {
	out, err := exec.Command("git", "-C", dir, "-c", "safe.directory=*", "cat-file", "-e", oid+"^{commit}").CombinedOutput()
	return strings.TrimSpace(string(out)), err
}
