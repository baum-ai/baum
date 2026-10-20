package tilecatalogue

import (
	"slices"
	"strings"
	"testing"
)

func TestValidateAcceptsCatalogueKindsOnly(t *testing.T) {
	if err := Validate(nil); err != nil {
		t.Fatalf("Validate(nil) = %v, want no tiles to be valid", err)
	}
	if err := Validate(Kinds()); err != nil {
		t.Fatalf("Validate(every kind) = %v", err)
	}
	for _, tc := range []struct {
		kinds []string
		want  string
	}{
		{[]string{"bug-fix", "refactor"}, `unknown tile kind "refactor"`},
		{[]string{""}, `unknown tile kind ""`},
		{[]string{"bug-fix", "bug-fix"}, `"bug-fix" is listed twice`},
	} {
		if err := Validate(tc.kinds); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("Validate(%q) = %v, want %s", tc.kinds, err, tc.want)
		}
	}
}

func TestEntriesFollowCatalogueOrder(t *testing.T) {
	var got []string
	for _, entry := range Entries([]string{"bug-fix", "database-schema"}) {
		got = append(got, entry.Kind)
		if !strings.Contains(string(entry.Raw), `"use_when"`) {
			t.Fatalf("entry %s lost its guidance: %s", entry.Kind, entry.Raw)
		}
	}
	if want := []string{"database-schema", "bug-fix"}; !slices.Equal(got, want) {
		t.Fatalf("Entries() kinds = %v, want %v", got, want)
	}
	if len(Entries(nil)) != 0 {
		t.Fatal("Entries(nil) offered kinds")
	}
}

func TestWithCandidatesReplacesOfferedEntriesOnly(t *testing.T) {
	candidates, err := ParseEntries([]byte(`[{"kind":"bug-fix","use_when":"candidate"}]`))
	if err != nil {
		t.Fatal(err)
	}
	got, err := WithCandidates([]string{"database-schema", "bug-fix"}, candidates)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Kind != "database-schema" || string(got[0].Raw) != string(Entries([]string{"database-schema"})[0].Raw) || got[1].Kind != "bug-fix" || !strings.Contains(string(got[1].Raw), "candidate") {
		t.Fatalf("WithCandidates() = %+v", got)
	}
	if text := InstructionsFor(got); !strings.Contains(text, "candidate") || !strings.Contains(text, "Tile catalogue") {
		t.Fatalf("InstructionsFor() = %s", text)
	}
	if InstructionsFor(Entries([]string{"bug-fix"})) != Instructions([]string{"bug-fix"}) {
		t.Fatal("InstructionsFor(shipped) differs from Instructions")
	}
	for name, tc := range map[string]struct {
		kinds      []string
		candidates string
		want       string
	}{
		"not offered":     {[]string{"database-schema"}, `[{"kind":"bug-fix"}]`, "not in the offered"},
		"unknown":         {[]string{"database-schema"}, `[{"kind":"nope"}]`, "unknown tile kind"},
		"twice":           {[]string{"bug-fix"}, `[{"kind":"bug-fix"},{"kind":"bug-fix"}]`, "twice"},
		"unknown offered": {[]string{"nope"}, `[]`, "unknown tile kind"},
	} {
		entries, err := ParseEntries([]byte(tc.candidates))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := WithCandidates(tc.kinds, entries); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%s: WithCandidates() = %v, want %s", name, err, tc.want)
		}
	}
	if _, err := ParseEntries([]byte(`[{"use_when":"x"}]`)); err == nil {
		t.Fatal("an entry without a kind parsed")
	}
}
