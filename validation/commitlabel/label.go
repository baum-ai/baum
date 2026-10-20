// Package commitlabel relates captured validation calls to a reviewed revision.
// It makes claims only about edits recorded in the supplied session tree.
package commitlabel

import (
	"context"
	"fmt"
	"strings"
	"time"
)

type Kind string

const (
	Head        Kind = "head"
	Earlier     Kind = "earlier"
	Uncommitted Kind = "uncommitted"
	Unknown     Kind = "unknown"
)

type Label struct {
	Kind          Kind   `json:"kind"`
	CommitSHA     string `json:"commit_sha,omitempty"`
	CommitsBehind int    `json:"commits_behind,omitempty"`
	ToolCallID    string `json:"tool_call_id,omitempty"`
	Method        string `json:"method"`
	Reason        string `json:"reason"`
}

// Commit is verified Git object evidence, never a session's claimed tree ID.
type Commit struct{ SHA, TreeSHA string }

// Repository resolves unique hex commit IDs using only locally held objects.
// Missing, ambiguous or unavailable objects return an error. Implementations
// preserve their host's repository scope and object visibility rules.
type Repository interface {
	ValidationCommit(context.Context, string) (Commit, error)
}

// Interval includes both endpoints. Nil, zero or reversed endpoints are unknown.
type Interval struct{ StartedAt, EndedAt *time.Time }
type Call struct {
	ID, RepositoryID string
	Interval
}
type Edit struct {
	ToolCallID, RepositoryID string
	Interval
}
type CommitRef struct {
	SHA, Role, RepositoryID string
	ObservedAt              *time.Time
}
type Input struct {
	RepositoryID string
	HeadSHA      string
	// PRCommits is the complete oldest-to-newest PR commit list, ending at head.
	PRCommits  []string
	Calls      []Call
	Edits      []Edit
	CommitRefs []CommitRef
}

// Compute labels the latest cited call by start time. It never fetches objects,
// reads session stores, invokes a model or changes its inputs.
func Compute(ctx context.Context, repo Repository, input Input, citedIDs []string) Label {
	unknown := func(callID, reason string) Label {
		return Label{Kind: Unknown, ToolCallID: callID, Method: "insufficient_evidence", Reason: reason}
	}
	if input.RepositoryID == "" {
		return unknown("", "The repository scope is unknown.")
	}
	call, reason := latestCall(input, citedIDs)
	if reason != "" {
		return unknown("", reason)
	}
	for _, edit := range input.Edits {
		if edit.RepositoryID != "" && edit.RepositoryID != input.RepositoryID {
			continue
		}
		if edit.RepositoryID == "" || edit.ToolCallID == "" || !validInterval(edit.Interval) {
			return unknown(call.ID, "A session edit has unknown ownership, repository scope or timing.")
		}
		if edit.ToolCallID == call.ID {
			return unknown(call.ID, "The cited tool call also edits files; its run cannot be assigned to a commit.")
		}
	}
	if repo == nil {
		return unknown(call.ID, "Git evidence is unavailable.")
	}
	cache := map[string]Commit{}
	resolve := func(id string) (Commit, bool) {
		id = strings.ToLower(id)
		if !hexID(id, 7) {
			return Commit{}, false
		}
		if c, ok := cache[id]; ok {
			return c, true
		}
		c, err := repo.ValidationCommit(ctx, id)
		if err != nil || !fullID(c.SHA) || !fullID(c.TreeSHA) || !strings.HasPrefix(c.SHA, id) {
			return Commit{}, false
		}
		cache[id] = c
		return c, true
	}
	head, ok := resolve(input.HeadSHA)
	if !ok {
		return unknown(call.ID, "The reviewed head cannot be verified in Git.")
	}
	positions := map[string]int{}
	for i, id := range input.PRCommits {
		c, ok := resolve(id)
		if !ok {
			return unknown(call.ID, "A PR commit cannot be verified in Git.")
		}
		if _, duplicate := positions[c.SHA]; duplicate {
			return unknown(call.ID, "The PR commit order is ambiguous.")
		}
		positions[c.SHA] = len(input.PRCommits) - 1 - i
	}
	if behind, ok := positions[head.SHA]; !ok || behind != 0 {
		return unknown(call.ID, "The PR commit list does not end at the reviewed head.")
	}
	var best *Label
	var outside, unresolved, priorEdit bool
	for _, ref := range input.CommitRefs {
		if ref.Role != "created" {
			continue
		}
		if ref.RepositoryID == "" {
			unresolved = true
			continue
		}
		if ref.RepositoryID != input.RepositoryID {
			continue
		}
		if !validTime(ref.ObservedAt) {
			unresolved = true
			continue
		}
		c, ok := resolve(ref.SHA)
		if !ok {
			unresolved = true
			continue
		}
		start, end := *call.StartedAt, *call.EndedAt
		if ref.ObservedAt.Before(start) {
			start = *ref.ObservedAt
		}
		if ref.ObservedAt.After(end) {
			end = *ref.ObservedAt
		}
		blocked := false
		for _, edit := range input.Edits {
			if edit.RepositoryID != input.RepositoryID {
				continue
			}
			if !edit.EndedAt.Before(start) && !edit.StartedAt.After(end) {
				blocked = true
			}
			if ref.ObservedAt.Before(*edit.StartedAt) && edit.EndedAt.Before(*call.StartedAt) {
				priorEdit = true
			}
		}
		if blocked {
			continue
		}
		label := Label{CommitSHA: c.SHA, ToolCallID: call.ID, Method: "no_session_edits", Reason: fmt.Sprintf("No edits in this session between this run and commit %s.", c.SHA)}
		if c.TreeSHA == head.TreeSHA {
			label.Kind = Head
			if c.SHA == head.SHA {
				label.Reason = fmt.Sprintf("No edits in this session between this run and %s, the reviewed head.", c.SHA)
			} else {
				label.Method = "no_session_edits_same_tree"
				label.Reason = fmt.Sprintf("No edits in this session between this run and commit %s; Git verifies that commit's tree matches the reviewed head %s.", c.SHA, head.SHA)
			}
			if best == nil || best.Kind != Head || (c.SHA == head.SHA && best.CommitSHA != head.SHA) || (best.CommitSHA != head.SHA && label.CommitSHA < best.CommitSHA) {
				copy := label
				best = &copy
			}
		} else if behind, inPR := positions[c.SHA]; inPR {
			label.Kind, label.CommitsBehind = Earlier, behind
			label.Reason = fmt.Sprintf("No edits in this session between this run and commit %s, %d commits behind the reviewed head.", c.SHA, behind)
			if best == nil || (best.Kind != Head && behind < best.CommitsBehind) {
				copy := label
				best = &copy
			}
		} else {
			outside = true
		}
	}
	if best != nil {
		return *best
	}
	if outside {
		return unknown(call.ID, "A matching session-created commit is outside this PR and has a different tree from the reviewed head.")
	}
	if unresolved {
		return unknown(call.ID, "A session-created commit has unknown timing or cannot be verified in Git.")
	}
	if priorEdit {
		return Label{Kind: Uncommitted, ToolCallID: call.ID, Method: "session_edits_without_matching_commit", Reason: "This session edited files after a known created commit; no session-created PR commit matches this run without intervening session edits."}
	}
	return unknown(call.ID, "No session-created commit establishes the state used by this run.")
}

func latestCall(input Input, citedIDs []string) (Call, string) {
	if len(citedIDs) == 0 {
		return Call{}, "The item cites no tool calls."
	}
	byID := map[string]Call{}
	for _, call := range input.Calls {
		if _, duplicate := byID[call.ID]; duplicate {
			return Call{}, "Tool-call identities are ambiguous."
		}
		byID[call.ID] = call
	}
	var latest Call
	ambiguous := false
	for _, id := range citedIDs {
		call, ok := byID[id]
		if !ok || call.ID == "" || call.RepositoryID != input.RepositoryID || !validInterval(call.Interval) {
			return Call{}, "A cited tool call is missing or has unknown repository scope or timing."
		}
		if latest.ID == "" || call.StartedAt.After(*latest.StartedAt) {
			latest = call
			ambiguous = false
		} else if call.StartedAt.Equal(*latest.StartedAt) && call.ID != latest.ID {
			ambiguous = true
		}
	}
	if ambiguous {
		return Call{}, "The latest cited tool call is ambiguous because timestamps coincide."
	}
	return latest, ""
}
func validTime(t *time.Time) bool { return t != nil && !t.IsZero() }
func validInterval(i Interval) bool {
	return validTime(i.StartedAt) && validTime(i.EndedAt) && !i.EndedAt.Before(*i.StartedAt)
}
func fullID(id string) bool { return (len(id) == 40 || len(id) == 64) && hexID(id, 40) }
func hexID(id string, min int) bool {
	if len(id) < min || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9') && !(c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
