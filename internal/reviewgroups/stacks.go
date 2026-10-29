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
)

// Stack is a set of open pull requests stacked on each other, as Stacks
// finds them.
type Stack struct {
	// Members are the pull request numbers, ascending.
	Members []int
	// Name is the group name a new stack group gets: the bottom pull
	// request's title.
	Name string
}

// Stacks finds the stacks among a repository's open pull requests: chains
// of pull requests by one author, each based on the previous one's head
// branch. A branch several open pull requests are based on is shared, like a
// develop branch with its own pull request into main, and links none of them.
// Pull requests from forks are left out, and so are stacks of more than
// MaxMembers.
func Stacks(prs []discovery.PullRequest) []Stack {
	prs = slices.DeleteFunc(slices.Clone(prs), func(pr discovery.PullRequest) bool {
		return pr.Fork || pr.HeadRef == "" || pr.Number <= 0
	})
	slices.SortFunc(prs, func(a, b discovery.PullRequest) int { return a.Number - b.Number })
	parent := make([]int, len(prs))
	for i := range parent {
		parent[i] = i
	}
	var root func(int) int
	root = func(i int) int {
		if parent[i] != i {
			parent[i] = root(parent[i])
		}
		return parent[i]
	}
	heads := map[string][]int{}
	bases := map[string]int{}
	for i, pr := range prs {
		heads[pr.HeadRef] = append(heads[pr.HeadRef], i)
		bases[pr.BaseRef]++
	}
	stacked := make([]bool, len(prs))
	for i, pr := range prs {
		below := heads[pr.BaseRef]
		if bases[pr.BaseRef] != 1 || len(below) != 1 || below[0] == i || !strings.EqualFold(prs[below[0]].Author, pr.Author) {
			continue
		}
		stacked[i] = true
		parent[root(i)] = root(below[0])
	}
	components := map[int][]int{}
	for i := range prs {
		components[root(i)] = append(components[root(i)], i)
	}
	var out []Stack
	for _, members := range components {
		if len(members) < MinMembers || len(members) > MaxMembers {
			continue
		}
		// The bottom is the one member not stacked on another.
		bottom := members[0]
		for _, i := range members {
			if !stacked[i] {
				bottom = i
				break
			}
		}
		stack := Stack{Name: stackName(prs[bottom])}
		for _, i := range members {
			stack.Members = append(stack.Members, prs[i].Number)
		}
		out = append(out, stack)
	}
	slices.SortFunc(out, func(a, b Stack) int { return a.Members[0] - b.Members[0] })
	return out
}

// stackName is a valid group name from the bottom pull request's title.
func stackName(pr discovery.PullRequest) string {
	title := strings.Join(strings.FieldsFunc(pr.Title, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }), " ")
	if title == "" {
		return fmt.Sprintf("Stack from #%d", pr.Number)
	}
	if runes := []rune(title); len(runes) > MaxNameLength {
		title = strings.TrimSpace(string(runes[:MaxNameLength-1])) + "…"
	}
	return title
}

// SyncStacks keeps repository's stack groups in step with its current
// stacks and reports whether anything changed:
//
//   - a stack without a group gets a new stack group;
//   - a stack group whose stack gained or lost members gets the stack's
//     members;
//   - a stack group whose stack is gone (merged, closed or unstacked) is
//     dissolved: removed from every list, its versions kept.
//
// The person's choices win: a stack with a pull request in a group the person
// made or took over, or in a stack group the person deleted, gets no group.
func (s *Store) SyncStacks(ctx context.Context, repository string, stacks []Stack) (bool, error) {
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
	now := time.Now().UTC().Truncate(time.Millisecond)
	changed := false
	kept := map[string]bool{}
	for _, stack := range stacks {
		members, err := cleanMembers(stack.Members)
		if err != nil || slices.ContainsFunc(members, func(n int) bool { return claimed[n] }) {
			continue
		}
		at := slices.IndexFunc(live, func(g Group) bool {
			return !kept[g.ID] && slices.ContainsFunc(g.Members, func(n int) bool { return slices.Contains(members, n) })
		})
		encoded, _ := json.Marshal(members)
		if at < 0 {
			name, err := cleanName(stack.Name)
			if err != nil {
				name = fmt.Sprintf("Stack from #%d", members[0])
			}
			id := newID()
			if _, err := tx.ExecContext(ctx, `INSERT INTO groups(id,repository,name,members,stack,created,updated) VALUES(?,?,?,?,1,?,?)`, id, repository, name, string(encoded), now.UnixMilli(), now.UnixMilli()); err != nil {
				return false, err
			}
			kept[id], changed = true, true
			continue
		}
		g := live[at]
		kept[g.ID] = true
		if !slices.Equal(g.Members, members) {
			if _, err := tx.ExecContext(ctx, `UPDATE groups SET members=?, updated=? WHERE id=?`, string(encoded), now.UnixMilli(), g.ID); err != nil {
				return false, err
			}
			changed = true
		}
	}
	for _, g := range live {
		if kept[g.ID] {
			continue
		}
		// Dissolved, not deleted by the person: the flag goes, so its pull
		// requests may form a stack group again.
		if _, err := tx.ExecContext(ctx, `UPDATE groups SET deleted=?, stack=0 WHERE id=?`, now.UnixMilli(), g.ID); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, tx.Commit()
}
