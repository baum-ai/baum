// Package seeddata writes a large, invented, deterministic dataset into a
// home directory, for profiling the desktop app against data that is the
// same on every machine: repositories with hundreds of open pull requests
// and their saved reviews, one with a 5,000-line diff, long Claude Code and
// Codex transcripts and twenty saved inboxes. The real daemon then serves
// it with no GitHub and no model (see README.md).
//
// Everything is written through the stores the daemon reads, as the tour's
// sample project (internal/sampleapp) is. The same Options give the same
// data: names, contents, commits, review documents, inboxes and
// transcripts come from a PRNG seeded with Options.Seed, and every time is
// counted from Options.Epoch, never from the clock.
package seeddata

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/baum-ai/baum/internal/daemon"
	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/hookevents"
	"github.com/baum-ai/baum/internal/inboxes"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/uistate"
	"github.com/baum-ai/baum/review"
)

// Token is the GitHub credential the dataset's configuration names. It is
// not a real token: the discovery cache is partitioned by credential, so
// the daemon reads the seeded cache only under this one.
const Token = "baum-seeddata-offline"

// DefaultEpoch is the instant the dataset's times are counted back from.
var DefaultEpoch = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

// Options size the dataset. Zero fields take the defaults (Defaults).
type Options struct {
	Seed int64
	// Home is the home directory written into; it must hold no baum data.
	Home string
	// Epoch is "now" for every time in the dataset.
	Epoch time.Time
	// SessionsAt is when the agent sessions' hooks recorded their start and
	// end. The Sessions page lists an ended session for a day after its
	// end, so a run that wants them listed passes the current time; the
	// default is Epoch.
	SessionsAt          time.Time
	Repositories        int
	PullRequests        int
	LargeDiffLines      int
	Inboxes             int
	ClaudeSessions      int
	CodexSessions       int
	LongTranscriptTurns int
	DraftComments       int
}

// Defaults are the sizes the profiling recipe uses.
func Defaults() Options {
	return Options{Seed: 1, Epoch: DefaultEpoch, Repositories: 24, PullRequests: 320, LargeDiffLines: 5000, Inboxes: 20,
		ClaudeSessions: 8, CodexSessions: 4, LongTranscriptTurns: 3000, DraftComments: 12}
}

func (o Options) withDefaults() Options {
	d := Defaults()
	if o.Epoch.IsZero() {
		o.Epoch = d.Epoch
	}
	if o.SessionsAt.IsZero() {
		o.SessionsAt = o.Epoch
	}
	for _, f := range []struct{ v, d *int }{{&o.Repositories, &d.Repositories}, {&o.PullRequests, &d.PullRequests}, {&o.LargeDiffLines, &d.LargeDiffLines},
		{&o.Inboxes, &d.Inboxes}, {&o.ClaudeSessions, &d.ClaudeSessions}, {&o.CodexSessions, &d.CodexSessions},
		{&o.LongTranscriptTurns, &d.LongTranscriptTurns}, {&o.DraftComments, &d.DraftComments}} {
		if *f.v <= 0 {
			*f.v = *f.d
		}
	}
	o.Inboxes = min(o.Inboxes, 20)
	return o
}

// Counts are what a dataset holds.
type Counts struct {
	Repositories         int `json:"repositories"`
	PullRequests         int `json:"pull_requests"`
	ReviewedPullRequests int `json:"reviewed_pull_requests"`
	ReviewVersions       int `json:"review_versions"`
	LargestDiffLines     int `json:"largest_diff_lines"`
	// LargeDiff names the pull request sized by Options.LargeDiffLines, and
	// LargeDiffLines is its diff's changed lines.
	LargeDiff         string `json:"large_diff"`
	LargeDiffLines    int    `json:"large_diff_lines"`
	Inboxes           int    `json:"inboxes"`
	DraftComments     int    `json:"draft_comments"`
	Sessions          int    `json:"sessions"`
	LongestTranscript int    `json:"longest_transcript_turns"`
	TranscriptBytes   int64  `json:"transcript_bytes"`
}

// Result is what Generate wrote: the counts and a digest of the content,
// which equal options give again.
type Result struct {
	Seed   int64  `json:"seed"`
	Counts Counts `json:"counts"`
	// Digest is a SHA-256 over Manifest.
	Digest string `json:"digest"`
	// Manifest names each part of the content with its digest: commits by
	// SHA, review documents, discovery snapshots, inboxes and transcripts.
	Manifest []string `json:"manifest"`
}

// MarkerFile is the file Generate leaves in the home, relative to it: the
// result as JSON. isolated.sh reads its presence to keep the instance
// offline.
const MarkerFile = ".local/state/baum/seeddata.json"

// owned are the parts of a home Generate writes. One that did not exist
// before is removed whole when generation fails; inside one that did (a
// .claude folder Claude Code made, say), only what Generate wrote is.
var owned = []string{".local/state/baum", ".config/baum", ".config/com.usebaum.desktop", "src", ".claude", ".codex"}

// Generate writes the dataset into o.Home. The marker file (MarkerFile) is
// written last; when generation fails, what it wrote is removed, so the
// home holds either the whole dataset or no baum data.
func Generate(ctx context.Context, o Options) (Result, error) {
	o = o.withDefaults()
	if o.Home == "" || !filepath.IsAbs(o.Home) {
		return Result{}, errors.New("seeddata: the home must be an absolute path")
	}
	for _, dir := range []string{".local/state/baum", ".config/baum"} {
		if _, err := os.Stat(filepath.Join(o.Home, dir)); err == nil {
			return Result{}, fmt.Errorf("seeddata: %s already holds baum data", filepath.Join(o.Home, dir))
		} else if !errors.Is(err, fs.ErrNotExist) {
			return Result{}, err
		}
	}
	home, err := filepath.EvalSymlinks(o.Home)
	if err != nil {
		return Result{}, err
	}
	var fresh []string
	for _, part := range owned {
		if _, err := os.Lstat(filepath.Join(home, part)); errors.Is(err, fs.ErrNotExist) {
			fresh = append(fresh, filepath.Join(home, part))
		}
	}
	var written []string
	result, err := writeDataset(ctx, o, home, func(path string) { written = append(written, path) })
	if err != nil {
		for _, path := range append(fresh, written...) {
			_ = os.RemoveAll(path)
		}
	}
	return result, err
}

// claim refuses a path something else already holds, so a failed
// generation never removes what it did not write, and tells wrote of one
// that is free.
func claim(path string, wrote func(string)) error {
	if _, err := os.Lstat(path); err == nil {
		return fmt.Errorf("seeddata: %s already exists", path)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	wrote(path)
	return nil
}

// writeDataset writes the dataset into home, telling wrote each clone and
// transcript it writes.
func writeDataset(ctx context.Context, o Options, home string, wrote func(string)) (Result, error) {
	state, config := filepath.Join(home, ".local", "state", "baum"), filepath.Join(home, ".config", "baum")
	for _, dir := range []string{state, config} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return Result{}, err
		}
	}
	appDB := filepath.Join(state, "app.sqlite")
	var manifest []string
	note := func(format string, args ...any) { manifest = append(manifest, fmt.Sprintf(format, args...)) }
	counts := Counts{}

	// The configuration: the offline credential, and automatic reviews off
	// so nothing asks a model.
	if err := writePrivate(filepath.Join(config, "config.toml"), fmt.Sprintf("github_token = %q\n", Token)); err != nil {
		return Result{}, err
	}
	// The desktop app's first-run setup is done, replacing any settings an
	// earlier run of the app left.
	desktop := filepath.Join(home, ".config", "com.usebaum.desktop")
	if err := os.MkdirAll(desktop, 0o700); err != nil {
		return Result{}, err
	}
	if err := os.WriteFile(filepath.Join(desktop, "desktop.json"), []byte(`{"setup":{"onboarded":true,"commands":[]}}`+"\n"), 0o600); err != nil {
		return Result{}, err
	}
	registry, err := repos.Open(appDB)
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = registry.Close() }()
	if _, err := registry.UpdatePreferences(ctx, func(p repos.Preferences) repos.Preferences {
		p.Automatic = false
		return p
	}); err != nil {
		return Result{}, err
	}

	plans := plan(o)
	validator, err := review.NewDocumentValidator()
	if err != nil {
		return Result{}, err
	}
	client := github.New(func(context.Context, string) (string, error) { return Token, nil }, github.Options{})
	disc := discovery.New(client, state)
	clones := make([]string, len(plans))
	for i, r := range plans {
		dir := filepath.Join(home, "src", r.owner, r.name)
		if err := claim(dir, wrote); err != nil {
			return Result{}, err
		}
		g, err := writeRepository(ctx, o, r, dir)
		if err != nil {
			return Result{}, fmt.Errorf("writing %s: %w", r.identity(), err)
		}
		clone, err := registry.Add(ctx, dir, nil)
		if err != nil {
			return Result{}, fmt.Errorf("registering %s: %w", r.identity(), err)
		}
		clones[i] = clone.Path
		stats, err := writeReviews(ctx, o, r, g, clone.CommonDir, validator, &counts, note)
		if err != nil {
			return Result{}, err
		}
		note("repo %s main %s", r.identity(), g.main)
		snapshot := discovery.Snapshot{Repository: r.identity(), Viewer: viewer, PullRequests: []discovery.PullRequest{},
			FetchedAt: o.Epoch, NextPoll: time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)}
		for _, pr := range r.pullRequests {
			heads := g.heads[pr.number]
			note("pr %s heads %s", pr.key(), strings.Join(heads, ","))
			snapshot.PullRequests = append(snapshot.PullRequests, pullRequest(o, pr, heads[len(heads)-1], stats[pr.number]))
			counts.PullRequests++
		}
		if err := disc.Seed(ctx, snapshot); err != nil {
			return Result{}, fmt.Errorf("seeding discovery for %s: %w", r.identity(), err)
		}
		raw, _ := json.Marshal(snapshot)
		note("discovery %s %s", r.identity(), digest(raw))
		counts.Repositories++
	}

	inboxDoc, err := writeInboxes(o, appDB, plans)
	if err != nil {
		return Result{}, err
	}
	counts.Inboxes = o.Inboxes
	note("inboxes %s", digest(inboxDoc))
	uiState := &uistate.Store{Path: appDB, Now: func() time.Time { return o.Epoch }}
	// The first-run tour would cover the page.
	if _, err := uiState.Replace("tour", "", 0, "seeddata-tour:1", json.RawMessage(`{"seen":true}`)); err != nil {
		return Result{}, err
	}
	drafts, err := writeDraftComments(o, uiState, plans)
	if err != nil {
		return Result{}, err
	}
	counts.DraftComments = drafts
	note("drafts %d", drafts)

	sessions := planSessions(o, plans, clones)
	for _, s := range sessions {
		write := writeClaudeTranscript
		if s.agent == "codex" {
			write = writeCodexRollout
		}
		size, err := write(o, home, s)
		// A transcript already there is someone else's: never removed.
		if !errors.Is(err, fs.ErrExist) {
			wrote(s.path)
		}
		if err != nil {
			return Result{}, err
		}
		raw, err := os.ReadFile(s.path)
		if err != nil {
			return Result{}, err
		}
		note("session %s %s turns %d %s", s.agent, s.id, s.turns, digest(raw))
		counts.Sessions++
		counts.TranscriptBytes += size
		counts.LongestTranscript = max(counts.LongestTranscript, s.turns)
	}
	paths := daemon.Paths{State: filepath.Join(state, "daemon")}
	if err := writeSessionEvents(hookevents.RecordDir(paths), sessions, o.SessionsAt); err != nil {
		return Result{}, err
	}

	sum := sha256.Sum256([]byte(strings.Join(manifest, "\n")))
	result := Result{Seed: o.Seed, Counts: counts, Digest: hex.EncodeToString(sum[:]), Manifest: manifest}
	raw, _ := json.MarshalIndent(result, "", "  ")
	if err := writePrivate(filepath.Join(home, MarkerFile), string(raw)+"\n"); err != nil {
		return Result{}, err
	}
	return result, nil
}

// pullRequest is pr as discovery lists it.
func pullRequest(o Options, pr *prPlan, head string, s diffStat) discovery.PullRequest {
	yes := true
	unresolved := pr.unresolved
	return discovery.PullRequest{Repository: pr.repo.identity(), Number: pr.number, HeadSHA: head, HeadRef: pr.branch, HeadRepository: pr.repo.identity(),
		BaseRef: "main", Additions: s.additions, Deletions: s.deletions, ChangedFiles: s.files, Title: pr.title,
		URL: fmt.Sprintf("https://github.com/%s/pull/%d", pr.repo.identity(), pr.number), Author: pr.author, AuthorBot: pr.bot,
		State: "OPEN", Draft: pr.draft, AuthoredByMe: pr.author == viewer, ReviewRequestedFromMe: pr.requested, ReviewRequested: pr.requested,
		AssignedToMe: pr.assigned, Assigned: pr.assigned, AuthorHasWrite: &yes, ViewerCanPush: &yes, TeamMembershipKnown: true,
		Review: pr.review, Checks: pr.checks, Merge: pr.merge, Unresolved: &unresolved,
		UpdatedAt: o.Epoch.Add(-time.Duration(pr.updated) * time.Minute)}
}

// writeInboxes saves o.Inboxes inboxes with fixed ids: the default one,
// one per busy repository and owner, and filters on what waits on whom.
func writeInboxes(o Options, appDB string, plans []*repoPlan) ([]byte, error) {
	colors := []string{"blue", "green", "red", "amber", "purple", "grey"}
	list := []inboxes.Inbox{
		{Name: "Default", Query: "author=@me OR assignee=@me OR review-requested=@me"},
		{Name: "Waiting on me", Query: "waiting=review"},
		{Name: "Ready to merge", Query: "waiting=merge"},
		{Name: "Mine", Query: "author=@me"},
		{Name: "Drafts", Query: "is=draft"},
		{Name: "Failing checks", Query: "checks=failing"},
		{Name: "Approved", Query: "review=approved checks=passing"},
		{Name: "Bots", Query: "is=bot"},
		{Name: "Not bots", Query: "is!=bot"},
	}
	ownersSeen := map[string]bool{}
	for _, r := range plans {
		if !ownersSeen[r.owner] {
			ownersSeen[r.owner] = true
			list = append(list, inboxes.Inbox{Name: "Org " + r.owner, Owners: []string{r.owner}})
		}
	}
	for _, r := range plans {
		list = append(list, inboxes.Inbox{Name: r.identity(), Repositories: []string{r.identity()}})
	}
	list = list[:min(o.Inboxes, len(list))]
	for i := range list {
		sum := sha256.Sum256([]byte(fmt.Sprintf("seeddata inbox %d %d", o.Seed, i)))
		list[i].ID = hex.EncodeToString(sum[:16])
		list[i].Color = colors[i%len(colors)]
		list[i].What, list[i].Risk = "any", "any"
		if list[i].Owners == nil {
			list[i].Owners = []string{}
		}
		if list[i].Repositories == nil {
			list[i].Repositories = []string{}
		}
		list[i].Badge = i < 4
	}
	saved := inboxes.Open(appDB)
	current, err := saved.Read()
	if err != nil {
		return nil, err
	}
	state, err := saved.Replace(current.Revision, list)
	if err != nil {
		return nil, err
	}
	return json.Marshal(state.Inboxes)
}

// writeDraftComments leaves review comments the person has started on
// some reviewed pull requests, as the review page keeps them.
func writeDraftComments(o Options, ui *uistate.Store, plans []*repoPlan) (int, error) {
	written := 0
	for _, r := range plans {
		for _, pr := range r.pullRequests {
			if written >= o.DraftComments || pr.reviewed == 0 || pr.number%3 != 0 {
				continue
			}
			var comments []any
			for i := 0; i < 1+pr.number%4; i++ {
				comments = append(comments, map[string]any{"id": fmt.Sprintf("seed%d", i), "target": map[string]any{"kind": "general", "id": "", "title": ""},
					"body": fmt.Sprintf("Draft note %d: check how %s behaves with an empty %s.", i+1, functions[i%len(functions)], identifiers[(pr.number+i)%len(identifiers)]),
					"head": "", "created": o.Epoch.Unix() - int64(i*600)})
			}
			raw, _ := json.Marshal(map[string]any{"comments": comments, "note": ""})
			if _, err := ui.Replace("review", pr.key(), 0, "seeddata-drafts:1", raw); err != nil {
				return written, fmt.Errorf("draft comments on %s: %w", pr.key(), err)
			}
			written++
		}
	}
	return written, nil
}

func digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:12])
}
