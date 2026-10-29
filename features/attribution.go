package features

import (
	"cmp"
	"slices"
	"time"

	"github.com/baum-ai/baum/decisions"
)

// Decision attribution (README.md, "Decision attribution") gives each
// decision of a session exactly one feature: the host reads the session's
// links and the decisions' cited files, an Attributor chooses.

// LinkedPullRequest is a feature's pull request holding commits linked
// (matched) to a session, and the paths it touches.
type LinkedPullRequest struct {
	FeatureID, PullRequestID string
	Number                   int32
	OpenedAt                 time.Time
	Commits, Paths           []string
}

// SessionLinks is what attributes a session's decisions: the feature pull
// requests linked to it and the feature it is in on its own.
type SessionLinks struct {
	PullRequests []LinkedPullRequest
	OwnFeature   string
}

// Features are the session's features, dominant first: by the session's
// linked commits in each feature's pull requests, most first, then by the
// earliest of those pull requests. A session with no feature pull request
// has the one it is in on its own, if any.
func (s SessionLinks) Features() []string {
	if len(s.PullRequests) == 0 {
		if s.OwnFeature == "" {
			return nil
		}
		return []string{s.OwnFeature}
	}
	type rank struct {
		commits  map[string]bool
		earliest LinkedPullRequest
	}
	ranks := map[string]*rank{}
	for _, pull := range s.PullRequests {
		each := ranks[pull.FeatureID]
		if each == nil {
			each = &rank{commits: map[string]bool{}, earliest: pull}
			ranks[pull.FeatureID] = each
		}
		for _, commit := range pull.Commits {
			each.commits[commit] = true
		}
		if comparePullRequests(pull, each.earliest) < 0 {
			each.earliest = pull
		}
	}
	features := make([]string, 0, len(ranks))
	for feature := range ranks {
		features = append(features, feature)
	}
	slices.SortFunc(features, func(a, b string) int {
		return cmp.Or(
			cmp.Compare(len(ranks[b].commits), len(ranks[a].commits)),
			comparePullRequests(ranks[a].earliest, ranks[b].earliest),
			cmp.Compare(a, b),
		)
	})
	return features
}

// comparePullRequests orders pull requests earliest opened first, then by
// number.
func comparePullRequests(a, b LinkedPullRequest) int {
	return cmp.Or(a.OpenedAt.Compare(b.OpenedAt), cmp.Compare(a.Number, b.Number), cmp.Compare(a.PullRequestID, b.PullRequestID))
}

// Attributor attributes one session's decisions to exactly one feature
// each; it is built once per session, not per decision.
type Attributor struct {
	features  []string
	dominance map[string]int
	pulls     []citedPullRequest
}

type citedPullRequest struct {
	LinkedPullRequest
	cited decisions.CitedPaths
}

// Attributor builds the session's Attributor.
func (s SessionLinks) Attributor() Attributor {
	a := Attributor{features: s.Features()}
	a.pulls = make([]citedPullRequest, len(s.PullRequests))
	for i, pull := range s.PullRequests {
		a.pulls[i] = citedPullRequest{LinkedPullRequest: pull, cited: decisions.NewCitedPaths(pull.Paths)}
	}
	if len(a.features) <= 1 {
		return a
	}
	a.dominance = make(map[string]int, len(a.features))
	for i, feature := range a.features {
		a.dominance[feature] = i
	}
	return a
}

// PullRequest is the pull request of the feature a decision citing files is
// shown under: of the session's linked pull requests in that feature, the one
// whose paths contain most of the files, ties (and no match) to the earliest.
// "" when the session has none in it.
func (a Attributor) PullRequest(featureID string, files []string) string {
	var best *citedPullRequest
	bestScore := -1
	for i := range a.pulls {
		pull := &a.pulls[i]
		if featureID == "" || pull.FeatureID != featureID {
			continue
		}
		score := 0
		for _, file := range files {
			if _, ok := pull.cited.Match(file); ok {
				score++
			}
		}
		if score > bestScore || (score == bestScore && comparePullRequests(pull.LinkedPullRequest, best.LinkedPullRequest) < 0) {
			best, bestScore = pull, score
		}
	}
	if best == nil {
		return ""
	}
	return best.PullRequestID
}

// Attribute is the one feature a decision citing files goes to, "" when the
// session belongs to none. With pull requests of several features, it goes
// to the pull request whose paths contain most of the cited files, matched
// by decisions.CitedPaths, since a cited file may be relative to the
// session's working directory (ties to the more dominant feature, then the
// earlier pull request); with no file in any of them, or one feature, to the
// dominant feature.
func (a Attributor) Attribute(files []string) string {
	if len(a.features) <= 1 {
		if len(a.features) == 0 {
			return ""
		}
		return a.features[0]
	}
	var best *citedPullRequest
	bestScore := 0
	for i := range a.pulls {
		pull := &a.pulls[i]
		score := 0
		for _, file := range files {
			if _, ok := pull.cited.Match(file); ok {
				score++
			}
		}
		if score == 0 {
			continue
		}
		if best == nil || score > bestScore ||
			(score == bestScore && cmp.Or(
				cmp.Compare(a.dominance[pull.FeatureID], a.dominance[best.FeatureID]),
				comparePullRequests(pull.LinkedPullRequest, best.LinkedPullRequest)) < 0) {
			best, bestScore = pull, score
		}
	}
	if best != nil {
		return best.FeatureID
	}
	return a.features[0]
}
