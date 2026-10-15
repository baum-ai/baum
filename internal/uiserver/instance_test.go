package uiserver

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSecondInstanceReusesLiveServerAndStaleMetadataIsReplaced(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	ctx, stop := context.WithCancel(context.Background())
	first, err := Start(ctx, directory, 0, Options{})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- first.Server.Serve(ctx) }()
	second, err := Start(context.Background(), directory, 0, Options{})
	if err != nil {
		stop()
		t.Fatal(err)
	}
	if !second.Existing || second.Server != nil || second.URL != first.URL {
		t.Fatalf("second %+v", second)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(first.path); err != nil {
		t.Fatal("second removed owner's metadata", err)
	}
	stop()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	// A crash may leave metadata; the unlocked SQLite file is authoritative.
	if err = os.WriteFile(filepath.Join(directory, "ui-instance.json"), []byte(`{"url":"http://127.0.0.1:1/#token=`+strings.Repeat("a", 43)+`"}`), 0600); err != nil {
		t.Fatal(err)
	}
	replacement, err := Start(context.Background(), directory, 0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	defer replacement.Server.listener.Close()
	if replacement.Existing || replacement.URL == first.URL {
		t.Fatal("did not replace stale instance")
	}
}
func TestPrivateInstanceDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "public")
	if err := os.Mkdir(directory, 0755); err != nil {
		t.Fatal(err)
	}
	if instance, err := Start(context.Background(), directory, 0, Options{}); err == nil {
		_ = instance.Close()
		t.Fatal("accepted public capability directory")
	}
}
func TestStartCancelledWhileLockOwnerNotServing(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private")
	owner, err := Start(context.Background(), directory, 0, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	defer owner.Server.listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if other, err := Start(ctx, directory, 0, Options{}); err == nil {
		_ = other.Close()
		t.Fatal("cancel ignored")
	}
}
