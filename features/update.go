package features

// PathChange compares the paths a pull request was assigned with to those
// it touches now: how many paths are in one set only, and how many are in
// either.
func PathChange(assigned, current []string) (differing, total int) {
	inAssigned := map[string]bool{}
	for _, path := range assigned {
		inAssigned[path] = true
	}
	inCurrent := map[string]bool{}
	for _, path := range current {
		inCurrent[path] = true
	}
	for path := range inAssigned {
		if !inCurrent[path] {
			differing++
		}
	}
	for path := range inCurrent {
		if !inAssigned[path] {
			differing++
		}
	}
	both := 0
	for path := range inAssigned {
		if inCurrent[path] {
			both++
		}
	}
	return differing, differing + both
}

// LargeUpdate reports whether a pull request's paths changed enough since
// its assignment to assign it again: the path change (1 - |A ∩ B| / |A ∪ B|)
// is at least minChange and at least minPaths paths differ. A host that
// selects pending items in its store applies the same rule.
func LargeUpdate(assigned, current []string, minChange float64, minPaths int) bool {
	differing, total := PathChange(assigned, current)
	return differing >= max(minPaths, 1) && float64(differing) >= minChange*float64(total)
}
