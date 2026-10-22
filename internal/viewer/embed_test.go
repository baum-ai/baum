package viewer

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"

	"github.com/baum-ai/baum/internal/uibuild"
)

var (
	reference = regexp.MustCompile(`(?i)\b(?:src|href)\s*=\s*"([^"]*)"`)
	inline    = regexp.MustCompile(`(?is)<script\b[^>]*>(.*?)</script>`)
	styled    = regexp.MustCompile(`(?i)<style\b|\bstyle\s*=`)
	// Vite names a chunk another one imports as "./name-HASH.js", and a lazy
	// chunk as "assets/name-HASH.js".
	chunk = regexp.MustCompile(`["'](\./|assets/)([A-Za-z0-9_.-]+\.(?:js|css|woff2))["']`)
)

func readIndex(t *testing.T) string {
	t.Helper()
	index, err := fs.ReadFile(Assets(), "index.html")
	if err != nil {
		t.Fatalf("index.html: %v", err)
	}
	return string(index)
}

func TestIndexReferencesAreSameOriginFiles(t *testing.T) {
	uibuild.Require(t, Assets())
	assets := Assets()
	matches := reference.FindAllStringSubmatch(readIndex(t), -1)
	if len(matches) == 0 {
		t.Fatal("index.html references no assets")
	}
	for _, match := range matches {
		ref := match[1]
		if !strings.HasPrefix(ref, "/") || strings.HasPrefix(ref, "//") {
			t.Errorf("%q is not a /-rooted same-origin path", ref)
			continue
		}
		if _, err := fs.Stat(assets, path.Clean(strings.TrimPrefix(ref, "/"))); err != nil {
			t.Errorf("%q does not resolve: %v", ref, err)
		}
	}
}

// The server sends a strict default-src 'self' policy, which blocks inline
// scripts and styles.
func TestIndexHasNoInlineCode(t *testing.T) {
	uibuild.Require(t, Assets())
	index := readIndex(t)
	for _, match := range inline.FindAllStringSubmatch(index, -1) {
		if strings.TrimSpace(match[1]) != "" {
			t.Errorf("inline script: %.60q", match[1])
		}
	}
	if styled.MatchString(index) {
		t.Error("index.html has a <style> element or a style attribute")
	}
}

func TestChunkImportsResolve(t *testing.T) {
	uibuild.Require(t, Assets())
	assets := Assets()
	checked := 0
	err := fs.WalkDir(assets, "assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".js") {
			return err
		}
		source, err := fs.ReadFile(assets, name)
		if err != nil {
			return err
		}
		for _, match := range chunk.FindAllStringSubmatch(string(source), -1) {
			checked++
			// Both forms name a file in the assets directory.
			if _, err := fs.Stat(assets, "assets/"+match[2]); err != nil {
				t.Errorf("%s imports %s%s, which does not resolve", name, match[1], match[2])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Error("found no chunk imports to check; the pattern no longer matches the build")
	}
}

func TestNoSourceMaps(t *testing.T) {
	uibuild.Require(t, Assets())
	err := fs.WalkDir(Assets(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".map") {
			t.Errorf("source map %s is embedded", name)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The server's Content-Security-Policy has no 'unsafe-eval', so a script that
// builds code from a string fails at run time and leaves the page blank. Ajv
// does this when it compiles a schema, so the viewer ships precompiled
// validators; see scripts/review-tile-validators.mjs in the viewer package.
func TestNoRuntimeCodeGeneration(t *testing.T) {
	uibuild.Require(t, Assets())
	evaluates := regexp.MustCompile(`new Function\(|(?:^|[^\w$.])eval\(|Error compiling schema`)
	err := fs.WalkDir(Assets(), "assets", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(name, ".js") {
			return err
		}
		source, err := fs.ReadFile(Assets(), name)
		if err != nil {
			return err
		}
		if match := evaluates.Find(source); match != nil {
			t.Errorf("%s evaluates strings as code (%q), which the CSP blocks", name, match)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
