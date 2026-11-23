package themes

import (
	"slices"
	"testing"
)

func TestSeedsAndRolesPartitionTheAllowlist(t *testing.T) {
	roles := Roles()
	if len(Seeds)+len(roles) != len(Tokens) {
		t.Fatalf("%d seeds and %d roles for %d tokens", len(Seeds), len(roles), len(Tokens))
	}
	for _, seed := range Seeds {
		if !Tokens[seed] || slices.Contains(roles, seed) {
			t.Errorf("seed %s", seed)
		}
	}
	if !slices.IsSorted(roles) {
		t.Error("roles are not sorted")
	}
}

func TestHex(t *testing.T) {
	for in, want := range map[string]string{"#102030": "#102030", "#10203080": "#10203080", "#fff": "#ffffff"} {
		c, err := ParseColor(in)
		if err != nil || c.Hex() != want {
			t.Errorf("%s: %s %v", in, c.Hex(), err)
		}
	}
	colours, err := Resolve("light", nil)
	if err != nil || colours["--surface-page"].Hex() == "" {
		t.Fatal(err)
	}
}
