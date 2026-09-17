package sourcepath

import "testing"

func TestParseNormalizesForeignPaths(t *testing.T) {
	for _, test := range []struct {
		name, raw, want string
		style           Style
		absolute        bool
		repoRelative    bool
	}{
		{name: "POSIX", raw: "/repo/pkg/../edit.go", want: "/repo/edit.go", style: StylePOSIX, absolute: true},
		{name: "Windows drive", raw: `d:\repo\pkg\..\edit.go`, want: "D:/repo/edit.go", style: StyleWindowsDrive, absolute: true},
		{name: "file URI drive", raw: `/e:/repo/edit.go`, want: "E:/repo/edit.go", style: StyleWindowsDrive, absolute: true},
		{name: "extended drive", raw: `\\?\z:\repo\edit.go`, want: "Z:/repo/edit.go", style: StyleWindowsDrive, absolute: true},
		{name: "drive root", raw: `F:\`, want: "F:/", style: StyleWindowsDrive, absolute: true},
		{name: "drive cannot escape root", raw: `g:\..\edit.go`, want: "G:/edit.go", style: StyleWindowsDrive, absolute: true},
		{name: "UNC", raw: `\\server\share\repo\edit.go`, want: "//server/share/repo/edit.go", style: StyleWindowsUNC, absolute: true},
		{name: "extended UNC", raw: `\\?\UNC\server\share\repo\edit.go`, want: "//server/share/repo/edit.go", style: StyleWindowsUNC, absolute: true},
		{name: "UNC cannot escape share", raw: `\\server\share\..\edit.go`, want: "//server/share/edit.go", style: StyleWindowsUNC, absolute: true},
		{name: "relative Windows", raw: `pkg\sub\..\edit.go`, want: "pkg/edit.go", style: StyleRelative, repoRelative: true},
		{name: "relative POSIX", raw: "pkg/sub/../edit.go", want: "pkg/edit.go", style: StyleRelative, repoRelative: true},
		{name: "parent relative", raw: "../edit.go", want: "../edit.go", style: StyleRelative},
		{name: "drive relative", raw: `h:repo\edit.go`, want: "H:repo/edit.go", style: StyleWindowsDriveRelative},
		{name: "current directory", raw: "pkg/..", want: ".", style: StyleRelative},
		{name: "Windows rooted", raw: `\repo\edit.go`, want: `\repo/edit.go`, style: StyleWindowsRooted},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := Parse(test.raw)
			if got.String() != test.want || got.Style() != test.style || got.IsAbsolute() != test.absolute || got.IsRepositoryRelative() != test.repoRelative {
				t.Fatalf("Parse(%q) = {%q %v absolute=%v repo-relative=%v}, want {%q %v absolute=%v repo-relative=%v}",
					test.raw, got.String(), got.Style(), got.IsAbsolute(), got.IsRepositoryRelative(),
					test.want, test.style, test.absolute, test.repoRelative)
			}
		})
	}
}

func TestNormalizePreservesUnsafePathStyles(t *testing.T) {
	for _, raw := range []string{`\repo\edit.go`, `H:repo\edit.go`} {
		normalized := Normalize(raw)
		if parsed := Parse(normalized); parsed.IsAbsolute() || parsed.IsRepositoryRelative() {
			t.Fatalf("Parse(Normalize(%q)) = %+v, want an unsafe path", raw, parsed)
		}
	}
}

func TestRelUsesTheRecordedPathStyle(t *testing.T) {
	for _, test := range []struct {
		name, base, target, want string
		ok                       bool
	}{
		{name: "POSIX", base: "/repo", target: "/repo/pkg/edit.go", want: "pkg/edit.go", ok: true},
		{name: "any drive", base: `d:\Work\Repo`, target: `D:\work\repo\pkg\edit.go`, want: "pkg/edit.go", ok: true},
		{name: "different drive", base: `D:\repo`, target: `E:\repo\edit.go`},
		{name: "component boundary", base: `D:\repo`, target: `D:\repository\edit.go`},
		{name: "UNC", base: `\\Server\Share\repo`, target: `\\server\share\REPO\pkg\edit.go`, want: "pkg/edit.go", ok: true},
		{name: "different UNC share", base: `\\server\one\repo`, target: `\\server\two\repo\edit.go`},
		{name: "different style", base: "/repo", target: `D:\repo\edit.go`},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := Rel(test.base, test.target)
			if got != test.want || ok != test.ok {
				t.Fatalf("Rel(%q, %q) = %q, %v; want %q, %v", test.base, test.target, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestJoinAndSuffixUsePathStyle(t *testing.T) {
	if got := Join(`d:\repo`, `pkg\edit.go`); got != "D:/repo/pkg/edit.go" {
		t.Fatalf("Windows Join = %q", got)
	}
	if got := Join(`\\server\share\repo`, `pkg\edit.go`); got != "//server/share/repo/pkg/edit.go" {
		t.Fatalf("UNC Join = %q", got)
	}
	if !HasPathSuffix(`D:\Repo\Web\App.tsx`, "web/app.tsx") {
		t.Fatal("Windows suffix comparison was not case-insensitive")
	}
	if HasPathSuffix("/Repo/Web/App.tsx", "web/app.tsx") {
		t.Fatal("POSIX suffix comparison was not case-sensitive")
	}
}
