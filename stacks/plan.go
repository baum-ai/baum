package stacks

import (
	"cmp"
	"slices"
	"strings"
	"time"
)

// Candidate is a pull request a recompute considers: one that is open, or a
// current member of a stack the recompute can change.
type Candidate struct {
	Number          int
	Open            bool
	Merged          bool
	CrossRepository bool
	BaseRef         string
	HeadRef         string
	// Author is the author's login. A pull request is stacked only on one by
	// the same author, so a shared branch's own pull request (develop into
	// main) does not gather everyone's pull requests based on that branch.
	Author string
}

// StoredStack is a stack as stored, with its current members only.
type StoredStack struct {
	ID        string
	CreatedAt time.Time
	Ended     bool
	Members   []Placement
}

// Placement is a member's place in a stack: its number and its parent's, nil
// for the root.
type Placement struct {
	Number int
	Parent *int
}

// Changes is what a recompute writes. Leaves are applied before joins, so a
// member moving between stacks is never current in two.
type Changes struct {
	Created []NewStack
	Updated []StackChange
}

type NewStack struct {
	Members []Placement
}

// StackChange changes one stored stack. Join adds members, returns members
// that had left and changes parents. Ended, when set, is the new ended state.
type StackChange struct {
	ID    string
	Join  []Placement
	Leave []int
	Ended *bool
}

func (c Changes) Empty() bool { return len(c.Created) == 0 && len(c.Updated) == 0 }

// Plan computes the changes that bring a repository's stored stacks in line
// with its pull requests (README.md, "Detection" and "Identity"). It is pure:
// the same input always gives the same changes, and applying them and
// planning again gives none.
func Plan(candidates []Candidate, stored []StoredStack) Changes {
	storedParent := map[int]*int{}
	storedStack := map[int]int{}
	for index, stack := range stored {
		for _, member := range stack.Members {
			storedParent[member.Number] = member.Parent
			storedStack[member.Number] = index
		}
	}

	// Forks never take part, and a merged or closed pull request takes part
	// only while it is a current member: a stack never forms around one.
	pulls := map[int]Candidate{}
	for _, pull := range candidates {
		if pull.CrossRepository {
			continue
		}
		if _, member := storedStack[pull.Number]; !pull.Open && !member {
			continue
		}
		pulls[pull.Number] = pull
	}
	numbers := sortedKeys(pulls)
	byHead := map[string][]int{}
	for _, number := range numbers {
		head := pulls[number].HeadRef
		if head != "" {
			byHead[head] = append(byHead[head], number)
		}
	}

	parents := map[int]int{}
	for _, number := range numbers {
		if parent, ok := chooseParent(pulls[number], pulls, byHead, storedParent); ok {
			parents[number] = parent
		}
	}
	breakCycles(numbers, parents)

	components := connectedComponents(numbers, parents)
	assigned := assignStacks(components, stored, storedStack)

	var changes Changes
	for index, component := range components {
		if stackIndex, ok := assigned[index]; ok {
			if change, ok := diffStack(stored[stackIndex], component, parents, pulls); ok {
				changes.Updated = append(changes.Updated, change)
			}
			continue
		}
		if len(component) < 2 || openCount(component, pulls) < 2 {
			continue
		}
		changes.Created = append(changes.Created, NewStack{Members: placements(component, parents)})
	}
	claimed := map[int]bool{}
	for _, stackIndex := range assigned {
		claimed[stackIndex] = true
	}
	for index, stack := range stored {
		if claimed[index] {
			continue
		}
		change := StackChange{ID: stack.ID}
		for _, member := range stack.Members {
			change.Leave = append(change.Leave, member.Number)
		}
		if !stack.Ended {
			change.Ended = ptr(true)
		}
		if len(change.Leave) > 0 || change.Ended != nil {
			changes.Updated = append(changes.Updated, change)
		}
	}
	slices.SortFunc(changes.Updated, func(a, b StackChange) int { return cmp.Compare(a.ID, b.ID) })
	return changes
}

// chooseParent preserves a stored chain when a merge retargets members to a
// merged ancestor's base, including when those updates arrive out of order.
func chooseParent(pull Candidate, pulls map[int]Candidate, byHead map[string][]int, storedParents map[int]*int) (int, bool) {
	stored := storedParents[pull.Number]
	if !pull.Open {
		if stored != nil {
			if _, ok := pulls[*stored]; ok {
				return *stored, true
			}
		}
		return 0, false
	}
	var matches []int
	for _, number := range byHead[pull.BaseRef] {
		if number != pull.Number && strings.EqualFold(pulls[number].Author, pull.Author) {
			matches = append(matches, number)
		}
	}
	if len(matches) > 0 {
		if stored != nil && slices.Contains(matches, *stored) {
			return *stored, true
		}
		for _, number := range matches {
			if pulls[number].Open {
				return number, true
			}
		}
		return matches[0], true
	}
	if stored != nil {
		seen := map[int]bool{pull.Number: true}
		for ancestor := stored; ancestor != nil && !seen[*ancestor]; ancestor = storedParents[*ancestor] {
			seen[*ancestor] = true
			parent, ok := pulls[*ancestor]
			if !ok {
				break
			}
			if parent.Merged && pull.BaseRef == parent.BaseRef {
				return *stored, true
			}
			// A retarget to an unrelated branch or an unmerged close is a split.
			next := storedParents[*ancestor]
			if !parent.Open || next == nil {
				break
			}
			previous, ok := pulls[*next]
			if !ok || (parent.BaseRef != previous.HeadRef && parent.BaseRef != pull.BaseRef) {
				break
			}
		}
	}
	return 0, false
}

// breakCycles makes the parent links a forest. Branches can name each other
// in a loop; the member of the loop with the lowest number loses its parent.
func breakCycles(numbers []int, parents map[int]int) {
	done := map[int]bool{}
	for _, start := range numbers {
		path := map[int]bool{}
		var order []int
		node := start
		for !done[node] {
			if path[node] {
				cycleStart := slices.Index(order, node)
				delete(parents, slices.Min(order[cycleStart:]))
				break
			}
			path[node] = true
			order = append(order, node)
			parent, ok := parents[node]
			if !ok {
				break
			}
			node = parent
		}
		for _, visited := range order {
			done[visited] = true
		}
	}
}

func connectedComponents(numbers []int, parents map[int]int) [][]int {
	root := map[int]int{}
	var find func(int) int
	find = func(number int) int {
		if parent, ok := root[number]; ok && parent != number {
			root[number] = find(parent)
			return root[number]
		}
		root[number] = number
		return number
	}
	for _, number := range numbers {
		if parent, ok := parents[number]; ok {
			a, b := find(number), find(parent)
			if a != b {
				root[max(a, b)] = min(a, b)
			}
		}
	}
	groups := map[int][]int{}
	for _, number := range numbers {
		top := find(number)
		groups[top] = append(groups[top], number)
	}
	components := make([][]int, 0, len(groups))
	for _, top := range sortedKeys(groups) {
		components = append(components, groups[top])
	}
	return components
}

// assignStacks gives each stored stack to at most one component of two or
// more pull requests, oldest stack first: a component joining several stacks
// keeps the oldest, and a stack that split goes to the part sharing the most
// of its members (then the larger part, then the one with the lowest number).
func assignStacks(components [][]int, stored []StoredStack, storedStack map[int]int) map[int]int {
	order := make([]int, len(stored))
	for index := range order {
		order[index] = index
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if c := stored[a].CreatedAt.Compare(stored[b].CreatedAt); c != 0 {
			return c
		}
		return cmp.Compare(stored[a].ID, stored[b].ID)
	})
	assigned := map[int]int{}
	for _, stackIndex := range order {
		best, bestShared := -1, 0
		for componentIndex, component := range components {
			if _, taken := assigned[componentIndex]; taken || len(component) < 2 {
				continue
			}
			shared := 0
			for _, number := range component {
				if index, ok := storedStack[number]; ok && index == stackIndex {
					shared++
				}
			}
			if shared == 0 {
				continue
			}
			// Components are ordered by lowest number, so the first of equals wins.
			if shared > bestShared || (shared == bestShared && len(component) > len(components[best])) {
				best, bestShared = componentIndex, shared
			}
		}
		if best >= 0 {
			assigned[best] = stackIndex
		}
	}
	return assigned
}

func diffStack(stack StoredStack, component []int, parents map[int]int, pulls map[int]Candidate) (StackChange, bool) {
	change := StackChange{ID: stack.ID}
	current := map[int]*int{}
	for _, member := range stack.Members {
		current[member.Number] = member.Parent
	}
	inComponent := map[int]bool{}
	for _, placement := range placements(component, parents) {
		inComponent[placement.Number] = true
		parent, member := current[placement.Number]
		if !member || !sameParent(parent, placement.Parent) {
			change.Join = append(change.Join, placement)
		}
	}
	for _, member := range stack.Members {
		if !inComponent[member.Number] {
			change.Leave = append(change.Leave, member.Number)
		}
	}
	if ended := openCount(component, pulls) == 0; ended != stack.Ended {
		change.Ended = ptr(ended)
	}
	return change, len(change.Join) > 0 || len(change.Leave) > 0 || change.Ended != nil
}

func placements(component []int, parents map[int]int) []Placement {
	result := make([]Placement, 0, len(component))
	for _, number := range component {
		placement := Placement{Number: number}
		if parent, ok := parents[number]; ok {
			placement.Parent = ptr(parent)
		}
		result = append(result, placement)
	}
	return result
}

func openCount(component []int, pulls map[int]Candidate) int {
	count := 0
	for _, number := range component {
		if pulls[number].Open {
			count++
		}
	}
	return count
}

func sameParent(a, b *int) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sortedKeys[V any](m map[int]V) []int {
	keys := make([]int, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

func ptr[T any](value T) *T { return &value }
