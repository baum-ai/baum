package reviewgroups

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/baum-ai/baum/internal/discovery"
	"github.com/baum-ai/baum/stacks"
)

// stackName is the name "Review together" suggests for a group: the first
// (here the bottom) pull request's title, cut to a group name's length.
func stackName(pr discovery.PullRequest) string {
	title := strings.Join(strings.FieldsFunc(pr.Title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if title == "" {
		return fmt.Sprintf("#%d", pr.Number)
	}
	if runes := []rune(title); len(runes) > MaxNameLength {
		title = strings.TrimSpace(string(runes[:MaxNameLength]))
	}
	return title
}

// SyncStacks keeps repository's stack groups in step with the stacks among
// its open pull requests (package stacks, the hosted detection) and reports
// whether anything changed:
//
//   - a stack of 2 to MaxMembers without a group gets a new stack group;
//   - a stack group whose stack gained or lost members gets the stack's
//     members;
//   - a stack group whose stack is gone (merged, closed, unstacked or grown
//     past MaxMembers) is dissolved: removed from every list, its versions
//     kept.
//
// The person's choices win: a pull request in a group the person made or took
// over, or in a stack group the person deleted, takes no part.
func (s *Store) SyncStacks(ctx context.Context, repository string, prs []discovery.PullRequest) (bool, error) {
	repository = strings.ToLower(strings.TrimSpace(repository))
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	// Live groups, then stack groups the person deleted.
	var live []Group
	claimed := map[int]bool{}
	for _, deleted := range []string{`deleted=0`, `deleted>0 AND stack=1`} {
		rows, err := tx.QueryContext(ctx, `SELECT `+groupColumns+` FROM groups WHERE repository=? AND `+deleted+` ORDER BY created, id`, repository)
		if err != nil {
			return false, err
		}
		for rows.Next() {
			g, err := scanGroup(rows)
			if err != nil {
				_ = rows.Close()
				return false, err
			}
			if g.Stack && deleted == `deleted=0` {
				live = append(live, g)
				continue
			}
			for _, number := range g.Members {
				claimed[number] = true
			}
		}
		if err := rows.Close(); err != nil {
			return false, err
		}
	}
	pulls := map[int]discovery.PullRequest{}
	var candidates []stacks.Candidate
	for _, pr := range prs {
		if !claimed[pr.Number] {
			pulls[pr.Number] = pr
			candidates = append(candidates, stacks.Candidate{Number: pr.Number, Open: true, CrossRepository: pr.Fork, BaseRef: pr.BaseRef, HeadRef: pr.HeadRef, Author: pr.Author})
		}
	}
	stored := make([]stacks.StoredStack, len(live))
	byID := map[string]Group{}
	for i, g := range live {
		byID[g.ID] = g
		stored[i] = stacks.StoredStack{ID: g.ID, CreatedAt: g.CreatedAt}
		for _, number := range g.Members {
			stored[i].Members = append(stored[i].Members, stacks.Placement{Number: number})
		}
	}
	changes := stacks.Plan(candidates, stored)
	now := time.Now().UTC().Truncate(time.Millisecond)
	changed := false
	for _, stack := range changes.Created {
		var members []int
		var name string
		for _, member := range stack.Members {
			members = append(members, member.Number)
			if member.Parent == nil {
				name = stackName(pulls[member.Number])
			}
		}
		if members, err = cleanMembers(members); err != nil {
			continue
		}
		encoded, _ := json.Marshal(members)
		if _, err := tx.ExecContext(ctx, `INSERT INTO groups(id,repository,name,members,stack,created,updated) VALUES(?,?,?,?,1,?,?)`, newID(), repository, name, string(encoded), now.UnixMilli(), now.UnixMilli()); err != nil {
			return false, err
		}
		changed = true
	}
	// A stack group stores no parents, so an update rejoins every member;
	// only a change of members is written.
	for _, update := range changes.Updated {
		g := byID[update.ID]
		members := slices.DeleteFunc(slices.Clone(g.Members), func(n int) bool { return slices.Contains(update.Leave, n) })
		for _, member := range update.Join {
			if !slices.Contains(members, member.Number) {
				members = append(members, member.Number)
			}
		}
		members, err := cleanMembers(members)
		switch {
		case err != nil || (update.Ended != nil && *update.Ended):
			// Dissolved, not deleted by the person: the flag goes, so its
			// pull requests may form a stack group again.
			_, err = tx.ExecContext(ctx, `UPDATE groups SET deleted=?, stack=0 WHERE id=?`, now.UnixMilli(), g.ID)
		case !slices.Equal(members, g.Members):
			encoded, _ := json.Marshal(members)
			_, err = tx.ExecContext(ctx, `UPDATE groups SET members=?, updated=? WHERE id=?`, string(encoded), now.UnixMilli(), g.ID)
		default:
			continue
		}
		if err != nil {
			return false, err
		}
		changed = true
	}
	return changed, tx.Commit()
}
