//go:build !linux && !darwin && !windows

package livesessions

import "errors"

// sameProcess cannot tell where Claude Code's start time is not known.
func sameProcess(p ClaudeProcess) error {
	if p.ProcStart == "" {
		return nil
	}
	return errors.ErrUnsupported
}
