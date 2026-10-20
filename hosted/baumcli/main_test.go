//go:build !windows

package baumcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
)

// runAsBaumEnv makes the test binary behave as the baum command, so tests can
// put it on PATH and run the dispatchers committed to customer repositories
// exactly as a shell would.
const runAsBaumEnv = "BAUMCLI_TEST_RUN_AS_BAUM"

// testDistributionEnv is the build distribution the test binary acting as
// baum takes, which a release build gets from -X.
const testDistributionEnv = "BAUMCLI_TEST_DISTRIBUTION"

// TestMain keeps the developer's own login out of the tests: credentials are
// read from an empty config directory and the token and server overrides are
// cleared, so a test that wants a login sets one up itself.
func TestMain(m *testing.M) {
	if os.Getenv(runAsBaumEnv) == "1" {
		// What -X sets in a release build.
		distribution = os.Getenv(testDistributionEnv)
		os.Exit(runAsBaum())
	}
	config, err := os.MkdirTemp("", "baumcli-config-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("XDG_CONFIG_HOME", config)
	_ = os.Unsetenv(tokenEnvVar)
	_ = os.Unsetenv(serverEnvVar)
	code := m.Run()
	_ = os.RemoveAll(config)
	os.Exit(code)
}

// runAsBaum is the hosted part of the baum command's main: a delegated hook's
// status passes through silently, any other failure is printed and exits 1.
func runAsBaum() int {
	err := runContext(context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	var exitErr *ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exitErr):
		return exitErr.Code
	default:
		fmt.Fprintln(os.Stderr, "baum:", err)
		return 1
	}
}
