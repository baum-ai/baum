package seeddata

import (
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
)

// The invented organisation the dataset belongs to: its owners, their
// repositories and the people who open pull requests there. Nothing in it
// comes from a real project or person.
var (
	owners = []string{"northwind", "lumen-labs", "tidepool", "quarry", "fernhill", "bramble-io"}
	nouns  = []string{"api", "web", "billing", "search", "ingest", "auth", "notify", "ledger", "atlas", "relay",
		"gateway", "scheduler", "catalog", "metrics", "export", "mobile", "sync", "vault", "pipeline", "docs",
		"console", "router", "importer", "renderer", "reports", "tasks", "cache", "orders", "profiles", "uploads"}
	people = []string{"ana-silva", "bo-chen", "cyra-okafor", "dev-patel", "eli-novak", "fumi-sato", "gus-meyer",
		"hana-kim", "ivo-petrov", "jules-martin", "kai-larsen", "lena-park", "milo-reyes", "nia-adams", "omar-haddad"}
	bots = []string{"dependabot[bot]", "renovate[bot]"}
	// verbs and topics make pull request titles.
	verbs  = []string{"Add", "Fix", "Refactor", "Speed up", "Remove", "Document", "Rename", "Split", "Cache", "Validate", "Retry", "Log", "Test", "Migrate"}
	topics = []string{"the retry budget", "pagination in list endpoints", "the session cookie", "CSV export", "webhook signatures",
		"the rate limiter", "nightly cleanup", "invoice rounding", "the search index", "feature flags", "audit events",
		"the upload size check", "time zone handling", "the health check", "connection pooling", "error pages",
		"the settings form", "batch imports", "stale cache entries", "the onboarding email", "permission checks",
		"the metrics exporter", "slow queries", "the config loader", "duplicate notifications", "the dark theme"}
	directories = []string{"internal/server", "internal/store", "internal/jobs", "internal/auth", "cmd/service", "web/src", "docs", "config", "internal/billing", "pkg/client"}
)

// viewer is the person the dataset belongs to: the signed-in GitHub account.
const viewer = "dana-whitfield"

// repoPlan is one repository: its identity, the files on main and its pull
// requests.
type repoPlan struct {
	index        int
	owner, name  string
	files        []filePlan
	pullRequests []*prPlan
}

func (r *repoPlan) identity() string { return r.owner + "/" + r.name }

// filePlan is a file on main: its path and line count.
type filePlan struct {
	path  string
	lines int
}

// prPlan is a pull request: who opened it, how big it is and how it stands
// on GitHub.
type prPlan struct {
	repo                *repoPlan
	number              int
	title               string
	branch              string
	author              string
	bot                 bool
	draft               bool
	requested, assigned bool
	// lines is the size of the change its first commit makes; each later
	// version adds a small fix-up.
	lines    int
	versions int
	// reviewed is how many of its versions have a saved review (0: none).
	reviewed              int
	review, checks, merge string
	unresolved            int
	// updated is how long before the epoch GitHub last updated it, in minutes.
	updated int
	// big marks the pull request with the large diff.
	big bool
	// commented: its review has a GitHub conversation with threads.
	commented bool
}

func (p *prPlan) key() string { return fmt.Sprintf("%s#%d", p.repo.identity(), p.number) }

// plan lays out the repositories and pull requests from the seed alone.
func plan(o Options) []*repoPlan {
	rng := rand.New(rand.NewPCG(uint64(o.Seed), 0x5eed_da7a))
	repos := make([]*repoPlan, 0, o.Repositories)
	used := map[string]bool{}
	for i := 0; i < o.Repositories; i++ {
		var owner, name string
		for {
			owner = owners[rng.IntN(len(owners))]
			name = nouns[rng.IntN(len(nouns))]
			if !used[owner+"/"+name] {
				break
			}
			// More repositories than names: number them.
			if len(used) >= len(owners)*len(nouns) {
				name = fmt.Sprintf("%s-%d", name, i)
				break
			}
		}
		used[owner+"/"+name] = true
		r := &repoPlan{index: i, owner: owner, name: name}
		// A large file in every repository, so a pull request can replace
		// a long stretch of it.
		r.files = append(r.files, filePlan{path: "internal/legacy/table.go", lines: 900})
		count := 12 + rng.IntN(18)
		for f := 0; f < count; f++ {
			dir := directories[rng.IntN(len(directories))]
			ext := ".go"
			switch {
			case strings.HasPrefix(dir, "web"):
				ext = ".ts"
			case dir == "docs":
				ext = ".md"
			case dir == "config":
				ext = ".yaml"
			}
			path := fmt.Sprintf("%s/%s_%d%s", dir, topicWord(rng), f, ext)
			r.files = append(r.files, filePlan{path: path, lines: 40 + rng.IntN(220)})
		}
		repos = append(repos, r)
	}
	// Pull requests spread unevenly: a few busy repositories hold most.
	weights := make([]float64, len(repos))
	total := 0.0
	for i := range repos {
		weights[i] = 1 / float64(i+2)
		total += weights[i]
	}
	for n := 0; n < o.PullRequests; n++ {
		pick := rng.Float64() * total
		i := 0
		for ; i < len(repos)-1 && pick > weights[i]; i++ {
			pick -= weights[i]
		}
		r := repos[i]
		pr := &prPlan{repo: r, number: 100 + len(r.pullRequests)*7 + rng.IntN(7)}
		r.pullRequests = append(r.pullRequests, pr)
		fill(rng, pr)
	}
	// Every repository has at least one pull request.
	for _, r := range repos {
		if len(r.pullRequests) == 0 {
			pr := &prPlan{repo: r, number: 100}
			r.pullRequests = append(r.pullRequests, pr)
			fill(rng, pr)
		}
	}
	// The large diff goes to the busiest repository's first pull request.
	big := repos[0].pullRequests[0]
	big.big, big.lines, big.versions, big.reviewed, big.bot, big.draft = true, o.LargeDiffLines, 2, 2, false, false
	big.title = "Replace the legacy routing table with generated handlers"
	big.author = people[0]
	big.requested = true
	for _, r := range repos {
		slices.SortFunc(r.pullRequests, func(a, b *prPlan) int { return a.number - b.number })
		for _, pr := range r.pullRequests {
			pr.branch = fmt.Sprintf("%s/pr-%d", branchWord(pr.title), pr.number)
		}
	}
	return repos
}

// fill decides one pull request's author, size, versions and state.
func fill(rng *rand.Rand, pr *prPlan) {
	pr.title = verbs[rng.IntN(len(verbs))] + " " + topics[rng.IntN(len(topics))]
	switch roll := rng.IntN(100); {
	case roll < 25:
		pr.author = viewer
	case roll < 33:
		pr.author, pr.bot = bots[rng.IntN(len(bots))], true
		pr.title = fmt.Sprintf("Bump %s from 1.%d.0 to 1.%d.0", nouns[rng.IntN(len(nouns))], rng.IntN(9), 9+rng.IntN(9))
	default:
		pr.author = people[rng.IntN(len(people))]
		pr.requested = rng.IntN(100) < 45
		pr.assigned = rng.IntN(100) < 10
	}
	// Most pull requests are small, with a long tail.
	switch roll := rng.IntN(100); {
	case roll < 60:
		pr.lines = 2 + rng.IntN(40)
	case roll < 85:
		pr.lines = 40 + rng.IntN(160)
	case roll < 97:
		pr.lines = 200 + rng.IntN(600)
	default:
		pr.lines = 800 + rng.IntN(1400)
	}
	pr.versions = 1 + rng.IntN(4)
	switch roll := rng.IntN(100); {
	case roll < 25:
		pr.reviewed = 0
	case roll < 60:
		pr.reviewed = 1
	default:
		pr.reviewed = pr.versions
	}
	pr.reviewed = min(pr.reviewed, pr.versions)
	pr.draft = !pr.bot && rng.IntN(100) < 12
	pr.review = []string{"unreviewed", "unreviewed", "commented", "approved", "changes_requested"}[rng.IntN(5)]
	pr.checks = []string{"passing", "passing", "passing", "failing", "running", "none"}[rng.IntN(6)]
	switch {
	case pr.draft:
		pr.merge = "draft"
	case rng.IntN(100) < 8:
		pr.merge = "conflict"
	case pr.review == "approved" && pr.checks == "passing":
		pr.merge = "ready"
	default:
		pr.merge = "blocked"
	}
	if pr.review == "changes_requested" || pr.review == "commented" {
		pr.unresolved = rng.IntN(4)
	}
	pr.commented = pr.review != "unreviewed" && !pr.bot
	pr.updated = 5 + rng.IntN(60*24*30)
}

var titleWords = []string{"router", "ledger", "table", "budget", "export", "worker", "client", "handler", "limits", "format"}

func topicWord(rng *rand.Rand) string { return titleWords[rng.IntN(len(titleWords))] }

// branchWord is a branch name's prefix from a title.
func branchWord(title string) string {
	fields := strings.Fields(strings.ToLower(title))
	var out []string
	for _, f := range fields {
		f = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
				return r
			}
			return -1
		}, f)
		if f != "" && f != "the" {
			out = append(out, f)
		}
		if len(out) == 3 {
			break
		}
	}
	return strings.Join(out, "-")
}
