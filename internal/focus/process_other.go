//go:build !linux && !darwin

package focus

import "github.com/baum-ai/baum/internal/hookevents"

// readProcess reads nothing where focusing is not supported.
func readProcess(int) (hookevents.Process, int, bool) {
	return hookevents.Process{}, 0, false
}

func controllingTTY(int) string { return "" }

func executable(int) string { return "" }
