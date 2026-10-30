//go:build windows

package livesessions

import "errors"

// running is not asked on Windows, where the daemon neither reaches nor ends
// a Claude Code session (its inbox there is a named pipe).
func running(int) bool { return false }

func terminate(int) error { return errors.ErrUnsupported }

func sameProcess(ClaudeProcess) error { return errors.ErrUnsupported }
