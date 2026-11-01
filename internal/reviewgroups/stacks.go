package reviewgroups

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
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

// chainEntry is one member of a stack group as the last sync saw it: its
// place in the stack (Parent, nil for the root) and the branches and author
// the detection rules read, kept because a merged member leaves the open pull
// requests that discovery lists. State is empty for an open member, else
// what discovery last knew of it (the State constants).
type chainEntry struct {
	Number  int    `json:"n"`
	Parent  *int   `json:"p,omitempty"`
	BaseRef string `json:"b,omitempty"`
	HeadRef string `json:"h,omitempty"`
	Author  string `json:"a,omitempty"`
	State   string `json:"s,omitempty"`
}

// SyncStacks keeps repository's stack groups in step with the stacks among
// its pull requests (package stacks, the hosted detection) and reports
// whether anything changed. prs are the open pull requests and observations
// what discovery knows of others (a merged or closed state):
//
//   - a stack of 2 to MaxMembers open pull requests without a group gets a
//     new stack group;
//   - a stack group whose stack gained or lost members gets the stack's
//     members;
//   - a member that merged or closed stays in the group in its place, as
//     Landed with its state, and is no longer a Member. A member discovery
//     no longer lists and has no observation of is kept as StateUnknown until
//     its state is known;
//   - a stack group whose stack is gone (no open member left,
//     unstacked, or grown past MaxMembers open pull requests) is dissolved:
//     removed from every list, its versions kept.
//
// The person's choices win: a pull request in a group the person made or took
// over, or in a stack group the person deleted, takes no part.
func (s *Store) SyncStacks(ctx context.Context, repository string, prs []discovery.PullRequest, observations []discovery.Observation) (bool, error) {
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
	candidates := map[int]stacks.Candidate{}
	for _, pr := range prs {
		if !claimed[pr.Number] {
			pulls[pr.Number] = pr
			candidates[pr.Number] = stacks.Candidate{Number: pr.Number, Open: true, CrossRepository: pr.Fork, BaseRef: pr.BaseRef, HeadRef: pr.HeadRef, Author: pr.Author}
		}
	}
	observed := map[int]string{}
	for _, observation := range observations {
		observed[observation.Number] = strings.ToUpper(observation.State)
	}
	stored := make([]stacks.StoredStack, len(live))
	byID := map[string]Group{}
	for i, g := range live {
		byID[g.ID] = g
		stored[i] = stacks.StoredStack{ID: g.ID, CreatedAt: g.CreatedAt}
		entries := map[int]chainEntry{}
		for _, number := range g.Members {
			entries[number] = chainEntry{Number: number}
		}
		for _, entry := range g.chain {
			entries[entry.Number] = entry
		}
		for _, number := range slices.Sorted(maps.Keys(entries)) {
			entry := entries[number]
			stored[i].Members = append(stored[i].Members, stacks.Placement{Number: number, Parent: entry.Parent})
			// A member that is no longer open is a candidate only from what
			// the last sync saved. Closed keeps its place like merged does;
			// a state not known yet is planned as merged, whose retarget
			// rule keeps a chain a merge retargeted.
			if _, open := candidates[number]; !open && !claimed[number] && (entry.HeadRef != "" || entry.BaseRef != "") {
				candidates[number] = stacks.Candidate{Number: number, Merged: landedState(observed[number], entry.State) != StateClosed, BaseRef: entry.BaseRef, HeadRef: entry.HeadRef, Author: entry.Author}
			}
		}
	}
	changes := stacks.Plan(slices.Collect(maps.Values(candidates)), stored)
	now := time.Now().UTC().Truncate(time.Millisecond)
	changed := false
	for _, stack := range changes.Created {
		var members []int
		var name string
		var entries []chainEntry
		for _, member := range stack.Members {
			members = append(members, member.Number)
			if member.Parent == nil {
				name = stackName(pulls[member.Number])
			}
			entries = append(entries, entryOf(member.Number, member.Parent, candidates))
		}
		if members, err = cleanMembers(members); err != nil {
			continue
		}
		encoded, _ := json.Marshal(members)
		chain, _ := json.Marshal(sortedEntries(entries))
		if _, err := tx.ExecContext(ctx, `INSERT INTO groups(id,repository,name,members,stack,chain,created,updated) VALUES(?,?,?,?,1,?,?,?)`, newID(), repository, name, string(encoded), string(chain), now.UnixMilli(), now.UnixMilli()); err != nil {
			return false, err
		}
		changed = true
	}
	updates := map[string]stacks.StackChange{}
	for _, update := range changes.Updated {
		updates[update.ID] = update
	}
	// Every live group is rewritten from the stack's placements, so that the
	// saved branches follow a retarget even when membership did not change.
	for _, g := range live {
		update := updates[g.ID]
		entries := map[int]chainEntry{}
		for _, number := range g.Members {
			entries[number] = chainEntry{Number: number}
		}
		for _, entry := range g.chain {
			entries[entry.Number] = entry
		}
		for _, number := range update.Leave {
			delete(entries, number)
		}
		for _, member := range update.Join {
			entries[member.Number] = chainEntry{Number: member.Number, Parent: member.Parent}
		}
		var members []int
		list := make([]chainEntry, 0, len(entries))
		for number, entry := range entries {
			if refreshed, ok := candidates[number]; ok {
				entry.BaseRef, entry.HeadRef, entry.Author = refreshed.BaseRef, refreshed.HeadRef, refreshed.Author
				previous := entry.State
				entry.State = ""
				if !refreshed.Open {
					entry.State = landedState(observed[number], previous)
				}
			}
			if entry.State == "" {
				members = append(members, number)
			}
			list = append(list, entry)
		}
		list = sortedEntries(list)
		slices.Sort(members)
		encodedChain, _ := json.Marshal(list)
		previousChain, _ := json.Marshal(sortedEntries(g.chain))
		switch {
		case (update.Ended != nil && *update.Ended) || len(members) == 0 || len(members) > MaxMembers:
			// Dissolved, not deleted by the person: the flag goes, so its
			// pull requests may form a stack group again.
			_, err = tx.ExecContext(ctx, `UPDATE groups SET deleted=?, stack=0 WHERE id=?`, now.UnixMilli(), g.ID)
		case !slices.Equal(members, g.Members) || string(encodedChain) != string(previousChain):
			encodedMembers, _ := json.Marshal(members)
			_, err = tx.ExecContext(ctx, `UPDATE groups SET members=?, chain=?, updated=? WHERE id=?`, string(encodedMembers), string(encodedChain), now.UnixMilli(), g.ID)
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

// The states of a member that is no longer open (chainEntry.State).
const (
	StateMerged  = "merged"
	StateClosed  = "closed"
	StateUnknown = "unknown"
)

// landedState is a no-longer-open member's state from discovery's observation
// (MERGED, CLOSED, or OPEN/none when it has not seen the change yet), keeping
// the previously known merged or closed state.
func landedState(observed, previous string) string {
	switch observed {
	case "MERGED":
		return StateMerged
	case "CLOSED":
		return StateClosed
	}
	if previous == StateMerged || previous == StateClosed {
		return previous
	}
	return StateUnknown
}

func entryOf(number int, parent *int, candidates map[int]stacks.Candidate) chainEntry {
	c := candidates[number]
	return chainEntry{Number: number, Parent: parent, BaseRef: c.BaseRef, HeadRef: c.HeadRef, Author: c.Author, State: ""}
}

func sortedEntries(entries []chainEntry) []chainEntry {
	out := slices.Clone(entries)
	if out == nil {
		out = []chainEntry{}
	}
	slices.SortFunc(out, func(a, b chainEntry) int { return a.Number - b.Number })
	return out
}
