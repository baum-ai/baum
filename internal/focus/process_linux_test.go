package focus

import (
	"os"
	"testing"

	"github.com/baum-ai/baum/internal/hookevents"
)

func TestParseStat(t *testing.T) {
	p, parent, ok := parseStat(42, "42 (tmux: server (x)) S 1 42 42 0 -1 4194560 1 0 0 0 1 2 0 0 20 0 1 0 21341 100 200")
	if !ok || parent != 1 || p != (hookevents.Process{PID: 42, Start: "21341", Name: "tmux: server (x)"}) {
		t.Fatalf("got %+v %d %v", p, parent, ok)
	}
	if _, _, ok := parseStat(42, "42 (short) S 1"); ok {
		t.Fatal("parsed a short line")
	}
}

func TestAncestorsReadThisProcess(t *testing.T) {
	chain := ancestors(os.Getpid())
	if len(chain) == 0 || chain[0].PID != os.Getpid() || chain[0].Start == "" || chain[0].Name == "" {
		t.Fatalf("ancestors = %+v", chain)
	}
	if now, _, ok := readProcess(os.Getpid()); !ok || now != chain[0] {
		t.Fatalf("read again: %+v", now)
	}
}
