package kit_test

import (
	"os"
	"regexp"
	"slices"
	"testing"

	"github.com/baum-ai/baum/internal/themes"
)

// The bootstrap checks the colour tokens a theme sends against a copy of the
// allowlist in internal/themes. The copy has to be the same list.
func TestBootstrapThemeTokensMatchThemes(t *testing.T) {
	source, err := os.ReadFile("v1/bootstrap.js")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)const THEME_TOKENS = new Set\(\[(.*?)\]\);`).FindSubmatch(source)
	if block == nil {
		t.Fatal("no THEME_TOKENS in v1/bootstrap.js")
	}
	var got []string
	for _, m := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(block[1], -1) {
		got = append(got, string(m[1]))
	}
	var want []string
	for token := range themes.Tokens {
		want = append(want, token)
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("THEME_TOKENS in v1/bootstrap.js is %v, want themes.Tokens %v", got, want)
	}
}
