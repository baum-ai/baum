//go:build unix

package githubauth

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCredentialHelperRejectsShellParent(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", "-c", `"$1" -test.run=^TestCredentialHelperParent$; result=$?; exit "$result"`, "shell", executable)
	cmd.Env = append(os.Environ(), "BAUM_TEST_CREDENTIAL_PARENT=deny")
	cmd.Stdin = strings.NewReader(`{"operation":"load"}`)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatal(err, string(output))
	}
}

func TestProcessStoreCancellation(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, "late-save")
	executable := filepath.Join(dir, "keyring-helper")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\n(sleep 0.3; echo saved > "+marker+") &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := (ProcessStore{Executable: executable}).Load(ctx)
	if !errors.Is(err, ErrStore) {
		t.Fatal(err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("helper or descendant survived cancellation")
	}
	time.Sleep(400 * time.Millisecond)
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("native child saved after helper cancellation", err)
	}
}
func TestProcessStoreSanitizesOutput(t *testing.T) {
	for _, body := range []string{"echo secret-detail >&2; exit 1", "echo garbage-secret", "printf '{\"error\":\"secret-detail\"}'"} {
		executable := filepath.Join(t.TempDir(), "keyring-helper")
		if err := os.WriteFile(executable, []byte("#!/bin/sh\n"+body+"\n"), 0700); err != nil {
			t.Fatal(err)
		}
		if _, err := (ProcessStore{Executable: executable}).Load(context.Background()); !errors.Is(err, ErrStore) {
			t.Fatal(err)
		}
	}
}
