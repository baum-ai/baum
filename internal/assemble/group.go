package assemble

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/baum-ai/baum/bundle"
)

// GroupMember is one member of a review group, assembled on its own.
type GroupMember struct {
	Number int
	Result Result
}

// GroupResult is the merged bundle of a review group.
type GroupResult struct {
	Result
	// HunkMembers names, for each hunk of the merged diff, the members whose
	// own diff holds it, in member order: a change two members both make is
	// one hunk of both.
	HunkMembers map[string][]int
}

// maxGroupMemberBody bounds each member's description in the group's body,
// so one long description cannot crowd out the others.
const maxGroupMemberBody = 4096

// MergeGroup merges the members' bundles, each assembled, redacted and fitted
// on its own, into the bundle of the group's combined review:
//
//   - the pull request header is the first member's (its number, branches and
//     SHAs), titled with the group's name, with a body listing every member
//     and its description, and every member's commits;
//   - the diff holds every member's hunks in member order, a hunk ID the
//     earlier member already holds listed once;
//   - checks, conversation, threads, sessions and references are the union,
//     each identifier once; description images are left out;
//   - FileComparisons holds one comparison per member, from its merge base,
//     with its file changes before deduplication;
//   - previous is the group's latest version, its sections keeping only the
//     pull requests still in the group.
//
// The merged bundle is fitted to caps again and digested. The name is
// redacted like any other text; the members' text already is.
func MergeGroup(ctx context.Context, name string, members []GroupMember, previous *bundle.PreviousVersion, caps bundle.Caps, redactor *bundle.Redactor) (GroupResult, error) {
	if len(members) == 0 {
		return GroupResult{}, errors.New("a review group needs a member")
	}
	if redactor == nil {
		var err error
		if redactor, err = bundle.NewRedactor(); err != nil {
			return GroupResult{}, err
		}
	}
	title, masked, err := redactor.RedactString(ctx, strings.TrimSpace(name))
	if err != nil {
		return GroupResult{}, err
	}
	first := members[0].Result
	b := bundle.Bundle{
		Repository: first.Bundle.Repository, PullRequest: first.Bundle.PullRequest,
		Diff: []bundle.Hunk{}, Checks: []bundle.Check{}, Conversation: []bundle.ConversationItem{},
		Threads: []bundle.Thread{}, Sessions: []bundle.Session{}, References: []bundle.Reference{},
		Sources: bundle.Sources{}, MaskedSpanCount: masked,
	}
	b.PullRequest.Title = title
	b.PullRequest.State = "open"
	b.PullRequest.Commits = []bundle.Commit{}
	b.PullRequest.RequestedReviewers = []string{}
	if config := first.Bundle.ReviewConfig; config != nil {
		copied := *config
		b.ReviewConfig = &copied
	}
	var body strings.Builder
	fmt.Fprintf(&body, "A review group of %d pull requests of %s, reviewed together:\n", len(members), b.Repository)
	hunkMembers := map[string][]int{}
	seen := map[string]bool{}
	for _, member := range members {
		mb := member.Result.Bundle
		pr := mb.PullRequest
		fmt.Fprintf(&body, "\n## #%d: %s\n\nBranch `%s` into `%s`, head %s.\n", member.Number, pr.Title, pr.HeadRef, pr.BaseRef, shortSHA(member.Result.Head))
		if description := strings.TrimSpace(pr.Body); description != "" {
			if len(description) > maxGroupMemberBody {
				description = strings.ToValidUTF8(description[:maxGroupMemberBody], "") + "…"
			}
			body.WriteString("\n" + description + "\n")
		}
		for _, commit := range pr.Commits {
			if !seen["commit:"+commit.SHA] {
				seen["commit:"+commit.SHA] = true
				b.PullRequest.Commits = append(b.PullRequest.Commits, commit)
			}
		}
		for _, login := range pr.RequestedReviewers {
			if !slices.Contains(b.PullRequest.RequestedReviewers, login) {
				b.PullRequest.RequestedReviewers = append(b.PullRequest.RequestedReviewers, login)
			}
		}
		comparison := bundle.FileComparison{BaseRevision: member.Result.MergeBase, Changes: []bundle.ComparedFile{}}
		if comparison.BaseRevision == "" {
			comparison.BaseRevision = member.Result.Base
		}
		files := map[bundle.ComparedFile]bool{}
		for _, hunk := range mb.Diff {
			file := bundle.ComparedFile{Path: hunk.Path, OldPath: hunk.OldPath, Change: hunk.Change}
			if !files[file] {
				files[file] = true
				comparison.Changes = append(comparison.Changes, file)
			}
			if !slices.Contains(hunkMembers[hunk.ID], member.Number) {
				hunkMembers[hunk.ID] = append(hunkMembers[hunk.ID], member.Number)
			}
			if !seen["hunk:"+hunk.ID] {
				seen["hunk:"+hunk.ID] = true
				b.Diff = append(b.Diff, hunk)
			}
		}
		b.FileComparisons = append(b.FileComparisons, comparison)
		for _, check := range mb.Checks {
			if !seen["check:"+check.ID] {
				seen["check:"+check.ID] = true
				b.Checks = append(b.Checks, check)
			}
		}
		for _, item := range mb.Conversation {
			if !seen["conversation:"+item.ID] {
				seen["conversation:"+item.ID] = true
				b.Conversation = append(b.Conversation, item)
			}
		}
		for _, thread := range mb.Threads {
			if !seen["thread:"+thread.ID] {
				seen["thread:"+thread.ID] = true
				b.Threads = append(b.Threads, thread)
			}
		}
		for _, session := range mb.Sessions {
			if !seen["session:"+session.RootSessionID] {
				seen["session:"+session.RootSessionID] = true
				b.Sessions = append(b.Sessions, session)
			}
		}
		for _, reference := range mb.References {
			key := "reference:" + reference.Kind + "\x00" + reference.Value + "\x00" + reference.Source
			if !seen[key] {
				seen[key] = true
				b.References = append(b.References, reference)
			}
		}
		for _, note := range mb.Truncated {
			if !slices.Contains(b.Truncated, note) {
				b.Truncated = append(b.Truncated, note)
			}
		}
		for source, present := range mb.Sources {
			b.Sources[source] = b.Sources[source] || present
		}
		if mb.ReviewConfig != nil && mb.ReviewConfig.ChangedInPullRequest && b.ReviewConfig != nil {
			b.ReviewConfig.ChangedInPullRequest = true
		}
		b.MaskedSpanCount += mb.MaskedSpanCount
	}
	b.PullRequest.Body = body.String()
	bundle.SortConversation(b.Conversation)
	b.Sources["attachments"] = false
	b.Sources["diff"] = len(b.Diff) > 0
	numbers := make([]int, len(members))
	for i, member := range members {
		numbers[i] = member.Number
	}
	if b.Previous, err = groupPrevious(previous, numbers); err != nil {
		return GroupResult{}, fmt.Errorf("previous group review: %w", err)
	}
	b.Sources["previous_version"] = b.Previous != nil
	if err := bundle.Fit(&b, caps); err != nil {
		return GroupResult{}, err
	}
	if err := bundle.SetDigest(&b); err != nil {
		return GroupResult{}, err
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		return GroupResult{}, fmt.Errorf("encoding bundle: %w", err)
	}
	return GroupResult{
		Result:      Result{Bundle: b, JSON: append(encoded, '\n'), Head: first.Head, Base: first.Base, MergeBase: first.MergeBase},
		HunkMembers: hunkMembers,
	}, nil
}

// groupPrevious carries the group's latest version into the next one: a
// carried section keeps only the pull requests still in the group, since a
// section naming one that left would no longer validate.
func groupPrevious(previous *bundle.PreviousVersion, members []int) (*bundle.PreviousVersion, error) {
	if previous == nil {
		return nil, nil
	}
	carried := *previous
	carried.Sections = slices.Clone(previous.Sections)
	for index, section := range carried.Sections {
		if len(section.Content) == 0 {
			continue
		}
		var content map[string]json.RawMessage
		if err := json.Unmarshal(section.Content, &content); err != nil {
			return nil, err
		}
		raw, ok := content["pull_requests"]
		if !ok {
			continue
		}
		var numbers []int
		if err := json.Unmarshal(raw, &numbers); err != nil {
			return nil, err
		}
		numbers = slices.DeleteFunc(numbers, func(number int) bool { return !slices.Contains(members, number) })
		if len(numbers) == 0 {
			delete(content, "pull_requests")
		} else {
			kept, err := json.Marshal(numbers)
			if err != nil {
				return nil, err
			}
			content["pull_requests"] = kept
		}
		encoded, err := json.Marshal(content)
		if err != nil {
			return nil, err
		}
		carried.Sections[index].Content = encoded
	}
	return &carried, nil
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}
