package cursortranscript

import (
	"os"
	"testing"
)

// Opt-in: set BAUM_CURSOR_STORE to a Cursor state.vscdb.
const corpusEnv = "BAUM_CURSOR_STORE"

// TestCorpus decodes the entire local Cursor database when explicitly enabled:
//
//	BAUM_CURSOR_STORE=~/.config/Cursor/User/globalStorage/state.vscdb \
//	  go test -count=1 -run TestCorpus ./internal/cursortranscript/
func TestCorpus(t *testing.T) {
	path := os.Getenv(corpusEnv)
	if path == "" {
		t.Skipf("set %s to a Cursor state.vscdb to run this", corpusEnv)
	}
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	shape, err := store.Survey()
	if err != nil {
		t.Fatalf("surveying store: %v", err)
	}
	discovery, err := store.Discover()
	if err != nil {
		t.Fatal(err)
	}
	var trees, diffs, codeBlocks int
	for _, ref := range discovery.Roots() {
		if !ref.HasData {
			continue
		}
		tree, err := store.Tree(ref.ComposerID)
		if err != nil {
			t.Errorf("composer %s: %v", ref.ComposerID, err)
			continue
		}
		body, err := EncodeTreePayload(tree)
		if err != nil {
			t.Errorf("encoding composer %s: %v", ref.ComposerID, err)
			continue
		}
		decoded, err := DecodeHostedTreePayload(body)
		if err != nil {
			t.Errorf("decoding composer %s payload: %v", ref.ComposerID, err)
			continue
		}
		var treeDiffs, treeCodeBlocks int
		for _, composer := range tree.Composers() {
			treeDiffs += len(composer.Diffs)
			treeCodeBlocks += len(composer.Data.CodeBlocks)
		}
		var decodedDiffs, decodedCodeBlocks int
		for _, composer := range decoded.Composers() {
			decodedDiffs += len(composer.Diffs)
			decodedCodeBlocks += len(composer.Data.CodeBlocks)
		}
		if decodedDiffs != treeDiffs || decodedCodeBlocks != treeCodeBlocks {
			t.Fatalf("composer %s payload counts changed: diffs %d/%d, code blocks %d/%d", ref.ComposerID, decodedDiffs, treeDiffs, decodedCodeBlocks, treeCodeBlocks)
		}
		diffs += treeDiffs
		codeBlocks += treeCodeBlocks
		trees++
	}
	t.Logf("trees=%d discovered=%d diffs=%d code_blocks=%d junk=%d shape=%+v", trees, len(discovery.Sessions), diffs, codeBlocks, discovery.JunkRows, shape)
}
