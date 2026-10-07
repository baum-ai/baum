package viewer

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
	"testing"
)

var reference = regexp.MustCompile(`(?:src|href)="(/[^"]*)"`)

func TestIndexExists(t *testing.T) {
	if _, err := fs.Stat(Assets(), "index.html"); err != nil {
		t.Fatalf("index.html: %v", err)
	}
}

func TestIndexReferencesResolve(t *testing.T) {
	assets := Assets()
	index, err := fs.ReadFile(assets, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	matches := reference.FindAllStringSubmatch(string(index), -1)
	if len(matches) == 0 {
		t.Fatal("index.html references no assets")
	}
	for _, match := range matches {
		name := strings.TrimPrefix(match[1], "/")
		if strings.HasPrefix(name, "/") {
			t.Errorf("%s is not a same-origin path", match[1])
			continue
		}
		if _, err := fs.Stat(assets, path.Clean(name)); err != nil {
			t.Errorf("%s does not resolve: %v", match[1], err)
		}
	}
}

func TestNoSourceMaps(t *testing.T) {
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
