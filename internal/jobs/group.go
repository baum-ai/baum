package jobs

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/baum-ai/baum/internal/generate"
	"github.com/baum-ai/baum/internal/github"
	"github.com/baum-ai/baum/internal/repos"
	"github.com/baum-ai/baum/internal/reviewgroups"
	"github.com/baum-ai/baum/internal/store"
)

// MembersNeedReviewsError refuses a group job whose members have no saved
// review: a group review reads each member's own review. Over HTTP it is a
// 409 with {"members_need_reviews": Numbers}.
type MembersNeedReviewsError struct{ Numbers []int }

func (e *MembersNeedReviewsError) Error() string {
	numbers := make([]string, len(e.Numbers))
	for i, n := range e.Numbers {
		numbers[i] = "#" + strconv.Itoa(n)
	}
	return "a group review needs a saved review of every member; generate one for " + strings.Join(numbers, ", ") + " first"
}

// ReasonMembersNeedReviews is a group job's failure when a member it waited
// for still has no saved review: that member's own review failed or was
// cancelled.
const ReasonMembersNeedReviews = "members_need_reviews"

// ReasonMembersChanged fails a group job whose members kept moving, or
// changed, while it waited, so it could not wait for them again.
const ReasonMembersChanged = "members_changed"

// ReasonGroupRemoved cancels a group job whose group was deleted or whose
// stack dissolved before it ran.
const ReasonGroupRemoved = "group_removed"

// ErrGroupsUnavailable is a group job on a runner without a group store.
var ErrGroupsUnavailable = errors.New("review groups are not available")

// groupInputs is what preparation reads of a group: its name, its members
// with their heads, and, as one pull, the first member's head and whether
// any member needs the isolation confirmation.
type groupInputs struct {
	pull
	name, members string
	// awaiting, with Item.AwaitMembers, are the members without a saved
	// review of their current head.
	awaiting []int
}

// prepareGroup reads every member of the group from GitHub, as a pull
// request job reads its pull request, and checks that each has a saved
// review in the clone's store, unless await accepts members without one.
func (l *Local) prepareGroup(ctx context.Context, c repos.Clone, reader *github.RESTReader, repository github.Repository, id string, await bool) (groupInputs, error) {
	if l.Groups == nil {
		return groupInputs{}, ErrGroupsUnavailable
	}
	group, err := l.Groups.Get(ctx, id)
	if err != nil {
		return groupInputs{}, err
	}
	if !strings.EqualFold(group.Repository, c.Repository) {
		return groupInputs{}, errors.New("the clone is not of the group's repository")
	}
	versions, err := store.Open(c.CommonDir, "", false)
	if err != nil {
		return groupInputs{}, err
	}
	out := groupInputs{name: group.Name}
	parts := make([]string, 0, len(group.Members))
	var missing []int
	for i, number := range group.Members {
		pr, err := readPull(ctx, reader, repository, number)
		if err != nil {
			return groupInputs{}, fmt.Errorf("pull request #%d: %w", number, err)
		}
		if i == 0 {
			out.Head = pr.Head
		}
		out.Untrusted = out.Untrusted || pr.Untrusted
		out.Draft = out.Draft || pr.Draft
		parts = append(parts, strconv.Itoa(number)+"@"+pr.Head)
		latest, err := versions.Latest(ctx, store.Key{Repository: c.Repository, Number: number})
		switch {
		case errors.Is(err, store.ErrNotFound):
			missing = append(missing, number)
			out.awaiting = append(out.awaiting, number)
		case err != nil:
			return groupInputs{}, err
		case latest.Metadata.Head != pr.Head:
			out.awaiting = append(out.awaiting, number)
		}
	}
	if len(missing) > 0 && !await {
		return groupInputs{}, &MembersNeedReviewsError{Numbers: missing}
	}
	if !await {
		out.awaiting = nil
	}
	out.members = strings.Join(parts, ",")
	return out, nil
}

// groupMembers parses Prepared.Members.
func groupMembers(encoded string) ([]generate.GroupMember, error) {
	var members []generate.GroupMember
	for part := range strings.SplitSeq(encoded, ",") {
		number, head, ok := strings.Cut(part, "@")
		n, err := strconv.Atoi(number)
		if !ok || err != nil || n <= 0 || head == "" {
			return nil, errors.New("invalid group members")
		}
		members = append(members, generate.GroupMember{Number: n, Head: head})
	}
	return members, nil
}

// groupRequest completes a group job's generation options: the members with
// their saved review versions, and members.json from those reviews.
func (l *Local) groupRequest(ctx context.Context, p Prepared, versions *store.Store) (*generate.Group, error) {
	if l.Groups == nil {
		return nil, ErrGroupsUnavailable
	}
	group, err := l.Groups.Get(ctx, p.Group)
	if err != nil {
		return nil, err
	}
	members, err := groupMembers(p.Members)
	if err != nil {
		return nil, err
	}
	reviews := make([]reviewgroups.MemberReview, 0, len(members))
	var missing []int
	for i, member := range members {
		latest, err := versions.Latest(ctx, store.Key{Repository: p.Repository, Number: member.Number})
		if errors.Is(err, store.ErrNotFound) {
			missing = append(missing, member.Number)
			continue
		}
		if err != nil {
			return nil, err
		}
		members[i].ReviewVersion = latest.Metadata.Version
		reviews = append(reviews, reviewgroups.MemberReview{Number: member.Number, Head: member.Head, Version: latest.Metadata.Version, Document: latest.Document})
	}
	if len(missing) > 0 {
		return nil, &MembersNeedReviewsError{Numbers: missing}
	}
	file, err := reviewgroups.MembersFile(reviews)
	if err != nil {
		return nil, err
	}
	return &generate.Group{Name: group.Name, GeneratedName: group.Stack, Members: members, MemberReviews: file}, nil
}
