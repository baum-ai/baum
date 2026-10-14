package codex

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const codexImport = "github.com/baumai/baum/agent/codex"

// violations reports the ways a Go source file could select the unsandboxed
// Container profile of this package, however it spells the import:
//   - any selector X.Container, X being an import of this package;
//   - a conversion X.Profile(v) of anything but the constant 0, which is Local;
//   - a Profile field of an X.Backend literal that is not X.Local;
//   - the string danger-full-access.
func violations(t *testing.T, name, source string) []string {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), name, source, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var names []string
	for _, spec := range file.Imports {
		path, _ := strconv.Unquote(spec.Path.Value)
		if path != codexImport {
			continue
		}
		local := "codex"
		if spec.Name != nil {
			local = spec.Name.Name
		}
		names = append(names, local)
	}
	isPackage := func(expr ast.Expr, member string) bool {
		selector, ok := expr.(*ast.SelectorExpr)
		if !ok || selector.Sel.Name != member {
			return false
		}
		ident, ok := selector.X.(*ast.Ident)
		if !ok {
			return false
		}
		for _, local := range names {
			if ident.Name == local {
				return true
			}
		}
		return false
	}
	var found []string
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.SelectorExpr:
			if isPackage(n, "Container") {
				found = append(found, "selects Container")
			}
		case *ast.CallExpr:
			if isPackage(n.Fun, "Profile") && (len(n.Args) != 1 || !isZero(n.Args[0])) {
				found = append(found, "converts to Profile")
			}
		case *ast.CompositeLit:
			if !isPackage(n.Type, "Backend") {
				return true
			}
			for _, element := range n.Elts {
				pair, ok := element.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				if key, ok := pair.Key.(*ast.Ident); ok && key.Name == "Profile" && !isPackage(pair.Value, "Local") {
					found = append(found, "sets Backend.Profile to something other than Local")
				}
			}
		case *ast.BasicLit:
			if n.Kind == token.STRING && strings.Contains(n.Value, "danger-full-access") {
				found = append(found, "names danger-full-access")
			}
		}
		return true
	})
	return found
}

func isZero(expr ast.Expr) bool {
	literal, ok := expr.(*ast.BasicLit)
	return ok && literal.Kind == token.INT && literal.Value == "0"
}

// The unsandboxed Container profile exists for a host that supplies its own
// boundary. Nothing a user runs locally (the CLI and its internals) may name
// it or ask Codex for the unsandboxed mode.
func TestLocalCodePathsNeverUseTheUnsandboxedProfile(t *testing.T) {
	scanned := 0
	for _, root := range []string{"../../internal", "../../cmd"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.IsDir() || filepath.Ext(path) != ".go" {
				return err
			}
			body, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			scanned++
			for _, problem := range violations(t, path, string(body)) {
				t.Errorf("%s %s", path, problem)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("no files scanned; the guard walks the wrong directories")
	}
}

// The guard has to catch each way of reaching Container, and let Local through.
func TestGuardCatchesEachSpelling(t *testing.T) {
	const header = "package p\nimport %s \"" + codexImport + "\"\n"
	for name, test := range map[string]struct {
		body string
		want int
	}{
		"selector":          {`var _ = codex.Container`, 1},
		"alias":             {`var _ = other.Container`, 1},
		"conversion":        {`var _ = codex.Profile(1)`, 1},
		"variable argument": {`var p = 1; var _ = codex.Profile(p)`, 1},
		"literal profile":   {`var _ = &codex.Backend{Profile: profile}`, 1},
		"mode string":       {"var _ = \"danger-full-access\"", 1},
		"local":             {`var _ = &codex.Backend{Model: "m", Profile: codex.Local}; var _ = codex.Profile(0); var _ = &codex.Backend{}`, 0},
	} {
		t.Run(name, func(t *testing.T) {
			spec := "codex"
			if name == "alias" {
				spec = "other"
			}
			source := strings.Replace(header, "%s", spec, 1) + test.body
			if got := violations(t, name+".go", source); len(got) != test.want {
				t.Fatalf("violations = %v, want %d", got, test.want)
			}
		})
	}
	// An unrelated package's Container is not this package's.
	if got := violations(t, "unrelated.go", "package p\nimport \"example.com/box\"\nvar _ = box.Container"); len(got) != 0 {
		t.Fatalf("violations = %v", got)
	}
}
