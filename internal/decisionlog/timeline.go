package decisionlog

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/baum-ai/baum/features"
	"github.com/baum-ai/baum/internal/gitlocal"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/store"
)

// The Timeline API (README.md, "Timeline API") serves the app's Timeline
// page from the store: a repository's features of its current version and
// its sessions' current decisions, in the view models of the review
// viewer's decision log (oss/viewer/src/decision-log.ts).

// TimelinePageSize is how many rows a list answers with unless asked for
// more; TimelineMaxPage is the most it answers with.
const (
	TimelinePageSize = 50
	TimelineMaxPage  = 500
)

// TimelineRegistry is what the Timeline reads of the repository registry;
// *repos.Registry is one.
type TimelineRegistry interface {
	Preferences(context.Context) (repos.Preferences, error)
	Repositories(context.Context) ([]repos.Repository, error)
}

// Person is who a session or pull request belongs to.
type Person struct {
	Email     string  `json:"email"`
	Name      *string `json:"name"`
	Login     *string `json:"login"`
	AvatarURL *string `json:"avatar_url"`
}

// TimelineClone is what one request reads of a repository's clone; it reads
// each once.
type TimelineClone interface {
	// Person is who this machine's sessions belong to, nil when unknown.
	Person(ctx context.Context) *Person
	// Review is the summary of the pull request's latest saved review and
	// its state against head ("current" or "outdated"), "" and "none"
	// without one.
	Review(ctx context.Context, number int, head string) (summary, state string)
}

// Timeline serves /api/timeline/.
type Timeline struct {
	Store    *Store
	Registry TimelineRegistry
	// Clone opens the repository's clone for a request; nil, or a nil
	// answer, reads nobody and no review.
	Clone func(ctx context.Context, repository string) TimelineClone
}

type timelineDecision struct {
	ID         string    `json:"id"`
	SessionID  string    `json:"session_id"`
	Turn       int       `json:"turn"`
	DecidedAt  time.Time `json:"decided_at"`
	Who        string    `json:"who"`
	Person     *Person   `json:"person"`
	Vendor     string    `json:"vendor"`
	Category   string    `json:"category"`
	Summary    string    `json:"summary"`
	Superseded bool      `json:"superseded"`
	FileCount  int       `json:"file_count"`
}

type timelineKeyDecision struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Turn      int    `json:"turn"`
	Who       string `json:"who"`
	Category  string `json:"category"`
	Summary   string `json:"summary"`
}

type timelineFeature struct {
	ID               string                `json:"id"`
	Title            *string               `json:"title"`
	Description      *string               `json:"description"`
	Status           features.Status       `json:"status"`
	FirstActivityAt  *time.Time            `json:"first_activity_at"`
	LastActivityAt   *time.Time            `json:"last_activity_at"`
	SummaryPending   bool                  `json:"summary_pending"`
	People           []Person              `json:"people"`
	PullRequestCount int                   `json:"pull_request_count"`
	SessionCount     int                   `json:"session_count"`
	DecisionCount    int                   `json:"decision_count"`
	KeyDecisions     []timelineKeyDecision `json:"key_decisions"`
}

type timelinePull struct {
	Number      int                `json:"number"`
	Title       string             `json:"title"`
	State       string             `json:"state"`
	Draft       bool               `json:"draft"`
	Merged      bool               `json:"merged"`
	ReviewState string             `json:"review_state"`
	Summary     *string            `json:"summary"`
	Decisions   []timelineDecision `json:"decisions"`
}

type timelineSession struct {
	ID        string             `json:"id"`
	Person    *Person            `json:"person"`
	Vendor    string             `json:"vendor"`
	Branch    string             `json:"branch"`
	StartedAt *time.Time         `json:"started_at"`
	EndedAt   *time.Time         `json:"ended_at"`
	Decisions []timelineDecision `json:"decisions"`
}

// Handler serves the Timeline API. Every route names, or reads from, a
// picked repository whose decisions are not turned off; another is a 400
// for a list and a 404 for a detail.
func (t *Timeline) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/timeline/repositories", func(w http.ResponseWriter, r *http.Request) {
		answer, err := t.repositoryList(r.Context())
		if err != nil {
			timelineError(w, err)
			return
		}
		writeTimeline(w, answer)
	})
	mux.HandleFunc("GET /api/timeline/features", func(w http.ResponseWriter, r *http.Request) {
		repository, page, ok := t.listQuery(w, r)
		if !ok {
			return
		}
		q := r.URL.Query()
		statuses := multi(q, "status")
		snap, err := t.snapshot(r.Context(), repository, q.Get("q"), "")
		if err != nil {
			timelineError(w, err)
			return
		}
		var after *snapshotFeature
		if page.after != nil {
			after = &snapshotFeature{createdAt: page.after.at, row: timelineFeature{ID: page.after.id}}
		}
		rows := []timelineFeature{}
		var next *string
		for _, f := range snap.ordered() {
			if after != nil && featureOrder(f, after) <= 0 {
				continue
			}
			if len(statuses) > 0 && !slices.Contains(statuses, string(f.row.Status)) {
				continue
			}
			if len(rows) == page.limit {
				last := rows[len(rows)-1]
				next = cursorOf(lastActive(snap.features[last.ID]), last.ID)
				break
			}
			rows = append(rows, f.row)
		}
		writeTimeline(w, map[string]any{"features": rows, "next_cursor": next, "order": "newest"})
	})
	mux.HandleFunc("GET /api/timeline/features/{id}", func(w http.ResponseWriter, r *http.Request) {
		detail, err := t.feature(r.Context(), r.PathValue("id"))
		if err != nil {
			timelineError(w, err)
			return
		}
		writeTimeline(w, detail)
	})
	mux.HandleFunc("GET /api/timeline/build", func(w http.ResponseWriter, r *http.Request) {
		repository, err := t.allowed(r.Context(), r.URL.Query().Get("repository"))
		if err != nil {
			timelineError(w, err)
			return
		}
		building, err := t.build(r.Context(), repository)
		if err != nil {
			timelineError(w, err)
			return
		}
		features, since, err := t.current(r.Context(), repository)
		if err != nil {
			timelineError(w, err)
			return
		}
		writeTimeline(w, map[string]any{"building": building, "has_features": features, "current_since": since})
	})
	mux.HandleFunc("GET /api/timeline/decisions", func(w http.ResponseWriter, r *http.Request) {
		repository, page, ok := t.listQuery(w, r)
		if !ok {
			return
		}
		answer, err := t.decisions(r.Context(), repository, r.URL.Query(), page)
		if err != nil {
			timelineError(w, err)
			return
		}
		writeTimeline(w, answer)
	})
	mux.HandleFunc("GET /api/timeline/decisions/{id}", func(w http.ResponseWriter, r *http.Request) {
		detail, err := t.decision(r.Context(), r.PathValue("id"))
		if err != nil {
			timelineError(w, err)
			return
		}
		writeTimeline(w, detail)
	})
	return mux
}

var (
	errTimelineNotFound   = errors.New("not found")
	errTimelineRepository = errors.New("repository is not one of your repositories with decisions on")
	errTimelineQuery      = errors.New("invalid query")
)

func timelineError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTimelineNotFound):
		http.NotFound(w, nil)
	case errors.Is(err, errTimelineRepository), errors.Is(err, errTimelineQuery):
		http.Error(w, err.Error(), http.StatusBadRequest)
	default:
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
	}
}

func writeTimeline(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(body)
}

// repositoryList answers /repositories: whether the decisions switch is on,
// the picked repositories the Timeline serves and those whose decisions
// are turned off.
func (t *Timeline) repositoryList(ctx context.Context) (map[string]any, error) {
	prefs, err := t.Registry.Preferences(ctx)
	if err != nil {
		return nil, err
	}
	list, err := t.Registry.Repositories(ctx)
	if err != nil {
		return nil, err
	}
	enabled := prefs.Decisions.Enabled == nil || *prefs.Decisions.Enabled
	served, off := []map[string]string{}, []string{}
	for _, name := range servedRepositories(prefs, list) {
		served = append(served, map[string]string{"repository": name})
	}
	for _, r := range list {
		if r.Settings.Decisions != nil && !*r.Settings.Decisions {
			off = append(off, r.Repository)
		}
	}
	slices.Sort(off)
	return map[string]any{"decisions_enabled": enabled, "repositories": served, "turned_off": off}, nil
}

// servedRepositories are the picked repositories whose decisions are not
// turned off, by the registry's names. The automatic master switch is not
// read: pausing capture does not hide what was captured.
func servedRepositories(prefs repos.Preferences, list []repos.Repository) []string {
	names := []string{}
	if prefs.Decisions.Enabled != nil && !*prefs.Decisions.Enabled {
		return names
	}
	for _, r := range list {
		if r.Settings.Decisions == nil || *r.Settings.Decisions {
			names = append(names, r.Repository)
		}
	}
	slices.Sort(names)
	return names
}

// allowed is the registry's name of repository when the Timeline serves
// it.
func (t *Timeline) allowed(ctx context.Context, repository string) (string, error) {
	prefs, err := t.Registry.Preferences(ctx)
	if err != nil {
		return "", err
	}
	list, err := t.Registry.Repositories(ctx)
	if err != nil {
		return "", err
	}
	for _, name := range servedRepositories(prefs, list) {
		if strings.EqualFold(name, repository) {
			return name, nil
		}
	}
	return "", errTimelineRepository
}

// listPage is a list's page: the row its cursor names, rows come after,
// and how many.
type listPage struct {
	after *listCursor
	limit int
}

type listCursor struct {
	at time.Time
	id string
}

// cursorOf names a row by its time and id, the lists' order.
func cursorOf(at time.Time, id string) *string {
	cursor := strconv.FormatInt(at.UnixMilli(), 10) + "|" + id
	return &cursor
}

// listQuery reads a list's repository, cursor and limit.
func (t *Timeline) listQuery(w http.ResponseWriter, r *http.Request) (string, listPage, bool) {
	q := r.URL.Query()
	page := listPage{limit: TimelinePageSize}
	repository, err := t.allowed(r.Context(), q.Get("repository"))
	if err != nil {
		timelineError(w, err)
		return "", page, false
	}
	if raw := q.Get("cursor"); raw != "" {
		at, id, found := strings.Cut(raw, "|")
		ms, err := strconv.ParseInt(at, 10, 64)
		if !found || err != nil || id == "" {
			http.Error(w, "invalid cursor", http.StatusBadRequest)
			return "", page, false
		}
		page.after = &listCursor{at: time.UnixMilli(ms), id: id}
	}
	if raw := q.Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > TimelineMaxPage {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return "", page, false
		}
		page.limit = n
	}
	return repository, page, true
}

// multi reads a parameter given repeatedly or comma separated.
func multi(q map[string][]string, name string) []string {
	var out []string
	for _, value := range q[name] {
		for part := range strings.SplitSeq(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

// matchQuery is q as an FTS5 query: each word a quoted prefix, all of them
// required; "" when q has no word.
func matchQuery(q string) string {
	words := strings.FieldsFunc(q, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
	for i, word := range words {
		words[i] = `"` + word + `"*`
	}
	return strings.Join(words, " ")
}

// sessionID is a session's id in the Timeline: its agent and the agent's
// id, as the app's session page names it.
func sessionID(agent, id string) string { return agent + ":" + id }

// harness is the agent as the viewer names its harness.
func harness(agent string) string { return strings.ReplaceAll(agent, "-", "_") }

// cloneOf opens the repository's clone for one request.
func (t *Timeline) cloneOf(ctx context.Context, repository string) TimelineClone {
	if t.Clone == nil {
		return nil
	}
	return t.Clone(ctx, repository)
}

func personOf(ctx context.Context, clone TimelineClone) *Person {
	if clone == nil {
		return nil
	}
	return clone.Person(ctx)
}

func summaryRow(d Decision, person *Person) timelineDecision {
	turn := 0
	if len(d.Turns) > 0 {
		turn = slices.Min(d.Turns)
	}
	return timelineDecision{ID: d.ID, SessionID: sessionID(d.Agent, d.SessionID), Turn: turn, DecidedAt: d.DecidedAt, Who: string(d.Who),
		Person: person, Vendor: harness(d.Agent), Category: string(d.Category), Summary: d.Summary, Superseded: d.Superseded,
		FileCount: len(d.Files)}
}

// snapshotFeature is one feature of a snapshot, with what its detail reads.
type snapshotFeature struct {
	row          timelineFeature
	createdAt    time.Time
	digest       string
	matched      bool
	pulls        []snapshotPull
	sessions     []SessionKey
	ownSessions  []SessionKey
	decisions    []attributedDecision
	keyDecisions []keyDecision
}

type snapshotPull struct {
	number                 int
	title, state, head     string
	author                 string
	draft, merged, bot     bool
	createdAt, activeUntil time.Time
}

type attributedDecision struct {
	Decision
	// pull is the number of the feature's pull request it is shown under,
	// 0 for its session.
	pull int
}

// timelineSnapshot is a repository's features of its current version,
// those not merged into another, with their attributed decisions.
type timelineSnapshot struct {
	repository string
	features   map[string]*snapshotFeature
	branches   map[SessionKey]string
	clone      TimelineClone
	self       *Person
}

func (s *timelineSnapshot) ordered() []*snapshotFeature {
	list := make([]*snapshotFeature, 0, len(s.features))
	for _, f := range s.features {
		if f.matched {
			list = append(list, f)
		}
	}
	slices.SortFunc(list, featureOrder)
	return list
}

// featureOrder is the features list's: last activity (else creation)
// newest first, then id.
func featureOrder(a, b *snapshotFeature) int {
	return cmp.Or(lastActive(b).Compare(lastActive(a)), cmp.Compare(a.row.ID, b.row.ID))
}

func lastActive(f *snapshotFeature) time.Time {
	if f.row.LastActivityAt != nil {
		return f.row.LastActivityAt.Truncate(time.Millisecond)
	}
	return f.createdAt.Truncate(time.Millisecond)
}

// snapshot reads the repository's features, or with only that one. With q,
// those it does not match are read but not matched: the list leaves them
// out.
func (t *Timeline) snapshot(ctx context.Context, repository, q, only string) (*timelineSnapshot, error) {
	db := t.Store.db
	snap := &timelineSnapshot{repository: repository, features: map[string]*snapshotFeature{}, branches: map[SessionKey]string{}}
	version, err := liveVersion(ctx, db, repository, false, t.Store.now())
	if err != nil || version == 0 {
		return snap, err
	}
	// ?3 names the one feature read, '' every one.
	type featureRow struct {
		id, title, description, digest, keys string
		pending                              bool
		created                              int64
	}
	rows, err := collect(func(rows *sql.Rows) (featureRow, error) {
		var f featureRow
		return f, rows.Scan(&f.id, &f.title, &f.description, &f.digest, &f.keys, &f.pending, &f.created)
	})(db.QueryContext(ctx, `SELECT id, title, description, digest, key_decisions_json, summarized_version < membership_version, created_at
FROM feature WHERE version_id=?1 AND repository=?2 AND merged_into IS NULL AND (?3='' OR id=?3)`, version, repository, only))
	if err != nil {
		return nil, err
	}
	for _, f := range rows {
		feature := &snapshotFeature{createdAt: time.UnixMilli(f.created), digest: f.digest, matched: true}
		feature.row = timelineFeature{ID: f.id, People: []Person{}, KeyDecisions: []timelineKeyDecision{}}
		// Text not written yet reads as pending while the feature is
		// behind its members, else as missing; older text stays shown.
		if f.title != "" {
			feature.row.Title, feature.row.Description = &f.title, &f.description
		} else {
			feature.row.SummaryPending = f.pending
		}
		if err := json.Unmarshal([]byte(f.keys), &feature.keyDecisions); err != nil {
			return nil, err
		}
		snap.features[f.id] = feature
	}
	if len(snap.features) == 0 {
		return snap, nil
	}
	if strings.TrimSpace(q) != "" {
		number, _ := strconv.Atoi(strings.TrimPrefix(strings.TrimSpace(q), "#"))
		matched, err := collect(scanString)(db.QueryContext(ctx, `SELECT id FROM feature WHERE version_id=?1 AND repository=?2
AND (rowid IN (SELECT rowid FROM feature_search WHERE feature_search MATCH ?3)
 OR id IN (SELECT fp.feature_id FROM feature_pull fp JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
  WHERE fp.version_id=?1 AND fp.repository=?2 AND (p.number=?4 OR p.rowid IN (SELECT rowid FROM pull_search WHERE pull_search MATCH ?3))))`,
			version, repository, cmp.Or(matchQuery(q), `""`), number))
		if err != nil {
			return nil, err
		}
		for _, f := range snap.features {
			f.matched = slices.Contains(matched, f.row.ID)
		}
	}
	type pullRow struct {
		feature string
		pull    snapshotPull
		created int64
		updated int64
	}
	pulls, err := collect(func(rows *sql.Rows) (pullRow, error) {
		var p pullRow
		return p, rows.Scan(&p.feature, &p.pull.number, &p.pull.title, &p.pull.state, &p.pull.head, &p.pull.author, &p.pull.draft,
			&p.pull.merged, &p.pull.bot, &p.created, &p.updated)
	})(db.QueryContext(ctx, `SELECT fp.feature_id, p.number, p.title, p.state, p.head_sha, p.author, p.draft, p.merged_at IS NOT NULL, p.bot,
p.created_at, p.updated_at FROM feature_pull fp JOIN pull_request p ON p.repository=fp.repository AND p.number=fp.number
WHERE fp.version_id=?1 AND fp.repository=?2 AND (?3='' OR fp.feature_id=?3) ORDER BY p.created_at DESC, p.number DESC`, version, repository, only))
	if err != nil {
		return nil, err
	}
	for _, p := range pulls {
		if f := snap.features[p.feature]; f != nil {
			p.pull.createdAt, p.pull.activeUntil = time.UnixMilli(p.created), time.UnixMilli(p.updated)
			f.pulls = append(f.pulls, p.pull)
		}
	}
	// The repository's captured sessions only: a session captured under
	// another repository is not this Timeline's.
	type sessionRow struct {
		feature string
		key     SessionKey
		branch  string
		own     bool
	}
	sessions, err := collect(func(rows *sql.Rows) (sessionRow, error) {
		var s sessionRow
		return s, rows.Scan(&s.feature, &s.key.Agent, &s.key.ID, &s.branch, &s.own)
	})(db.QueryContext(ctx, `SELECT DISTINCT fp.feature_id, sc.agent, sc.session_id, cs.branch, 0 FROM feature_pull fp
JOIN pull_commit pc ON pc.repository=fp.repository AND pc.number=fp.number
JOIN session_commit sc ON sc.repository=pc.repository AND sc.sha=pc.sha
JOIN capture_session cs ON cs.agent=sc.agent AND cs.session_id=sc.session_id AND cs.repository=?2
WHERE fp.version_id=?1 AND fp.repository=?2 AND (?3='' OR fp.feature_id=?3)
UNION SELECT fs.feature_id, fs.agent, fs.session_id, cs.branch, 1 FROM feature_session fs
JOIN capture_session cs ON cs.agent=fs.agent AND cs.session_id=fs.session_id AND cs.repository=?2
WHERE fs.version_id=?1 AND fs.repository=?2 AND (?3='' OR fs.feature_id=?3) ORDER BY 2, 3, 1`, version, repository, only))
	if err != nil {
		return nil, err
	}
	var keys []SessionKey
	for _, s := range sessions {
		f := snap.features[s.feature]
		if f == nil {
			continue
		}
		if !slices.Contains(f.sessions, s.key) {
			f.sessions = append(f.sessions, s.key)
		}
		if s.own {
			f.ownSessions = append(f.ownSessions, s.key)
		}
		if _, seen := snap.branches[s.key]; !seen {
			keys = append(keys, s.key)
		}
		snap.branches[s.key] = s.branch
	}
	// Each session's decisions go to one feature each, as its text reads
	// them, and under that feature's pull request the attributor chooses.
	links, err := t.Store.sessionLinksOf(ctx, version, repository, keys)
	if err != nil {
		return nil, err
	}
	current, err := t.Store.currentOf(ctx, keys)
	if err != nil {
		return nil, err
	}
	for _, key := range keys {
		attributor := links[key].Attributor()
		for _, d := range current[key] {
			id := attributor.Attribute(d.Files)
			f := snap.features[id]
			if f == nil {
				continue
			}
			pull, _ := strconv.Atoi(attributor.PullRequest(id, d.Files))
			f.decisions = append(f.decisions, attributedDecision{Decision: d, pull: pull})
		}
	}
	snap.clone = t.cloneOf(ctx, repository)
	snap.self = personOf(ctx, snap.clone)
	for _, f := range snap.features {
		f.summarize(snap.self)
	}
	return snap, nil
}

// summarize fills the feature's row from its members.
func (f *snapshotFeature) summarize(self *Person) {
	// Key decisions resolve over the decisions as the feature's text reads
	// them: in session and extraction order, then earliest first.
	earliest := make([]Decision, len(f.decisions))
	for i, d := range f.decisions {
		earliest[i] = d.Decision
	}
	slices.SortStableFunc(earliest, func(a, b Decision) int { return a.DecidedAt.Compare(b.DecidedAt) })
	slices.SortStableFunc(f.decisions, func(a, b attributedDecision) int { return b.DecidedAt.Compare(a.DecidedAt) })
	var first, last time.Time
	active := func(at time.Time) {
		if first.IsZero() || at.Before(first) {
			first = at
		}
		if at.After(last) {
			last = at
		}
	}
	open, merged := 0, 0
	logins := map[string]bool{}
	if self != nil && len(f.sessions) > 0 {
		f.row.People = append(f.row.People, *self)
		if self.Login != nil {
			logins[strings.ToLower(*self.Login)] = true
		}
	}
	for _, p := range f.pulls {
		active(p.createdAt)
		active(p.activeUntil)
		switch {
		case p.merged:
			merged++
		case p.state == "open":
			open++
		}
		if p.author != "" && !p.bot && !logins[strings.ToLower(p.author)] {
			logins[strings.ToLower(p.author)] = true
			login := p.author
			// A pull request author has no address the store knows; the
			// login's GitHub address keeps people apart.
			f.row.People = append(f.row.People, Person{Email: login + "@users.noreply.github.com", Login: &login})
		}
	}
	for _, d := range f.decisions {
		active(d.DecidedAt)
	}
	if !first.IsZero() {
		f.row.FirstActivityAt, f.row.LastActivityAt = &first, &last
	}
	f.row.Status = features.DeriveStatus(len(f.pulls), open, merged)
	f.row.PullRequestCount, f.row.SessionCount, f.row.DecisionCount = len(f.pulls), len(f.sessions), len(f.decisions)
	for _, key := range f.keyDecisions {
		current, ok := currentKeys([]keyDecision{key}, earliest)
		if !ok {
			continue
		}
		i := slices.IndexFunc(earliest, func(d Decision) bool { return d.ID == current[0].ID })
		row := summaryRow(earliest[i], nil)
		f.row.KeyDecisions = append(f.row.KeyDecisions, timelineKeyDecision{ID: row.ID, SessionID: row.SessionID, Turn: row.Turn,
			Who: row.Who, Category: row.Category, Summary: row.Summary})
	}
}

// feature is a feature's detail, resolved by Store.ResolveFeature: one
// merged into another answers with the survivor's, one of a retired
// version as archived, one of a version being built is not found.
func (t *Timeline) feature(ctx context.Context, id string) (any, error) {
	resolved, err := t.Store.ResolveFeature(ctx, id)
	if errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrMergeChain) {
		return nil, errTimelineNotFound
	}
	if err != nil {
		return nil, err
	}
	repository := resolved.Repository
	if _, err := t.allowed(ctx, repository); err != nil {
		if errors.Is(err, errTimelineRepository) {
			return nil, errTimelineNotFound
		}
		return nil, err
	}
	if resolved.Archived {
		return map[string]bool{"archived": true}, nil
	}
	id = resolved.ID
	snap, err := t.snapshot(ctx, repository, "", id)
	if err != nil {
		return nil, err
	}
	f := snap.features[id]
	if f == nil {
		return nil, errTimelineNotFound
	}
	type featureRow struct {
		timelineFeature
		Digest *string `json:"digest"`
	}
	row := featureRow{timelineFeature: f.row}
	if f.digest != "" {
		row.Digest = &f.digest
	}
	pulls := []timelinePull{}
	for _, p := range f.pulls {
		pull := pullOf(ctx, snap.clone, p)
		for _, d := range f.decisions {
			if d.pull == p.number {
				pull.Decisions = append(pull.Decisions, summaryRow(d.Decision, snap.self))
			}
		}
		pulls = append(pulls, pull)
	}
	sessions := []timelineSession{}
	for _, key := range f.sessions {
		var held []timelineDecision
		for _, d := range f.decisions {
			if d.pull == 0 && d.Agent == key.Agent && d.SessionID == key.ID {
				held = append(held, summaryRow(d.Decision, snap.self))
			}
		}
		if len(held) == 0 && !slices.Contains(f.ownSessions, key) {
			continue
		}
		session := sessionOf(key, snap.branches[key], snap.self)
		session.Decisions = append(session.Decisions, held...)
		sessions = append(sessions, session)
	}
	if err := t.pullSummaries(ctx, repository, pulls); err != nil {
		return nil, err
	}
	return map[string]any{"feature": row, "pull_requests": pulls, "sessions": sessions}, nil
}

func sessionOf(key SessionKey, branch string, person *Person) timelineSession {
	return timelineSession{ID: sessionID(key.Agent, key.ID), Person: person, Vendor: harness(key.Agent), Branch: branch,
		Decisions: []timelineDecision{}}
}

// pullOf is a pull request as a detail lists it, with its saved review's
// summary when there is one.
func pullOf(ctx context.Context, clone TimelineClone, p snapshotPull) timelinePull {
	state := "closed"
	if p.state == "open" && !p.merged {
		state = "open"
	}
	pull := timelinePull{Number: p.number, Title: p.title, State: state, Draft: p.draft, Merged: p.merged, ReviewState: "none",
		Decisions: []timelineDecision{}}
	if clone != nil {
		summary, review := clone.Review(ctx, p.number, p.head)
		pull.ReviewState = cmp.Or(review, "none")
		if summary != "" {
			pull.Summary = &summary
		}
	}
	return pull
}

// pullSummaries gives the pull requests without a saved review's summary
// their generated one.
func (t *Timeline) pullSummaries(ctx context.Context, repository string, pulls []timelinePull) error {
	var numbers []int
	for _, p := range pulls {
		if p.Summary == nil {
			numbers = append(numbers, p.Number)
		}
	}
	if len(numbers) == 0 {
		return nil
	}
	generated, err := t.Store.pullSummaries(ctx, repository, numbers)
	if err != nil {
		return err
	}
	for i := range pulls {
		if g, ok := generated[pulls[i].Number]; ok && pulls[i].Summary == nil && g.Summary != "" {
			text := g.Summary
			pulls[i].Summary = &text
		}
	}
	return nil
}

// decisions is a page of the repository's sessions' current decisions,
// newest first, with the coverage of its sessions.
func (t *Timeline) decisions(ctx context.Context, repository string, q map[string][]string, page listPage) (map[string]any, error) {
	get := func(name string) string {
		if values := q[name]; len(values) > 0 {
			return strings.TrimSpace(values[0])
		}
		return ""
	}
	where := []string{"d.obsolete_at IS NULL", "cs.repository=?"}
	args := []any{repository}
	if match := matchQuery(get("q")); match != "" {
		where = append(where, "d.rowid IN (SELECT rowid FROM decision_search WHERE decision_search MATCH ?)")
		args = append(args, match)
	}
	if file := strings.TrimSuffix(get("file"), "/"); file != "" {
		where = append(where, `EXISTS (SELECT 1 FROM decision_file f WHERE f.decision_id=d.decision_id AND (f.path=? OR f.path LIKE ? ESCAPE '\'))`)
		args = append(args, file, strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(file)+"/%")
	}
	for _, column := range []string{"who", "category"} {
		if values := multi(q, column); len(values) > 0 {
			where = append(where, "d."+column+" IN (?"+strings.Repeat(",?", len(values)-1)+")")
			for _, v := range values {
				args = append(args, v)
			}
		}
	}
	if superseded := get("superseded"); superseded != "1" && superseded != "true" {
		where = append(where, "d.superseded=0")
	}
	for _, bound := range []struct{ name, op string }{{"from", ">="}, {"to", "<"}} {
		if raw := get(bound.name); raw != "" {
			day, err := time.ParseInLocation(time.DateOnly, raw, time.Local)
			if err != nil {
				return nil, errTimelineQuery
			}
			where = append(where, "d.decided_at "+bound.op+" ?")
			args = append(args, day.UnixMilli())
		}
	}
	if page.after != nil {
		where = append(where, "(d.decided_at < ? OR (d.decided_at = ? AND d.decision_id > ?))")
		args = append(args, page.after.at.UnixMilli(), page.after.at.UnixMilli(), page.after.id)
	}
	db := t.Store.db
	type row struct {
		agent, session, id string
		decision           timelineDecision
		decided            int64
	}
	rows, err := collect(func(rows *sql.Rows) (row, error) {
		var r row
		err := rows.Scan(&r.id, &r.agent, &r.session, &r.decision.Turn, &r.decided, &r.decision.Who, &r.decision.Category,
			&r.decision.Summary, &r.decision.Superseded, &r.decision.FileCount)
		return r, err
	})(db.QueryContext(ctx, `SELECT d.decision_id, d.agent, d.session_id, d.first_turn, d.decided_at, d.who, d.category, d.summary, d.superseded,
(SELECT count(*) FROM decision_file f WHERE f.decision_id=d.decision_id)
FROM decision d JOIN capture_session cs ON cs.agent=d.agent AND cs.session_id=d.session_id
WHERE `+strings.Join(where, " AND ")+` ORDER BY d.decided_at DESC, d.decision_id LIMIT ?`,
		append(args, page.limit+1)...))
	if err != nil {
		return nil, err
	}
	self := personOf(ctx, t.cloneOf(ctx, repository))
	list := []timelineDecision{}
	for _, r := range rows[:min(len(rows), page.limit)] {
		d := r.decision
		d.ID, d.SessionID, d.Vendor, d.DecidedAt, d.Person = r.id, sessionID(r.agent, r.session), harness(r.agent), time.UnixMilli(r.decided), self
		list = append(list, d)
	}
	var next *string
	if len(rows) > page.limit {
		last := list[len(list)-1]
		next = cursorOf(last.DecidedAt, last.ID)
	}
	coverage, err := t.coverage(ctx, repository)
	if err != nil {
		return nil, err
	}
	return map[string]any{"decisions": list, "next_cursor": next, "order": "newest", "coverage": coverage, "features_enabled": true}, nil
}

type timelineCoverage struct {
	Extracted       int            `json:"extracted"`
	Pending         int            `json:"pending"`
	Skipped         int            `json:"skipped"`
	SkippedByReason map[string]int `json:"skipped_by_reason"`
}

// coverage counts the repository's sessions by capture outcome; pending
// are those being read now.
func (t *Timeline) coverage(ctx context.Context, repository string) (timelineCoverage, error) {
	c := timelineCoverage{SkippedByReason: map[string]int{}}
	type count struct {
		coverage, reason string
		n                int
	}
	counts, err := collect(func(rows *sql.Rows) (count, error) {
		var c count
		return c, rows.Scan(&c.coverage, &c.reason, &c.n)
	})(t.Store.db.QueryContext(ctx, `SELECT coverage, skip_reason, count(*) FROM capture_session WHERE repository=? GROUP BY 1, 2`, repository))
	if err != nil {
		return c, err
	}
	for _, each := range counts {
		switch each.coverage {
		case CoverageExtracted:
			c.Extracted += each.n
		case CoverageSkipped:
			c.Skipped += each.n
			c.SkippedByReason[cmp.Or(each.reason, "unknown")] += each.n
		}
	}
	err = t.Store.db.QueryRowContext(ctx, `SELECT count(*) FROM (SELECT DISTINCT agent, session_id FROM capture_attempt
WHERE repository=? AND state=? AND stage=?)`, repository, AttemptPending, StageCapture).Scan(&c.Pending)
	return c, err
}

// decision is a current decision's detail: its session's current
// decisions, never its prompts, and the pull requests its session's
// commits are in.
func (t *Timeline) decision(ctx context.Context, id string) (any, error) {
	db := t.Store.db
	var key SessionKey
	var repository, branch string
	err := db.QueryRowContext(ctx, `SELECT d.agent, d.session_id, cs.repository, cs.branch FROM decision d
JOIN capture_session cs ON cs.agent=d.agent AND cs.session_id=d.session_id WHERE d.decision_id=? AND d.obsolete_at IS NULL`, id).
		Scan(&key.Agent, &key.ID, &repository, &branch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errTimelineNotFound
	}
	if err != nil {
		return nil, err
	}
	if _, err := t.allowed(ctx, repository); err != nil {
		if errors.Is(err, errTimelineRepository) {
			return nil, errTimelineNotFound
		}
		return nil, err
	}
	current, err := t.Store.Current(ctx, key)
	if err != nil {
		return nil, err
	}
	type detailDecision struct {
		ID                  string    `json:"id"`
		Turn                int       `json:"turn"`
		Turns               []int     `json:"turns"`
		DecidedAt           time.Time `json:"decided_at"`
		Who                 string    `json:"who"`
		Category            string    `json:"category"`
		Summary             string    `json:"summary"`
		RejectedAlternative *string   `json:"rejected_alternative"`
		Superseded          bool      `json:"superseded"`
		Files               []string  `json:"files"`
	}
	list := make([]detailDecision, 0, len(current))
	for _, d := range current {
		row := summaryRow(d, nil)
		each := detailDecision{ID: d.ID, Turn: row.Turn, Turns: d.Turns, DecidedAt: d.DecidedAt, Who: row.Who, Category: row.Category,
			Summary: d.Summary, Superseded: d.Superseded, Files: d.Files}
		if d.RejectedAlternative != "" {
			each.RejectedAlternative = &d.RejectedAlternative
		}
		list = append(list, each)
	}
	pulls, err := collect(func(rows *sql.Rows) (snapshotPull, error) {
		var p snapshotPull
		return p, rows.Scan(&p.number, &p.title, &p.state, &p.head, &p.draft, &p.merged)
	})(db.QueryContext(ctx, `SELECT DISTINCT p.number, p.title, p.state, p.head_sha, p.draft, p.merged_at IS NOT NULL FROM session_commit sc
JOIN pull_commit pc ON pc.repository=sc.repository AND pc.sha=sc.sha
JOIN pull_request p ON p.repository=pc.repository AND p.number=pc.number
WHERE sc.agent=? AND sc.session_id=? AND sc.repository=? ORDER BY p.number DESC`, key.Agent, key.ID, repository))
	if err != nil {
		return nil, err
	}
	clone := t.cloneOf(ctx, repository)
	listed := make([]timelinePull, len(pulls))
	for i, p := range pulls {
		listed[i] = pullOf(ctx, clone, p)
		listed[i].Summary = nil
	}
	return map[string]any{
		"session":   sessionOf(key, branch, personOf(ctx, clone)),
		"decisions": list,
		// The Timeline serves no transcript text.
		"turns":            []any{},
		"prompts_withheld": true,
		"pull_requests":    listed,
	}, nil
}

// LocalClones is a Timeline's Clone: the first available clone of the
// picked repository, whose git user.email names this machine's sessions
// and whose review store holds its saved reviews.
func LocalClones(registry TimelineRegistry) func(context.Context, string) TimelineClone {
	return func(ctx context.Context, repository string) TimelineClone {
		list, err := registry.Repositories(ctx)
		if err != nil {
			return nil
		}
		for _, r := range list {
			if !strings.EqualFold(r.Repository, repository) {
				continue
			}
			for _, c := range r.Clones {
				if c.Available {
					return &localClone{clone: c}
				}
			}
		}
		return nil
	}
}

// localClone reads its clone's email and review store at most once.
type localClone struct {
	clone        repos.Clone
	personOnce   sync.Once
	person       *Person
	versionsOnce sync.Once
	versions     *store.Store
}

func (c *localClone) Person(ctx context.Context) *Person {
	c.personOnce.Do(func() {
		repo, err := gitlocal.Open(ctx, c.clone.Path, gitlocal.Options{})
		if err != nil {
			return
		}
		defer func() { _ = repo.Close() }()
		if email, err := repo.UserEmail(ctx); err == nil && email != "" {
			c.person = &Person{Email: email}
		}
	})
	return c.person
}

func (c *localClone) Review(ctx context.Context, number int, head string) (string, string) {
	c.versionsOnce.Do(func() {
		if versions, err := store.Open(c.clone.CommonDir, "", false); err == nil {
			c.versions = versions
		}
	})
	if c.versions == nil {
		return "", "none"
	}
	latest, err := c.versions.Latest(ctx, store.Key{Repository: c.clone.Repository, Number: number})
	if err != nil {
		return "", "none"
	}
	var doc struct {
		Summary struct {
			Text string `json:"text"`
		} `json:"summary"`
	}
	_ = json.Unmarshal(latest.Document, &doc)
	state := "outdated"
	if latest.Metadata.Head == head {
		state = "current"
	}
	return strings.TrimSpace(doc.Summary.Text), state
}

// timelineBuild is a first build's version in progress: its pull requests
// placed of the total, why it stopped ("" while it may go on) and what it
// spent over all its runs.
type timelineBuild struct {
	Placed   int     `json:"placed"`
	Total    int     `json:"total"`
	Stopped  string  `json:"stopped"`
	SpentUSD float64 `json:"spent_usd"`
}

// build is the repository's building version, nil without one.
func (t *Timeline) build(ctx context.Context, repository string) (*timelineBuild, error) {
	var b timelineBuild
	err := t.Store.db.QueryRowContext(ctx, `SELECT placed, total, stopped, spent_usd FROM feature_version WHERE repository=? AND state=?`,
		repository, VersionBuilding).Scan(&b.Placed, &b.Total, &b.Stopped, &b.SpentUSD)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return &b, err
}

// current reports whether the repository's current version has a feature
// not merged into another, whatever a list's filters, and when it became
// current (nil without one).
func (t *Timeline) current(ctx context.Context, repository string) (bool, *time.Time, error) {
	var found bool
	var switched sql.NullInt64
	err := t.Store.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM feature f WHERE f.version_id=v.id AND f.merged_into IS NULL),
COALESCE(v.switched_at, v.created_at) FROM feature_version v WHERE v.repository=? AND v.state=?`, repository, VersionCurrent).Scan(&found, &switched)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil, nil
	}
	if err != nil || !switched.Valid {
		return found, nil, err
	}
	since := time.UnixMilli(switched.Int64)
	return found, &since, nil
}
