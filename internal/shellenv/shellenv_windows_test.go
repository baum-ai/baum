package shellenv

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveReadsThePathFromTheRegistry(t *testing.T) {
	t.Setenv("PATH", `C:\only-this-process\bin`)
	t.Setenv("BAUM_SHELLENV_TEST", "kept")
	result, err := Resolve(context.Background(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	path := result.Env["Path"]
	if strings.Contains(path, "only-this-process") {
		t.Fatalf("the inherited PATH was used: %q", path)
	}
	system := strings.ToLower(filepath.Join(os.Getenv("SystemRoot"), "System32"))
	found := false
	for _, dir := range filepath.SplitList(path) {
		if strings.ToLower(strings.TrimRight(dir, `\`)) == system {
			found = true
		}
	}
	if !found {
		t.Fatalf("%s is not on the registry Path %q", system, path)
	}
	if result.Env["BAUM_SHELLENV_TEST"] != "kept" {
		t.Fatal("other inherited variables were not kept")
	}
	if result.Shell != "" {
		t.Fatalf("shell %q", result.Shell)
	}
}
