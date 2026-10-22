//go:build !linux && !darwin && !windows

package githubauth

func parentExecutable(int) (string, error) { return "", ErrStore }
