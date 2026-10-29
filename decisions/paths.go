package decisions

import "strings"

// CitedPaths matches the files a decision cites against a set of repository
// paths, such as a diff's. A decision may cite a file relative to the
// session's working directory rather than to the repository root.
type CitedPaths struct {
	paths []string
	set   map[string]bool
}

// NewCitedPaths indexes paths; a path given twice counts once.
func NewCitedPaths(paths []string) CitedPaths {
	set := make(map[string]bool, len(paths))
	unique := make([]string, 0, len(paths))
	for _, path := range paths {
		if !set[path] {
			set[path] = true
			unique = append(unique, path)
		}
	}
	return CitedPaths{paths: unique, set: set}
}

// Match returns the path a cited file names: the file itself when it is one
// of the paths, else the one path ending in "/" + file. A file that matches
// no path, or several by suffix, matches none.
func (c CitedPaths) Match(file string) (string, bool) {
	if c.set[file] {
		return file, true
	}
	match := ""
	for _, candidate := range c.paths {
		if strings.HasSuffix(candidate, "/"+file) {
			if match != "" {
				return "", false
			}
			match = candidate
		}
	}
	return match, match != ""
}
