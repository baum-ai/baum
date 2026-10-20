//go:build !windows

package baumcli

import (
	"errors"
	"testing"
)

func TestDefaultTokenName(t *testing.T) {
	originalHostname := hostname
	t.Cleanup(func() { hostname = originalHostname })

	hostname = func() (string, error) { return "test-laptop", nil }
	if got, want := defaultTokenName(), "baum-cli@test-laptop"; got != want {
		t.Errorf("defaultTokenName() = %q, want %q", got, want)
	}

	hostname = func() (string, error) { return "", errors.New("unavailable") }
	if got, want := defaultTokenName(), "baum-cli"; got != want {
		t.Errorf("defaultTokenName() = %q, want %q", got, want)
	}
}
