package agent

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/baum-ai/baum/bundle"
)

const maxSourceContextBytes = 96 << 10

var sourceDeclaration = regexp.MustCompile(`^(func |type |class |export |def |CREATE |-- name:)`)

// sourceContext selects numbered excerpts of the changed files from the
// checkout in dir (its head and merge-base trees). It budgets each path equally
// before selecting lines, so large files cannot hide later changes. Numbered
// excerpts are evidence, never a substitute for full code.
func sourceContext(dir string, b *bundle.Bundle) string {
	paths := map[string][]bundle.Hunk{}
	for _, h := range b.Diff {
		paths[h.Path] = append(paths[h.Path], h)
	}
	names := make([]string, 0, len(paths))
	for path := range paths {
		names = append(names, path)
	}
	sort.Strings(names)
	var result strings.Builder
	for _, path := range names {
		if !filepath.IsLocal(path) || strings.Contains(path, "\\") {
			continue
		}
		allowance := maxSourceContextBytes / max(len(names), 1)
		for _, root := range []string{"head", "merge-base"} {
			body, err := os.ReadFile(filepath.Join(dir, root, path))
			if err != nil || bytes.IndexByte(body, 0) >= 0 {
				continue
			}
			lines := strings.Split(string(body), "\n")
			selected := map[int]bool{}
			var indices []int
			selectLine := func(i int) {
				if !selected[i] {
					selected[i] = true
					indices = append(indices, i)
				}
			}
			for _, h := range paths[path] {
				start := max(h.StartLine-4, 0)
				for i := start; i < min(start+24, len(lines)); i++ {
					selectLine(i)
				}
			}
			for i, line := range lines {
				if sourceDeclaration.MatchString(line) {
					for j := i; j < min(i+4, len(lines)); j++ {
						selectLine(j)
					}
				}
			}
			header := fmt.Sprintf("\n%s/%s (selected lines; omissions are not evidence of absence):\n", root, path)
			limit := allowance / 4
			if root == "head" {
				limit = allowance * 3 / 4
			}
			if len(header) > limit {
				continue
			}
			result.WriteString(header)
			remaining := limit - len(header)
			for _, i := range indices {
				line := fmt.Sprintf("%d: %s\n", i+1, lines[i])
				if len(line) > remaining {
					continue
				}
				result.WriteString(line)
				remaining -= len(line)
			}
		}
	}
	return result.String()
}

func boundedText(text string, limit int) string {
	text = strings.Map(func(r rune) rune {
		if r < 32 {
			return ' '
		}
		return r
	}, text)
	if len(text) > limit {
		return text[:limit] + "…"
	}
	return text
}
