//go:build !linux && !darwin && !windows

package credentialstore

func parentExecutable(int) (string, error) { return "", ErrStore }
