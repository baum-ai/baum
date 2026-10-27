package gitlocal

import (
	"slices"
	"strings"
	"sync"
)

// renamed maps earlier github.com "owner/name"s that GitHub forwards after a
// rename or transfer to the repository's current one, as the registry
// recorded them. Remote URLs using an earlier name read as the current name
// everywhere, so a clone whose remote was never updated still counts as a
// clone of the repository.
var renamed struct {
	sync.RWMutex
	to map[string]string
}

// AddRenamed records that GitHub forwards the repository "owner/name" from to
// "owner/name" to. Names forwarded to from now forward to to, and to itself is
// current again: renaming back undoes the earlier rename.
func AddRenamed(from, to string) {
	from, to = strings.ToLower(from), strings.ToLower(to)
	renamed.Lock()
	defer renamed.Unlock()
	if renamed.to == nil {
		renamed.to = map[string]string{}
	}
	delete(renamed.to, to)
	for earlier, current := range renamed.to {
		if current == from {
			renamed.to[earlier] = to
		}
	}
	renamed.to[from] = to
}

// RenamedTo is the current lower-case "owner/name" of repository: the one
// GitHub forwards it to when it is a recorded earlier name, else repository.
func RenamedTo(repository string) string {
	repository = strings.ToLower(repository)
	renamed.RLock()
	defer renamed.RUnlock()
	if to, ok := renamed.to[repository]; ok {
		return to
	}
	return repository
}

// EarlierNames lists the recorded earlier names of the repository "owner/name",
// sorted.
func EarlierNames(repository string) []string {
	repository = strings.ToLower(repository)
	renamed.RLock()
	defer renamed.RUnlock()
	var names []string
	for from, to := range renamed.to {
		if to == repository {
			names = append(names, from)
		}
	}
	slices.Sort(names)
	return names
}
