package cursortranscript

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/lastpersonlabs/baum/internal/goldentest"
)

func TestDiscoverAndRoots(t *testing.T) {
	discovery, err := fixtureStore(t).Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(discovery.Sessions) != 6 {
		t.Fatalf("sessions = %+v", discovery.Sessions)
	}
	for _, id := range []string{"child", "grandchild"} {
		ref, ok := discovery.Session(id)
		if !ok || !ref.IsSubagent || !ref.HasData || !ref.HasHeader {
			t.Fatalf("subagent %s = %+v", id, ref)
		}
	}
	headerless, _ := discovery.Session("headerless")
	if !headerless.HasData || headerless.HasHeader {
		t.Fatalf("headerless = %+v", headerless)
	}
	dataless, _ := discovery.Session("data-less")
	if dataless.HasData || !dataless.HasHeader {
		t.Fatalf("data-less = %+v", dataless)
	}
	var roots []string
	for _, root := range discovery.Roots() {
		roots = append(roots, root.ComposerID)
	}
	for _, want := range []string{"root", "headerless", "draft", "data-less"} {
		if !slices.Contains(roots, want) {
			t.Errorf("roots %v do not contain %s", roots, want)
		}
	}
}

func TestTreeUsesHeaderOrderAndSurfacesUnlisted(t *testing.T) {
	store := fixtureStore(t)
	tree, err := store.Tree("root")
	if err != nil {
		t.Fatal(err)
	}
	if got := tree.Composer.Bubbles[0].BubbleID + "," + tree.Composer.Bubbles[1].BubbleID; got != "u1,think" {
		t.Fatalf("first bubbles = %s; timestamps overrode header order", got)
	}
	if len(tree.Composer.Unlisted) != 1 || tree.Composer.Unlisted[0].BubbleID != "unlisted" {
		t.Fatalf("unlisted = %+v", tree.Composer.Unlisted)
	}
	if len(tree.DanglingChildren) != 1 || tree.DanglingChildren[0] != "dangling" {
		t.Fatalf("dangling children = %v", tree.DanglingChildren)
	}
	if len(tree.Children) != 1 || tree.Children[0].ComposerID() != "child" ||
		len(tree.Children[0].Children) != 1 || tree.Children[0].Children[0].ComposerID() != "grandchild" {
		t.Fatalf("tree = %+v", tree)
	}
	if len(tree.Composer.Diffs) != 2 || tree.Composer.Diffs[0].DiffID != "11111111-1111-4111-8111-111111111111" ||
		len(tree.Composer.Data.CodeBlocks) != 2 || tree.Composer.Data.CodeBlocks[0].BubbleID != "mixed" ||
		tree.Composer.Data.CodeBlocks[0].Status != "accepted" || tree.Composer.Data.CodeBlocks[1].Status != "completed" {
		t.Fatalf("diffs/code blocks = %+v / %+v", tree.Composer.Diffs, tree.Composer.Data.CodeBlocks)
	}
	discovery, err := store.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if discovery.JunkRows != 2 {
		t.Fatalf("junk rows = %d, want 2", discovery.JunkRows)
	}
}

func TestSurveyClassifiesAllFixtureRows(t *testing.T) {
	shape, err := fixtureStore(t).Survey()
	if err != nil {
		t.Fatal(err)
	}
	if shape.ComposerVersions["17"] != 5 || shape.BubbleTypes["1"] != 5 || shape.BubbleTypes["2"] != 15 {
		t.Fatalf("shape = %+v", shape)
	}
	for _, status := range []string{"completed", "error", "cancelled", "loading", "degenerate"} {
		if shape.ToolStatuses[status] == 0 {
			t.Errorf("tool status %s missing: %+v", status, shape.ToolStatuses)
		}
	}
	if shape.KeyFamilies["messageRequestContext"] != 1 || shape.JunkRows < 2 {
		t.Fatalf("key families/junk = %+v / %d", shape.KeyFamilies, shape.JunkRows)
	}
	if shape.ComposersWithBubblesNoData != 1 || shape.BubblesWithoutComposerData != 1 {
		t.Fatalf("bubbles without composer data = %+v", shape)
	}
	goldentest.Golden(t, filepath.Join("testdata", "golden", "fixture_shape.json"), shape)
}
