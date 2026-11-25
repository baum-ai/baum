package orchestrate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/reviewv2"
)

func TestHasPathConcurrent(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "caller.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFacts(testBundle(), []string{root})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for _, p := range []string{"caller.go", "queue/retry.go", "nope.go"} {
				f.hasPath(p)
			}
		}()
	}
	wg.Wait()
	if !f.hasPath("caller.go") || f.hasPath("nope.go") {
		t.Fatal("hasPath wrong")
	}
}

func TestCitesDropUnchangedFiles(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "caller.go"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := newFacts(testBundle(), []string{root})
	kept, dropped := f.cites([]json.RawMessage{json.RawMessage(`{"type":"file","path":"caller.go"}`)})
	if len(kept) != 0 || dropped != 1 {
		t.Fatalf("kept %v dropped %d", kept, dropped)
	}
}

func TestClaimProposalAtomic(t *testing.T) {
	o := &orchestration{}
	var wg sync.WaitGroup
	for i := 0; i < reviewv2.MaxTiles*3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := sectionResult{Propose: &reviewv2.Tile{ID: "t"}}
			o.claimProposal(&r)
		}()
	}
	wg.Wait()
	if len(o.tiles) != reviewv2.MaxTiles {
		t.Fatalf("tiles %d, want %d", len(o.tiles), reviewv2.MaxTiles)
	}
	seen := map[string]bool{}
	for _, tl := range o.tiles {
		if seen[tl.ID] {
			t.Fatalf("duplicate %s", tl.ID)
		}
		seen[tl.ID] = true
	}
}

func TestToolsSectionNoRunTrees(t *testing.T) {
	for _, mcp := range []bool{true, false} {
		if s := toolsSection(mcp); strings.Contains(s, "head/") || strings.Contains(s, "merge-base") {
			t.Errorf("mcp=%v: %s", mcp, s)
		}
	}
}

func TestOverviewKeepsClipsInVideos(t *testing.T) {
	b := testBundle()
	b.Attachments = []bundle.Attachment{{ID: "att_img", ContentType: "image/png"}, {ID: "att_clip", ContentType: "video/mp4"}}
	reply := strings.Replace(overviewReplyJSON, `"screenshots":["nope"],"videos":[]`, `"screenshots":["att_img","att_clip"],"videos":["att_clip","att_img","nope"]`, 1)
	out, problem := checkOverview([]byte(reply), newFacts(b, nil), []reviewv2.Section{{ID: "sec_api"}}, nil, false)
	if problem != "" {
		t.Fatal(problem)
	}
	if got := out.Overview.Screenshots; len(got) != 1 || got[0] != "att_img" {
		t.Errorf("screenshots = %v, want the image only", got)
	}
	if got := out.Overview.Videos; len(got) != 1 || got[0] != "att_clip" {
		t.Errorf("videos = %v, want the clip only", got)
	}
}
