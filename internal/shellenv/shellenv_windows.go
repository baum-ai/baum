package shellenv

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"golang.org/x/sys/windows"
)

func detach(*exec.Cmd)          {}
func killGroup(*exec.Cmd)       {}
func detectShell(string) string { return "" }

// registryPath is the Path a new logon of this user starts with: the system
// Path and then the user's, read from the registry with their variables
// expanded, as CreateEnvironmentBlock builds it for Explorer.
func registryPath() (string, error) {
	var token windows.Token
	if err := windows.OpenProcessToken(windows.CurrentProcess(), windows.TOKEN_QUERY|windows.TOKEN_DUPLICATE, &token); err != nil {
		return "", fmt.Errorf("open the process token: %w", err)
	}
	defer func() { _ = token.Close() }()
	block, err := token.Environ(false)
	if err != nil {
		return "", fmt.Errorf("read the environment from the registry: %w", err)
	}
	for _, entry := range block {
		if name, value, ok := strings.Cut(entry, "="); ok && strings.EqualFold(name, "PATH") && value != "" {
			return value, nil
		}
	}
	return "", errors.New("the registry environment has no Path")
}
