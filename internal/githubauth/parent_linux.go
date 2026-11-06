package githubauth

import "strconv"

func parentExecutable(pid int) (string, error) {
	return "/proc/" + strconv.Itoa(pid) + "/exe", nil
}

// selfExecutable is the file this process runs. An update renames a new baum
// over the daemon's path while the daemon keeps running the old one; the path
// then names the new file, but /proc/self/exe still opens the running one.
func selfExecutable() (string, error) { return "/proc/self/exe", nil }
