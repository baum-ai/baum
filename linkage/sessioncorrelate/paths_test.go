package sessioncorrelate

import (
	"reflect"
	"testing"

	"github.com/lastpersonlabs/baum/sessions/session"
)

// A cwd inside the repository must not cut the path's leading directories
// when the repository root was recorded.
func TestCorrelatePrefersRecordedRootsOverSubdirectoryCwd(t *testing.T) {
	tree := newTree("/home/dev/repo", "").
		roots("/home/dev/repo", "").
		attribute("s-root", "cwd", "/home/dev/repo/ui").
		touch("s-root", "/home/dev/repo/ui/src/api.ts", session.TouchEdit)

	if got := paths(Correlate(tree.build(), "claude_code").Files); !reflect.DeepEqual(got, []string{"ui/src/api.ts"}) {
		t.Errorf("files = %v, want [ui/src/api.ts]", got)
	}
}

func TestCorrelateStripsAgentWorktreePrefix(t *testing.T) {
	tree := newTree("/home/dev/repo", "").
		touch("s-root", "/home/dev/repo/.claude/worktrees/first/platform/a.go", session.TouchEdit).
		touch("s-root", ".agents/worktrees/second/ui/b.ts", session.TouchEdit)

	if got := paths(Correlate(tree.build(), "claude_code").Files); !reflect.DeepEqual(got, []string{"platform/a.go", "ui/b.ts"}) {
		t.Errorf("files = %v, want [platform/a.go ui/b.ts]", got)
	}
}

// Without recorded roots, an agent that cds into a subdirectory of its agent
// worktree still edits paths relative to the worktree, a checkout of the
// repository. A subdirectory cwd outside any agent worktree still cuts.
func TestCorrelateCutsCwdInsideAgentWorktreeAtTheWorktree(t *testing.T) {
	tree := newTree("/home/dev/repo", "").
		attribute("s-root", "cwd", "/home/dev/repo/.agents/worktrees/w").
		attribute("s-root", "cwd", "/home/dev/repo/.agents/worktrees/w/platform").
		attribute("s-root", "cwd", "/home/dev/repo/.agents/worktrees/w/platform/pkg/config").
		attribute("s-root", "cwd", "/home/dev/repo/ui").
		touch("s-root", "/home/dev/repo/.agents/worktrees/w/.env.defaults", session.TouchEdit).
		touch("s-root", "/home/dev/repo/.agents/worktrees/w/platform/config.toml.example", session.TouchEdit).
		touch("s-root", "/home/dev/repo/.agents/worktrees/w/platform/pkg/config/loader.go", session.TouchEdit).
		touch("s-root", "/home/dev/repo/ui/src/x.ts", session.TouchEdit)

	want := []string{".env.defaults", "platform/config.toml.example", "platform/pkg/config/loader.go", "src/x.ts"}
	if got := paths(Correlate(tree.build(), "claude_code").Files); !reflect.DeepEqual(got, want) {
		t.Errorf("files = %v, want %v", got, want)
	}
}

// Only an in-repo .claude or .agents worktree the tree worked in re-cuts a
// path. Cursor, Codex and .worktrees layouts, where the directory after the
// marker need not be the checkout, keep the plain longest-cwd cut, whatever
// cwds the tree has.
func TestCorrelateCutsOnlyAtAnInRepoAgentWorktreeTheTreeWorkedIn(t *testing.T) {
	cases := map[string]struct {
		cwds []string
		edit string
		want string
	}{
		"cursor home worktree, cwd at checkout": {
			cwds: []string{"/home/dev", "/home/dev/.cursor/worktrees/baum/0jvq"},
			edit: "/home/dev/.cursor/worktrees/baum/0jvq/platform/x.go", want: "platform/x.go",
		},
		"cursor home worktree, cwd in subdirectory": {
			cwds: []string{"/home/dev", "/home/dev/.cursor/worktrees/baum/0jvq", "/home/dev/.cursor/worktrees/baum/0jvq/platform"},
			edit: "/home/dev/.cursor/worktrees/baum/0jvq/platform/x.go", want: "x.go",
		},
		"codex home worktree, cwd at checkout": {
			cwds: []string{"/home/user", "/home/user/.codex/worktrees/ab12/baum"},
			edit: "/home/user/.codex/worktrees/ab12/baum/platform/x.go", want: "platform/x.go",
		},
		"codex home worktree, cwd in subdirectory": {
			cwds: []string{"/home/user", "/home/user/.codex/worktrees/ab12/baum/platform"},
			edit: "/home/user/.codex/worktrees/ab12/baum/platform/x.go", want: "x.go",
		},
		"home .worktrees, cwd at checkout": {
			cwds: []string{"/home/dev", "/home/dev/.worktrees/feat/baum"},
			edit: "/home/dev/.worktrees/feat/baum/platform/x.go", want: "platform/x.go",
		},
		"home .worktrees, cwd in subdirectory": {
			cwds: []string{"/home/dev", "/home/dev/.worktrees/feat/baum/platform"},
			edit: "/home/dev/.worktrees/feat/baum/platform/x.go", want: "x.go",
		},
		"cursor worktree under a Windows home": {
			cwds: []string{`C:\Users\p`, `C:\Users\p\.cursor\worktrees\baum\0jvq`},
			edit: `C:\Users\p\.cursor\worktrees\baum\0jvq\platform\x.go`, want: "platform/x.go",
		},
		"codex worktree under a /var/home home": {
			cwds: []string{"/var/home/dev", "/var/home/dev/.codex/worktrees/ab12/baum"},
			edit: "/var/home/dev/.codex/worktrees/ab12/baum/platform/x.go", want: "platform/x.go",
		},
		"codex worktree id directory as a cwd": {
			cwds: []string{"/home/dev/.codex/worktrees/ab", "/home/dev/.codex/worktrees/ab/baum/platform"},
			edit: "/home/dev/.codex/worktrees/ab/baum/platform/x.go", want: "x.go",
		},
		"cursor worktree repo directory as a cwd": {
			cwds: []string{"/home/dev/.cursor/worktrees/baum", "/home/dev/.cursor/worktrees/baum/0j/platform"},
			edit: "/home/dev/.cursor/worktrees/baum/0j/platform/x.go", want: "x.go",
		},
		"home .worktrees branch directory as a cwd": {
			cwds: []string{"/home/dev/.worktrees/feat", "/home/dev/.worktrees/feat/baum/platform"},
			edit: "/home/dev/.worktrees/feat/baum/platform/x.go", want: "x.go",
		},
		"in-repo cursor worktree keeps the cwd cut": {
			cwds: []string{"/home/dev/repo/.cursor/worktrees/w", "/home/dev/repo/.cursor/worktrees/w/platform"},
			edit: "/home/dev/repo/.cursor/worktrees/w/platform/x.go", want: "x.go",
		},
		"in-repo worktree nested in the checkout's own": {
			cwds: []string{"/r", "/r/.claude/worktrees/w/.claude/worktrees/v", "/r/.claude/worktrees/w/.claude/worktrees/v/platform"},
			edit: "/r/.claude/worktrees/w/.claude/worktrees/v/platform/x.go", want: "platform/x.go",
		},
		"nested in-repo worktrees both worked in": {
			cwds: []string{"/r/.claude/worktrees/w", "/r/.claude/worktrees/w/.claude/worktrees/v", "/r/.claude/worktrees/w/.claude/worktrees/v/platform"},
			edit: "/r/.claude/worktrees/w/.claude/worktrees/v/platform/x.go", want: "platform/x.go",
		},
		"in-repo worktree on Windows": {
			cwds: []string{`C:\r\.agents\worktrees\w`, `C:\r\.agents\worktrees\w\platform`},
			edit: `C:\r\.agents\worktrees\w\platform\x.go`, want: "platform/x.go",
		},
		"in-repo worktree the tree never worked in": {
			cwds: []string{"/home/dev/repo", "/home/dev/repo/.claude/worktrees/w/platform"},
			edit: "/home/dev/repo/.claude/worktrees/w/platform/x.go", want: "x.go",
		},
		"in-repo worktree the tree worked in": {
			cwds: []string{"/home/dev/repo/.claude/worktrees/w", "/home/dev/repo/.claude/worktrees/w/platform"},
			edit: "/home/dev/repo/.claude/worktrees/w/platform/x.go", want: "platform/x.go",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tree := newTree(tc.cwds[0], "")
			for _, cwd := range tc.cwds[1:] {
				tree.attribute("s-root", "cwd", cwd)
			}
			graph := tree.touch("s-root", tc.edit, session.TouchEdit).build()
			if got := paths(Correlate(graph, "claude_code").Files); !reflect.DeepEqual(got, []string{tc.want}) {
				t.Errorf("files = %v, want [%s]", got, tc.want)
			}
			if got, ok := NewPathResolver(graph, "claude_code").Touch(graph.FileTouches[0]); !ok || got != tc.want {
				t.Errorf("resolver = %q, %v, want %q", got, ok, tc.want)
			}
		})
	}
}

func TestCorrelateStripsAgentWorktreePrefixInAdditionalRepositories(t *testing.T) {
	tree := newTree("/w", "").
		touch("s-root", "/w/main/a.go", session.TouchEdit).
		touch("s-root", "/w/other/.claude/worktrees/x/b.go", session.TouchEdit)

	result := Correlate(tree.build(), "claude_code", WithRepositoryRoots("main",
		RepositoryRoots{RepositoryID: "main", Roots: []string{"/w/main"}},
		RepositoryRoots{RepositoryID: "other", Roots: []string{"/w/other"}}))

	if len(result.AdditionalRepositories) != 1 || !reflect.DeepEqual(paths(result.AdditionalRepositories[0].Files), []string{"b.go"}) {
		t.Errorf("additional repositories = %+v, want other with [b.go]", result.AdditionalRepositories)
	}
}

// Every touch PathResolver resolves must be a path Correlate reports for the
// same tree and options, and the other way round.
func TestPathResolverAgreesWithCorrelate(t *testing.T) {
	cases := map[string]struct {
		graph *session.Graph
		opts  []Option
	}{
		"cwd and nested session": {graph: newTree("/home/dev/repo", "main").
			nested("s-child", "").
			touch("s-root", "/home/dev/repo/platform/main.go", session.TouchEdit).
			touch("s-child", "/home/dev/repo/ui/app.ts", session.TouchEdit).
			touch("s-root", "docs/README.md", session.TouchEdit).
			touch("s-root", "/home/dev/.claude/plans/plan.md", session.TouchEdit).
			build()},
		"anchors only": {
			graph: newTree("", "").
				touch("s-root", "/somewhere/repo/ui/app.ts", session.TouchEdit).
				touch("s-root", "/somewhere/repo/ui/other.ts", session.TouchEdit).
				build(),
			opts: []Option{WithFileAnchors("ui/app.ts")},
		},
		"roots and agent worktree": {graph: newTree("/home/dev/repo", "").
			roots("/home/dev/repo", "").
			attribute("s-root", "cwd", "/home/dev/repo/ui").
			touch("s-root", "/home/dev/repo/ui/src/api.ts", session.TouchEdit).
			touch("s-root", "/home/dev/repo/.claude/worktrees/w/platform/a.go", session.TouchEdit).
			touch("s-root", "/home/dev/notes.md", session.TouchEdit).
			build()},
		"cwd inside agent worktree": {graph: newTree("/home/dev/repo", "").
			attribute("s-root", "cwd", "/home/dev/repo/.claude/worktrees/w").
			attribute("s-root", "cwd", "/home/dev/repo/.claude/worktrees/w/platform").
			touch("s-root", "/home/dev/repo/.claude/worktrees/w/platform/a.go", session.TouchEdit).
			touch("s-root", "/home/dev/repo/.claude/worktrees/w/ui/b.ts", session.TouchEdit).
			touch("s-root", "/home/dev/notes.md", session.TouchEdit).
			build()},
		"other repository": {
			graph: newTree("/w", "").
				touch("s-root", "/w/main/a.go", session.TouchEdit).
				touch("s-root", "/w/other/b.go", session.TouchEdit).
				build(),
			opts: []Option{WithRepositoryRoots("main",
				RepositoryRoots{RepositoryID: "main", Roots: []string{"/w/main"}},
				RepositoryRoots{RepositoryID: "other", Roots: []string{"/w/other"}})},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			want := []string{}
			for _, file := range Correlate(tc.graph, "claude_code", tc.opts...).Files {
				want = append(want, file.Path)
			}
			if len(want) == 0 || len(want) == len(tc.graph.FileTouches) {
				t.Fatalf("case must have both resolved and unresolved touches; Correlate reports %v", want)
			}
			resolver := NewPathResolver(tc.graph, "claude_code", tc.opts...)
			got := []string{}
			for _, touch := range tc.graph.FileTouches {
				if path, ok := resolver.Touch(touch); ok {
					got = append(got, path)
				}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("resolved %v, Correlate reports %v", got, want)
			}
		})
	}
}

func TestPathResolverOfNilGraph(t *testing.T) {
	if _, ok := NewPathResolver(nil, "").Touch(session.FileTouch{Path: "/abs/x.go"}); ok {
		t.Error("an absolute path with no working directory must not resolve")
	}
}

// The relativization cases local session evidence depends on, with and
// without the clone's roots.
func TestPathResolverCases(t *testing.T) {
	cases := map[string]struct {
		tree       *treeBuilder
		cloneRoots []string
		want       map[string]string // touch path to result, "" unresolved
	}{
		"cwd below a recorded repository root": {
			tree: newTree("/repo/web", "").roots("/repo", ""),
			want: map[string]string{"/repo/web/src/x.ts": "web/src/x.ts"},
		},
		"in-repo agent worktree": {
			tree: newTree("/repo", ""),
			want: map[string]string{"/repo/.claude/worktrees/feat/pkg/a.go": "pkg/a.go"},
		},
		"cwd below a recorded repository root, clone known": {
			tree: newTree("/repo/web", "").roots("/repo", ""), cloneRoots: []string{"/repo"},
			want: map[string]string{"/repo/web/src/x.ts": "web/src/x.ts"},
		},
		"in-repo agent worktree, clone known": {
			tree: newTree("/repo", ""), cloneRoots: []string{"/repo"},
			want: map[string]string{"/repo/.claude/worktrees/feat/pkg/a.go": "pkg/a.go"},
		},
		"cwd above the clone": {
			tree: newTree("/home/user/src", ""), cloneRoots: []string{"/home/user/src/repo"},
			want: map[string]string{
				"/home/user/src/other-client/secrets.env": "",
				"/home/user/src/repo/main.go":             "main.go",
				"/home/user/.claude/plans/plan.md":        "",
				"docs/guide.md":                           "",
			},
		},
		"relative paths join the session's working directory": {
			tree: newTree("/repo/web", ""), cloneRoots: []string{"/repo"},
			want: map[string]string{"src/rel.ts": "web/src/rel.ts", "../outside.txt": ""},
		},
		"relative path from the home directory": {
			tree: newTree("/home/user", ""), cloneRoots: []string{"/home/user/src/repo"},
			want: map[string]string{".ssh/config": ""},
		},
		"relative path from a cwd history that disagrees": {
			tree:       newTree("/repo", "").attribute("s-root", "cwd", "/repo/web"),
			cloneRoots: []string{"/repo"},
			want:       map[string]string{"src/rel.ts": ""},
		},
		"worktree beside the clone and a previous path": {
			tree:       newTree("/home/user/src/repo-feature", ""),
			cloneRoots: []string{"/home/user/src/repo", "/home/user/src/repo-feature", "/home/user/old/repo"},
			want: map[string]string{
				"/home/user/src/repo-feature/pkg/b.go": "pkg/b.go",
				"/home/user/old/repo/c.go":             "c.go",
				"/home/user/src/repo-other/d.go":       "",
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for path := range tc.want {
				tc.tree.touch("s-root", path, session.TouchEdit)
			}
			graph := tc.tree.build()
			resolver := NewPathResolver(graph, "claude_code", WithCloneRoots(tc.cloneRoots...))
			for _, touch := range graph.FileTouches {
				got, ok := resolver.Touch(touch)
				if want := tc.want[touch.Path]; got != want || ok != (want != "") {
					t.Errorf("%s: Touch = %q, %v; want %q", touch.Path, got, ok, want)
				}
			}
		})
	}
}
