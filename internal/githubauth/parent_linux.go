package githubauth

import "strconv"

func parentExecutable(pid int) (string, error) {
	return "/proc/" + strconv.Itoa(pid) + "/exe", nil
}
