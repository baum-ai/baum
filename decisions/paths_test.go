package decisions

import "testing"

func TestCitedPathsMatchExactlyThenByAUniqueSuffix(t *testing.T) {
	// A path given twice (two commits touching it) is still one match.
	cited := NewCitedPaths([]string{"server/internal/api/handler.go", "ui/src/page.go", "ui/admin/page.go", "README.md", "ui/src/page.go"})
	for file, want := range map[string]string{
		"README.md":                      "README.md",
		"server/internal/api/handler.go": "server/internal/api/handler.go",
		"internal/api/handler.go":        "server/internal/api/handler.go",
		"page.go":                        "", // ambiguous
		"src/page.go":                    "ui/src/page.go",
		"api/other.go":                   "",
		"al/api/handler.go":              "", // not a whole path segment
	} {
		got, ok := cited.Match(file)
		if got != want || ok != (want != "") {
			t.Errorf("Match(%q) = %q, %v; want %q", file, got, ok, want)
		}
	}
}
