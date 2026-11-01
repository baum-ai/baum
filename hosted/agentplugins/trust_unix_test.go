//go:build unix

package agentplugins

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	if mode := os.Getenv("BAUM_TEST_HOOK_RPC"); mode != "" {
		scan := bufio.NewScanner(os.Stdin)
		log, _ := os.OpenFile(os.Getenv("BAUM_TEST_HOOK_LOG"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		for scan.Scan() {
			var msg struct {
				ID     *int
				Method string
				Params map[string]any
			}
			_ = json.Unmarshal(scan.Bytes(), &msg)
			if log != nil {
				_, _ = log.WriteString(msg.Method + "\n")
			}
			if msg.Method == "hooks/list" && mode == "hang" {
				time.Sleep(time.Minute)
			}
			if msg.ID == nil {
				continue
			}
			result := any(map[string]any{})
			if msg.Method == "hooks/list" {
				result = hookInventory("trusted")
			}
			if msg.Method == "config/read" {
				result = map[string]any{"config": map[string]any{"features": map[string]bool{"hooks": mode != "disabled"}}}
			}
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"id": *msg.ID, "result": result})
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestCodexHookInventoryProtocolAndCancellation(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	if err := os.Symlink(exe, filepath.Join(bin, "codex")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("BAUM_TEST_HOOK_RPC", "respond")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("BAUM_TEST_HOOK_LOG", log)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	data, err := codexHookInventory(ctx, []string{"/selected/repo"})
	if err != nil || parseHookTrust(data).State != "trusted" {
		t.Fatalf("inventory=%s %v", data, err)
	}
	calls, _ := os.ReadFile(log)
	if string(calls) != "initialize\ninitialized\nconfig/read\nhooks/list\n" {
		t.Fatalf("unexpected RPCs: %s", calls)
	}
	t.Setenv("BAUM_TEST_HOOK_RPC", "disabled")
	disabled, err := codexHookInventory(ctx, []string{"/selected/repo"})
	if err != nil || parseHookTrust(disabled).State != "hooks_disabled" {
		t.Fatalf("disabled=%s %v", disabled, err)
	}
	t.Setenv("BAUM_TEST_HOOK_RPC", "hang")
	short, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer stop()
	start := time.Now()
	if _, err := codexHookInventory(short, []string{"/selected/repo"}); err == nil {
		t.Fatal("hung child succeeded")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("cancellation did not stop child")
	}
}
