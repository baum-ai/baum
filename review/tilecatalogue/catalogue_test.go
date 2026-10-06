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
