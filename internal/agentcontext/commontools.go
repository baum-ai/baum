package agentcontext

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// commonToolNames are the tools of every page: they read the app as a whole,
// not what one page shows. New adds them to the tools of every page, and
// RegisterPage to those of each page registered later.
var commonToolNames = []string{"search", "link", "app_help", "daemon_status"}

// withCommonTools is names followed by the common tools, each once.
func withCommonTools(names []string) []string {
	out := slices.Clone(names)
	for _, name := range commonToolNames {
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

// commonTools are the tools every page offers.
func (s *Server) commonTools() []Tool {
	return []Tool{{
		Name:        "search",
		Description: "Finds pull requests, review groups, running sessions, repositories, features and decisions by words, as the app's Go to (Ctrl or Cmd+K) does: every word must be in the item's title, repository, kind or keywords, ignoring case; a title that starts with the words comes first. Each hit names its kind and the page it opens (the page fields current_page uses). The first hits only: pass limit (at most 50) for more or narrow the words.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","minLength":1,"maxLength":200,"description":"The words to look for, such as \"checkout 42\" or \"retry\"."},` +
			`"kinds":{"type":"array","maxItems":6,"items":{"type":"string","enum":["pull_request","group","session","repository","feature","decision"]},"description":"Only these kinds. Default: all."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":50,"description":"The most hits to return. Default 20."}},` +
			`"required":["query"],"additionalProperties":false}`),
		// Searches every repository, so an action never sees it.
		Scope: ScopeGlobal,
		Reads: []string{"/api/index", "/api/groups", "/api/live-sessions", "/api/repositories", "/api/timeline/features", "/api/timeline/decisions"},
		Call:  callSearch,
	}, {
		Name:        "link",
		Description: "The address the baum app uses for a page and what it shows, as a relative query string such as ?page=pulls or ?repository=acme/web&pr=42. The chat panel turns it into a link the person can click, so write it as a Markdown link. Reads nothing. Pages: inbox, pulls, timeline, sessions, analytics, jobs, repositories, settings (all take filters, a map of the page's own address parameters such as repo or who), and pull_request (repository, pull_request), group (group), session (session as agent:id), job (job), feature (feature). settings takes a section; repositories takes a repository and then a section.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"page":{"type":"string","enum":["inbox","pulls","timeline","sessions","analytics","jobs","repositories","settings","pull_request","group","session","job","feature"],"description":"The page to link to."},` +
			`"repository":{"type":"string","description":"owner/name: for pull_request, or repositories to open one repository's page; context only (not in the address) for group, session, job and feature."},` +
			`"pull_request":{"type":"integer","minimum":1,"description":"The pull request number, for pull_request."},` +
			`"group":{"type":"string","description":"The group id, for group."},` +
			`"session":{"type":"string","description":"The session as agent:id, for session."},` +
			`"job":{"type":"string","description":"The job id, for job."},` +
			`"feature":{"type":"string","description":"The feature id, for feature."},` +
			`"section":{"type":"string","description":"The settings section (general, reviews, fixes, connections, computer, prompts) or, with a repository, its section (reviews, prompts, registration)."},` +
			`"filters":{"type":"object","additionalProperties":{"type":"string"},"description":"Address parameters of a list page, such as {\"repo\":\"acme/web\"}."}},` +
			`"required":["page"],"additionalProperties":false}`),
		// It reads nothing, so an action may build addresses too, but not for
		// another repository.
		Scope: ScopeRepository,
		Call:  callLink,
	}, {
		Name:        "app_help",
		Description: "What the baum app is and how to use it: each page, the words it uses (inbox, up next, group, review, fix, session, job, timeline, decision, feature, prompt) and its keyboard shortcuts. Pass topic to get one section: a page name, a word, or shortcuts. Reads nothing.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"topic":{"type":"string","maxLength":64,"description":"One section: a page (inbox, pulls, timeline, sessions, analytics, jobs, repositories, settings, pull_request, group, session, job, feature), a word, or shortcuts. Default: the whole help."}},"additionalProperties":false}`),
		Scope:       ScopeRepository,
		Call:        callAppHelp,
	}, {
		Name:        "daemon_status",
		Description: "How baum's background daemon is doing: that it answers, its version, each repository's last and next refresh from GitHub, and how many jobs wait and run. No paths, environment or diagnostics.",
		Scope:       ScopeGlobal,
		Reads:       []string{"/api/background", "/api/jobs"},
		Call:        s.callDaemonStatus,
	}}
}

// hit is one result of search.
type hit struct {
	Kind   string      `json:"kind"`
	Title  string      `json:"title"`
	Detail string      `json:"detail,omitempty"`
	Opens  PageContext `json:"opens"`
}

type candidate struct {
	hit
	// words are the text the query's words are looked for in, lower case.
	words string
}

var searchKinds = []string{"pull_request", "group", "session", "repository", "feature", "decision"}

const defaultSearchLimit, maxSearchLimit = 20, 50

func callSearch(ctx context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Query string   `json:"query"`
		Kinds []string `json:"kinds"`
		Limit int      `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	words := strings.Fields(strings.ToLower(strings.ReplaceAll(a.Query, "#", " ")))
	if len(words) == 0 {
		return "", errors.New("give the words to look for")
	}
	limit := a.Limit
	if limit == 0 {
		limit = defaultSearchLimit
	}
	want := func(kind string) bool { return len(a.Kinds) == 0 || slices.Contains(a.Kinds, kind) }
	var (
		found       []candidate
		unavailable []string
	)
	// read gets one route's body; a route that cannot be read is named, so
	// the others still answer.
	read := func(name, path string, query url.Values) json.RawMessage {
		body, missing, err := c.get(ctx, path, query)
		if err != nil || missing || disabled(body) {
			unavailable = append(unavailable, name)
			return nil
		}
		return body
	}
	// The daemon's own lists, as Go to has them: in memory, so all are read.
	if want("pull_request") {
		found = append(found, pullCandidates(read("pull_requests", "/api/index", url.Values{"history": {"1"}}))...)
	}
	if want("group") {
		found = append(found, groupCandidates(read("groups", "/api/groups", nil))...)
	}
	if want("session") {
		found = append(found, sessionCandidates(read("sessions", "/api/live-sessions", nil))...)
	}
	if want("repository") {
		found = append(found, repositoryCandidates(read("repositories", "/api/repositories", nil))...)
	}
	hits := matches(found, words)
	// The timeline's features and decisions are searched by the daemon, one
	// query each, and only while the hits so far leave room.
	for _, kind := range []string{"feature", "decision"} {
		room := limit + 1 - len(hits)
		if !want(kind) || room <= 0 {
			continue
		}
		path, collect := "/api/timeline/decisions", decisionCandidates
		if kind == "feature" {
			path, collect = "/api/timeline/features", featureCandidates
		}
		query := url.Values{"q": {strings.Join(words, " ")}, "limit": {strconv.Itoa(min(room, maxSearchLimit+1))}}
		for _, c := range collect(read(kind+"s", path, query), a.Query) {
			hits = append(hits, c.hit)
		}
	}
	if len(unavailable) > 0 && len(unavailable) == countSources(a.Kinds) {
		return "", errors.New("the daemon could not be searched: " + strings.Join(unavailable, ", ") + " are unavailable")
	}
	truncated := len(hits) > limit
	if truncated {
		hits = hits[:limit]
	}
	if hits == nil {
		hits = []hit{}
	}
	return compactJSON(struct {
		Query       string   `json:"query"`
		Hits        []hit    `json:"hits"`
		Truncated   bool     `json:"truncated,omitempty"`
		Unavailable []string `json:"unavailable,omitempty"`
	}{a.Query, hits, truncated, unavailable})
}

// countSources is how many routes a search of kinds reads.
func countSources(kinds []string) int {
	if len(kinds) == 0 {
		return len(searchKinds)
	}
	n := 0
	for _, k := range searchKinds {
		if slices.Contains(kinds, k) {
			n++
		}
	}
	return n
}

// matches are the candidates whose text holds every word, best first as Go to
// ranks them: a title that starts with the words, then one with a word that
// does, then the rest in the order given.
func matches(candidates []candidate, words []string) []hit {
	whole := strings.Join(words, " ")
	type ranked struct {
		hit  hit
		rank int
	}
	var out []ranked
	seen := map[string]bool{}
	for _, c := range candidates {
		if slices.ContainsFunc(words, func(w string) bool { return !strings.Contains(c.words, w) }) {
			continue
		}
		o := c.Opens
		key := fmt.Sprint(c.Kind, "\x00", o.Page, "\x00", o.Repository, "\x00", o.PullRequest, "\x00", o.Group, "\x00", o.Session, "\x00", o.Feature)
		if c.Kind == "decision" {
			// Decisions share a page; their words tell them apart.
			key += "\x00" + c.Title + "\x00" + c.Detail
		}
		if seen[key] {
			continue
		}
		seen[key] = true
		title := strings.ToLower(c.Title)
		rank := 2
		if strings.HasPrefix(title, whole) {
			rank = 0
		} else if slices.ContainsFunc(nonWord.Split(title, -1), func(part string) bool { return strings.HasPrefix(part, words[0]) }) {
			rank = 1
		}
		out = append(out, ranked{c.hit, rank})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].rank < out[j].rank })
	hits := make([]hit, len(out))
	for i, r := range out {
		hits[i] = r.hit
	}
	return hits
}

// disabled reports a capability the daemon has not got: the route answers 200
// {"available":false} instead of its data.
func disabled(body json.RawMessage) bool {
	var v struct {
		Available *bool `json:"available"`
	}
	return json.Unmarshal(body, &v) == nil && v.Available != nil && !*v.Available
}

var nonWord = regexp.MustCompile(`[^a-z0-9]+`)

func candidateOf(kind, title, detail, keywords string, opens PageContext) candidate {
	return candidate{hit: hit{Kind: kind, Title: title, Detail: detail, Opens: opens}, words: strings.ToLower(title + " " + detail + " " + strings.ReplaceAll(kind, "_", " ") + " " + keywords)}
}

func pullCandidates(body json.RawMessage) []candidate {
	var index struct {
		Entries []struct {
			PullRequest struct {
				Repository string `json:"repository"`
				Number     int    `json:"number"`
				Title      string `json:"title"`
				Author     string `json:"author"`
				HeadRef    string `json:"head_ref"`
			} `json:"pull_request"`
		} `json:"entries"`
	}
	if json.Unmarshal(body, &index) != nil {
		return nil
	}
	var out []candidate
	for _, e := range index.Entries {
		pr := e.PullRequest
		if pr.Number == 0 || pr.Repository == "" {
			continue
		}
		out = append(out, candidateOf("pull_request", pr.Title, fmt.Sprintf("%s #%d", pr.Repository, pr.Number),
			fmt.Sprintf("%d %s %s", pr.Number, pr.Author, pr.HeadRef),
			PageContext{Page: PagePullRequest, Repository: pr.Repository, PullRequest: pr.Number}))
	}
	return out
}

func groupCandidates(body json.RawMessage) []candidate {
	var list struct {
		Groups []struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Repository string `json:"repository"`
		} `json:"groups"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	var out []candidate
	for _, g := range list.Groups {
		if g.ID != "" {
			out = append(out, candidateOf("group", g.Name, g.Repository, "", PageContext{Page: PageGroup, Repository: g.Repository, Group: g.ID}))
		}
	}
	return out
}

func sessionCandidates(body json.RawMessage) []candidate {
	var list struct {
		Sessions []struct {
			Agent      string `json:"agent"`
			ID         string `json:"session_id"`
			Title      string `json:"title"`
			Repository string `json:"repository"`
			Branch     string `json:"branch"`
		} `json:"sessions"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	var out []candidate
	for _, s := range list.Sessions {
		if s.ID == "" {
			continue
		}
		title := s.Title
		if title == "" {
			on := s.Branch
			if on == "" {
				on = s.Repository
			}
			title = s.Agent + " on " + on
		}
		detail := strings.Join(slices.DeleteFunc([]string{s.Agent, s.Repository, s.Branch}, func(p string) bool { return p == "" }), " · ")
		out = append(out, candidateOf("session", title, detail, "", PageContext{Page: PageSession, Repository: s.Repository, Session: s.Agent + ":" + s.ID}))
	}
	return out
}

func repositoryCandidates(body json.RawMessage) []candidate {
	var list []struct {
		Repository string `json:"repository"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	var out []candidate
	for _, r := range list {
		if r.Repository != "" {
			out = append(out, candidateOf("repository", r.Repository, "", "", PageContext{Page: PageRepositories, Repository: r.Repository}))
		}
	}
	return out
}

// featureCandidates are the features the daemon found for a query, which it
// matched itself.
func featureCandidates(body json.RawMessage, _ string) []candidate {
	var list struct {
		Features []struct {
			ID         string `json:"id"`
			Title      string `json:"title"`
			Repository *struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"features"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	var out []candidate
	for _, f := range list.Features {
		repository := ""
		if f.Repository != nil {
			repository = f.Repository.FullName
		}
		if f.ID != "" && f.Title != "" {
			out = append(out, candidateOf("feature", f.Title, repository, "", PageContext{Page: PageFeature, Repository: repository, Feature: f.ID}))
		}
	}
	return out
}

// decisionCandidates are the decisions the daemon found for a query.
func decisionCandidates(body json.RawMessage, query string) []candidate {
	var list struct {
		Decisions []struct {
			ID         string `json:"id"`
			Summary    string `json:"summary"`
			Category   string `json:"category"`
			Repository *struct {
				FullName string `json:"full_name"`
			} `json:"repository"`
		} `json:"decisions"`
	}
	if json.Unmarshal(body, &list) != nil {
		return nil
	}
	var out []candidate
	for _, d := range list.Decisions {
		repository := ""
		if d.Repository != nil {
			repository = d.Repository.FullName
		}
		if d.ID == "" || d.Summary == "" {
			continue
		}
		// A decision has no page of its own: the Timeline's All decisions
		// tab, filtered to its words, shows it.
		opens := PageContext{Page: PageTimeline, Filters: map[string]string{"tab": "all", "q": query}}
		if repository != "" {
			opens.Filters["repo"] = repository
		}
		detail := strings.Join(slices.DeleteFunc([]string{repository, d.Category}, func(p string) bool { return p == "" }), " · ")
		out = append(out, candidateOf("decision", d.Summary, detail, "", opens))
	}
	return out
}

// pageSubjects are the address fields each page takes.
var pageSubjects = map[string][]string{
	PageInbox:        {"filters"},
	PagePulls:        {"filters"},
	PageTimeline:     {"filters"},
	PageSessions:     {"filters"},
	PageAnalytics:    {"filters"},
	PageJobs:         {"filters"},
	PageRepositories: {"repository", "section", "filters"},
	PageSettings:     {"section", "filters"},
	PagePullRequest:  {"repository", "pull_request"},
	PageGroup:        {"group", "repository"},
	PageSession:      {"session", "repository"},
	PageJob:          {"job", "repository"},
	PageFeature:      {"feature", "repository"},
}

// requiredSubjects are the fields a page's address cannot do without.
var requiredSubjects = map[string][]string{
	PagePullRequest: {"repository", "pull_request"},
	PageGroup:       {"group"},
	PageSession:     {"session"},
	PageJob:         {"job"},
	PageFeature:     {"feature"},
}

var (
	settingsSections   = []string{"general", "reviews", "fixes", "connections", "computer", "prompts"}
	repositorySections = []string{"reviews", "prompts", "registration"}
	// reservedKeys are the address parameters that name a page or its subject:
	// they are set by their own arguments, which are checked, never by a filter.
	reservedKeys = map[string]bool{"page": true, "repository": true, "clone": true, "pr": true, "group": true, "session": true,
		"job": true, "feature": true, "section": true, "inbox": true, "peek": true, "walk": true}
	filterKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
)

// checkSection refuses a section the page does not have: Settings has
// settingsSections, a repository's page (the Repositories page with a
// repository) repositorySections, and no other page has any. The page context
// and link share it, so a section current_page reports is one link builds.
func checkSection(page, repository, section string) error {
	switch {
	case section == "":
		return nil
	case page == PageSettings:
		if !slices.Contains(settingsSections, section) {
			return fmt.Errorf("settings has no section %q; it has %s", section, strings.Join(settingsSections, ", "))
		}
	case page == PageRepositories:
		if repository == "" {
			return errors.New("a repository section needs the repository")
		}
		if !slices.Contains(repositorySections, section) {
			return fmt.Errorf("a repository has no section %q; it has %s", section, strings.Join(repositorySections, ", "))
		}
	default:
		return fmt.Errorf("page %s has no sections", page)
	}
	return nil
}

// linkOf is the app address of a page, as the app builds its own: the query
// strings of pages.ts and the detail pages' hrefs.
func linkOf(page, repository string, pullRequest int, group, session, job, feature, section string, filters map[string]string) (string, error) {
	takes, ok := pageSubjects[page]
	if !ok {
		return "", fmt.Errorf("unknown page %q", page)
	}
	given := map[string]bool{
		"repository": repository != "", "pull_request": pullRequest != 0, "group": group != "", "session": session != "",
		"job": job != "", "feature": feature != "", "section": section != "", "filters": len(filters) > 0,
	}
	for _, field := range slices.Sorted(maps.Keys(given)) {
		if given[field] && !slices.Contains(takes, field) {
			return "", fmt.Errorf("page %s takes no %s", page, field)
		}
	}
	for _, field := range requiredSubjects[page] {
		if !given[field] {
			return "", fmt.Errorf("page %s needs a %s", page, field)
		}
	}
	for key, value := range filters {
		if !filterKey.MatchString(key) || reservedKeys[key] || len(value) > 256 {
			return "", fmt.Errorf("filter %q is not a filter: the subject of a page is named by its own argument", key)
		}
	}
	if repository != "" && strings.Count(repository, "/") != 1 {
		return "", errors.New("name the repository as owner/name")
	}
	if err := checkSection(page, repository, section); err != nil {
		return "", err
	}
	query := url.Values{}
	switch page {
	case PagePullRequest:
		return "?repository=" + url.QueryEscape(repository) + "&pr=" + strconv.Itoa(pullRequest), nil
	case PageGroup:
		return "?group=" + url.QueryEscape(group), nil
	case PageSession:
		query.Set("page", "sessions")
		query.Set("session", session)
	case PageJob:
		query.Set("page", "jobs")
		query.Set("job", job)
	case PageFeature:
		query.Set("page", "timeline")
		query.Set("feature", feature)
	case PageSettings:
		query.Set("page", "settings")
		if section != "" {
			query.Set("section", section)
		}
	case PageRepositories:
		query.Set("page", "repositories")
		if repository != "" {
			query.Set("repository", repository)
		}
		if section != "" {
			query.Set("section", section)
		}
	default:
		query.Set("page", page)
		// The Timeline opens on human decisions unless an address says otherwise.
		if page == PageTimeline && len(filters) == 0 {
			query.Set("who", "human")
		}
	}
	for key, value := range filters {
		query.Set(key, value)
	}
	return "?" + encodeOrdered(query), nil
}

// encodeOrdered is query encoded with page first, as the app writes it, then
// the other keys in order.
func encodeOrdered(q url.Values) string {
	keys := make([]string, 0, len(q))
	for key := range q {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if (keys[i] == "page") != (keys[j] == "page") {
			return keys[i] == "page"
		}
		return keys[i] < keys[j]
	})
	parts := make([]string, len(keys))
	for i, key := range keys {
		parts[i] = url.QueryEscape(key) + "=" + url.QueryEscape(q.Get(key))
	}
	return strings.Join(parts, "&")
}

func callLink(_ context.Context, c CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Page        string            `json:"page"`
		Repository  string            `json:"repository"`
		PullRequest int               `json:"pull_request"`
		Group       string            `json:"group"`
		Session     string            `json:"session"`
		Job         string            `json:"job"`
		Feature     string            `json:"feature"`
		Section     string            `json:"section"`
		Filters     map[string]string `json:"filters"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	// An action may name only its own repository, in the subject or in a
	// filter that names repositories (the Timeline's and Analytics' repo).
	if a.Repository != "" {
		repository, _, err := c.Target(a.Repository, 0)
		if err != nil {
			return "", err
		}
		a.Repository = repository
	}
	for part := range strings.SplitSeq(a.Filters["repo"], ",") {
		if part = strings.TrimSpace(part); part != "" {
			if _, _, err := c.Target(part, 0); err != nil {
				return "", err
			}
		}
	}
	address, err := linkOf(a.Page, a.Repository, a.PullRequest, a.Group, a.Session, a.Job, a.Feature, a.Section, a.Filters)
	if err != nil {
		return "", err
	}
	return compactJSON(map[string]string{"page": a.Page, "address": address})
}

//go:embed help.md
var helpText string

// helpSections are the sections of the help, by lower-case heading: the text
// from a "## " line to the next one.
func helpSections() (names []string, sections map[string]string) {
	sections = map[string]string{}
	var name string
	var body strings.Builder
	flush := func() {
		if name != "" {
			sections[name] = strings.TrimSpace(body.String())
		}
		body.Reset()
	}
	for line := range strings.SplitSeq(helpText, "\n") {
		if heading, ok := strings.CutPrefix(line, "## "); ok {
			flush()
			name = strings.ToLower(strings.TrimSpace(heading))
			names = append(names, name)
		}
		body.WriteString(line + "\n")
	}
	flush()
	return names, sections
}

func callAppHelp(_ context.Context, _ CallContext, args json.RawMessage) (string, error) {
	var a struct {
		Topic string `json:"topic"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	topic := strings.ToLower(strings.TrimSpace(a.Topic))
	if topic == "" {
		return strings.TrimSpace(helpText), nil
	}
	names, sections := helpSections()
	topic = strings.ReplaceAll(topic, " ", "_")
	for _, name := range names {
		if strings.ReplaceAll(name, " ", "_") == topic {
			return sections[name], nil
		}
	}
	// A word the app uses, from the words section.
	for line := range strings.SplitSeq(sections["words"], "\n") {
		if strings.HasPrefix(strings.ToLower(line), "- **"+strings.ReplaceAll(topic, "_", " ")+"**") {
			return line, nil
		}
	}
	return "", fmt.Errorf("no help on %q; the topics are %s, or a word from words", a.Topic, strings.Join(names, ", "))
}

// daemonStatus is what daemon_status reports. Every field is safe to show:
// no paths, environment, diagnostics, tokens or error text.
type daemonStatus struct {
	Health       string         `json:"health"`
	Version      string         `json:"version"`
	Repositories []refreshState `json:"repositories"`
	Jobs         jobCounts      `json:"jobs"`
}

type refreshState struct {
	Repository  string `json:"repository"`
	LastSuccess string `json:"last_success,omitempty"`
	NextRefresh string `json:"next_refresh,omitempty"`
	Failures    int    `json:"failures,omitempty"`
	// Failing marks a repository whose latest refresh from GitHub failed.
	Failing  bool `json:"failing,omitempty"`
	Problems int  `json:"problems,omitempty"`
}

type jobCounts struct {
	Queued  int `json:"queued"`
	Running int `json:"running"`
	// Unavailable says the queue could not be read.
	Unavailable bool `json:"unavailable,omitempty"`
}

func (s *Server) callDaemonStatus(ctx context.Context, c CallContext, _ json.RawMessage) (string, error) {
	// The app server answers /api/health itself, before the API this reads
	// through; a call that reaches here has been answered by the daemon.
	out := daemonStatus{Health: "ok", Version: s.version, Repositories: []refreshState{}}
	body, _, err := c.get(ctx, "/api/background", nil)
	if err != nil {
		return "", fmt.Errorf("the background work is unavailable: %w", err)
	}
	if disabled(body) {
		return "", errors.New("the background work is unavailable: the daemon has none")
	}
	var states []struct {
		Repository  string `json:"repository"`
		LastSuccess string `json:"last_success"`
		NextRefresh string `json:"next_refresh"`
		Error       string `json:"error"`
		Failures    int    `json:"failures"`
		Problems    []struct {
			PR int `json:"pr"`
		} `json:"problems"`
	}
	if json.Unmarshal(body, &states) != nil {
		return "", errors.New("the background work is not readable")
	}
	for _, st := range states {
		out.Repositories = append(out.Repositories, refreshState{
			Repository: st.Repository, LastSuccess: zeroTime(st.LastSuccess), NextRefresh: zeroTime(st.NextRefresh),
			Failures: st.Failures, Failing: st.Error != "", Problems: len(st.Problems),
		})
	}
	body, _, err = c.get(ctx, "/api/jobs", nil)
	var jobs struct {
		Jobs []struct {
			State string `json:"state"`
		} `json:"jobs"`
	}
	if err != nil || disabled(body) || json.Unmarshal(body, &jobs) != nil {
		out.Jobs.Unavailable = true
	}
	for _, j := range jobs.Jobs {
		switch j.State {
		case "queued":
			out.Jobs.Queued++
		case "running":
			out.Jobs.Running++
		}
	}
	return compactJSON(out)
}

// zeroTime is "" for the zero time a state that never refreshed carries.
func zeroTime(t string) string {
	if strings.HasPrefix(t, "0001-01-01") {
		return ""
	}
	return t
}
