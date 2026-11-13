package cursortranscript

import "testing"

func TestNameAndRevisionReadOnlyTheComposerRecord(t *testing.T) {
	store := fixtureStore(t)
	for _, id := range []string{"root", "child", "headerless"} {
		tree, err := store.Tree(id)
		if err != nil {
			t.Fatal(err)
		}
		name, err := store.Name(id)
		if err != nil || name != tree.Composer.Data.Name || name == "" {
			t.Fatalf("Name(%s) = %q, %v; the tree says %q", id, name, err, tree.Composer.Data.Name)
		}
	}
	a, err := store.Revision("root")
	if err != nil {
		t.Fatal(err)
	}
	again, _ := store.Revision("root")
	b, _ := store.Revision("child")
	if a == "" || a != again || a == b {
		t.Fatalf("revisions %q %q %q", a, again, b)
	}
	ra, err := store.RecordRevision("root")
	if err != nil || ra == "" || ra == a {
		t.Fatalf("record revision %q, %v", ra, err)
	}
	for _, id := range []string{"data-less", "nope"} {
		if _, err := store.Name(id); err == nil {
			t.Fatalf("Name(%s) found a record", id)
		}
		if _, err := store.Revision(id); err == nil {
			t.Fatalf("Revision(%s) found a record", id)
		}
	}
}
