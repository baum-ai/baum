package sampleapp

import (
	"context"
	"time"

	"github.com/baum-ai/baum/internal/decisionlog"
)

// summaries are the sample's pull requests in a sentence, as the feature
// log writes them.
var summaries = map[int]string{
	newestFirst.number: "Lists notes newest first.",
	tags.number:        "Adds tags to notes and a way to list the notes with one.",
	accents.number:     "Makes search ignore accents as well as case.",
	readme.number:      "Explains in the README how search matches.",
	sortByEdit.number:  "Orders the list by each note's last edit.",
}

// features are what the sample's pull requests and sessions build, as the
// feature log would have grouped them.
func features(now time.Time) []decisionlog.SampleFeature {
	session := func(i int) string { return agentSessions[i].id }
	return []decisionlog.SampleFeature{
		{ID: "feature-search", Title: "Search", Updated: now.Add(-40 * time.Minute),
			Description:  "Find notes by the words in their title or text, whatever their case or accents.",
			Digest:       "Search matches words with or without accents, with a short explanation in the README.",
			Pulls:        []int{accents.number, readme.number},
			KeyDecisions: []decisionlog.SampleDecisionRef{{ID: "dec-readme", Agent: "claude-code", Session: session(1), Turn: 1}}},
		{ID: "feature-order", Title: "Note order", Updated: now.Add(-20 * time.Minute),
			Description: "Which notes come first in the list.",
			Digest:      "The list shows the newest notes first and is moving to the most recently edited.",
			Pulls:       []int{newestFirst.number, sortByEdit.number},
			KeyDecisions: []decisionlog.SampleDecisionRef{{ID: "dec-newest", Agent: "claude-code", Session: session(6), Turn: 1},
				{ID: "dec-edited", Agent: "claude-code", Session: session(0), Turn: 2}}},
		{ID: "feature-tags", Title: "Tags", Updated: now.Add(-5 * time.Hour),
			Description: "Label notes and list the notes with one label.",
			Digest:      "Notes carry tags, cleaned when saved, and can be filtered by one.",
			Pulls:       []int{tags.number}, Sessions: []string{session(3), session(2)},
			KeyDecisions: []decisionlog.SampleDecisionRef{{ID: "dec-clean", Agent: "claude-code", Session: session(3), Turn: 2},
				{ID: "dec-tags-index", Agent: "claude-code", Session: session(3), Turn: 3},
				{ID: "dec-tags-one-rule", Agent: "claude-code", Session: session(2), Turn: 1}}},
	}
}

// linkSessions records each pull request's head commit and the sessions
// that wrote it, as the feature log links them, so a feature counts the
// sessions and decisions of its pull requests.
func linkSessions(ctx context.Context, p project, decided *decisionlog.Store, now time.Time) error {
	var shas []string
	var links []decisionlog.SessionCommit
	for _, pl := range append([]pull{newestFirst}, openPulls...) {
		head := p.heads[pl.number]
		var paths []string
		for _, c := range pl.commits {
			for path := range c.files {
				paths = append(paths, path)
			}
		}
		commits := []decisionlog.PullCommit{{SHA: head, CommittedAt: now.Add(-pl.updated)}}
		if err := decided.SetPullPaths(ctx, repository, pl.number, head, paths, commits); err != nil {
			return err
		}
		shas = append(shas, head)
		for _, s := range agentSessions {
			if s.pr == pl.number {
				links = append(links, decisionlog.SessionCommit{Agent: "claude-code", SessionID: s.id, SHA: head, Method: "checkpoint", Confidence: 1})
			}
		}
	}
	return decided.ReplaceSessionCommits(ctx, repository, shas, links, time.Time{})
}
