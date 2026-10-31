// Package appdbtest provides private directories for application database tests.
package appdbtest

import (
	"path/filepath"
	"testing"

	"github.com/baum-ai/baum/internal/privatefs"
)

func TempDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "private")
	if err := privatefs.MkdirAll(dir); err != nil {
		t.Fatal(err)
	}
	return dir
}
