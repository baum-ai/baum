package sessioncorrelate

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

func at(offset int) *time.Time {
	moment := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC).Add(time.Duration(offset) * time.Minute)
	return &moment
}

func str(value string) *string { return &value }

// treeBuilder assembles the smallest graph that carries a given piece of
// evidence, so each test names only what it is about.
type treeBuilder struct{ graph session.Graph }

func newTree(workingDir, branch string) *treeBuilder {
	root := session.Session{ID: "s-root", Outcome: session.OutcomeUnknown, StartedAt: at(0)}
	if workingDir != "" {
		root.WorkingDir = str(workingDir)
	}
	if branch != "" {
		root.GitBranch = str(branch)
	}
	return &treeBuilder{graph: session.Graph{Sessions: []session.Session{root}}}
}

func (b *treeBuilder) nested(id, workingDir string) *treeBuilder {
	nested := session.Session{
		ID: id, ParentSessionID: str("s-root"), SpawnedByToolUseID: str("t-spawn"),
		Outcome: session.OutcomeUnknown,
	}
	if workingDir != "" {
		nested.WorkingDir = str(workingDir)
	}
	b.graph.Sessions = append(b.graph.Sessions, nested)
	return b
}

func (b *treeBuilder) roots(repoRoot, worktreeRoot string) *treeBuilder {
	if repoRoot != "" {
		b.graph.Sessions[0].RepoRoot = str(repoRoot)
	}
	if worktreeRoot != "" {
		b.graph.Sessions[0].WorktreeRoot = str(worktreeRoot)
	}
	return b
}

func (b *treeBuilder) attribute(sessionID, key, value string) *treeBuilder {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	b.graph.Attributes = append(b.graph.Attributes, session.SessionAttribute{
		ID: key + "@" + sessionID, SessionID: sessionID, Key: key,
		Value: raw, ValidFrom: *at(1),
	})
	return b
}

func (b *treeBuilder) rawAttribute(sessionID, key string, value json.RawMessage) *treeBuilder {
	b.graph.Attributes = append(b.graph.Attributes, session.SessionAttribute{
		ID: key + "@" + sessionID, SessionID: sessionID, Key: key,
		Value: value, ValidFrom: *at(1),
	})
	return b
}

func (b *treeBuilder) touch(sessionID, path string, kind session.TouchKind) *treeBuilder {
	b.graph.FileTouches = append(b.graph.FileTouches, session.FileTouch{
		ID: path + "@" + sessionID, SessionID: sessionID, UserInputID: "u-1",
		Path: path, Kind: kind,
	})
	return b
}

// exec adds one execute-verb tool call and its result.
func (b *treeBuilder) exec(id string, input json.RawMessage, status session.ToolStatus, output string) *treeBuilder {
	b.graph.ToolUses = append(b.graph.ToolUses, session.ToolUse{
		ID: id, SessionID: "s-root", UserInputID: "u-1", ModelInvocationID: "m-1",
		Name: "Bash", Verb: session.VerbExecute, Input: input, Status: status, Timestamp: at(2),
	})
	b.graph.ToolResults = append(b.graph.ToolResults, session.ToolResult{
		ID: "r-" + id, ToolUseID: id, SessionID: "s-root", UserInputID: "u-1",
		Status: status, OutputText: str(output), Timestamp: at(3),
	})
	return b
}

func (b *treeBuilder) build() *session.Graph { return &b.graph }

func paths(files []FileEvidence) []string {
	out := make([]string, 0, len(files))
	for _, file := range files {
		out = append(out, file.Path)
	}
	return out
}

// The relativization cases the old internal/correlation/paths.go covered, now
// against a graph rather than against a mutated run.
func TestCorrelateRelativizesFileTouches(t *testing.T) {
	tree := newTree("/home/dev/repo", "main").
		touch("s-root", "/home/dev/repo/platform/main.go", session.TouchEdit).
		touch("s-root", "docs/README.md", session.TouchRead)

	result := Correlate(tree.build(), "claude_code")

	if got, want := paths(result.Files), []string{"platform/main.go", "docs/README.md"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", result.Warnings)
	}
}

func TestCorrelateClassifiesAdditionalRepositoriesThroughVerifiedRoots(t *testing.T) {
	tree := newTree("/workspace/main", "main").
		nested("s-sibling", "/workspace/sibling/sub").
		attribute("s-sibling", "gitBranch", "feature/sibling").
		touch("s-root", "/workspace/main/main.go", session.TouchEdit).
		touch("s-sibling", "/workspace/sibling/sub/file.go", session.TouchEdit).
		touch("s-sibling", "relative.go", session.TouchRead).
		touch("s-root", "/workspace/main/vendor/nested/lib.go", session.TouchRead).
		touch("s-root", "/lookalike/sibling/sub/file.go", session.TouchRead).
		exec("t-sibling", json.RawMessage(`{"command":"git commit -m sibling"}`), session.StatusOK, "[feature abcdef1] sibling")
	tree.graph.ToolUses[len(tree.graph.ToolUses)-1].SessionID = "s-sibling"
	tree.graph.ToolResults[len(tree.graph.ToolResults)-1].SessionID = "s-sibling"

	result := Correlate(tree.build(), "claude_code", WithRepositoryRoots("primary",
		RepositoryRoots{RepositoryID: "primary", Roots: []string{"/workspace/main"}},
		RepositoryRoots{RepositoryID: "sibling", Roots: []string{"/workspace/sibling"}},
		RepositoryRoots{RepositoryID: "nested", Roots: []string{"/workspace/main/vendor/nested"}},
	))

	if got, want := paths(result.Files), []string{"main.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("primary files = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(result.WorkingDirs, []string{"/workspace/main"}) {
		t.Fatalf("primary working dirs = %v", result.WorkingDirs)
	}
	if len(result.AdditionalRepositories) != 2 {
		t.Fatalf("additional repositories = %+v", result.AdditionalRepositories)
	}
	byRepository := map[string]RepositoryResult{}
	for _, repository := range result.AdditionalRepositories {
		byRepository[repository.RepositoryID] = repository
	}
	sibling := byRepository["sibling"]
	if got, want := paths(sibling.Files), []string{"sub/file.go", "relative.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("sibling files = %v, want %v", got, want)
	}
	if !reflect.DeepEqual(sibling.Branches, []string{"feature/sibling"}) || !reflect.DeepEqual(sibling.WorkingDirs, []string{"sub"}) {
		t.Fatalf("sibling context = %+v", sibling)
	}
	if len(sibling.CommitRefs) != 1 || sibling.CommitRefs[0].SHA != "abcdef1" {
		t.Fatalf("sibling refs = %+v", sibling.CommitRefs)
	}
	nested := byRepository["nested"]
	if got, want := paths(nested.Files), []string{"lib.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested files = %v, want %v", got, want)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %v, want only the lookalike path warning", result.Warnings)
	}
}

func TestCorrelateRejectsConflictingAdditionalRoots(t *testing.T) {
	tree := newTree("/primary", "").touch("s-root", "/shared/repo/file.go", session.TouchEdit)
	result := Correlate(tree.build(), "claude_code", WithRepositoryRoots("primary",
		RepositoryRoots{RepositoryID: "primary", Roots: []string{"/primary"}},
		RepositoryRoots{RepositoryID: "left", Roots: []string{"/shared/repo"}},
		RepositoryRoots{RepositoryID: "right", Roots: []string{"/shared/repo"}},
	))
	if len(result.AdditionalRepositories) != 0 {
		t.Fatalf("ambiguous roots produced evidence: %+v", result.AdditionalRepositories)
	}
}

func TestCorrelatePreservesAuthoritativeEmptyRepositoryInventory(t *testing.T) {
	tree := newTree("/primary", "").touch("s-root", "/primary/file.go", session.TouchEdit)
	result := Correlate(tree.build(), "claude_code", WithRepositoryRoots("primary",
		RepositoryRoots{RepositoryID: "primary", Roots: []string{"relative/not-verified"}},
	))

	if result.AdditionalRepositories == nil || len(result.AdditionalRepositories) != 0 {
		t.Fatalf("additional repositories = %#v, want authoritative empty", result.AdditionalRepositories)
	}
}

func TestCorrelateRejectsAmbiguousWorkingDirectoryHistory(t *testing.T) {
	tree := newTree("/primary", "").attribute("s-root", "cwd", "/sibling")
	result := Correlate(tree.build(), "claude_code", WithRepositoryRoots("primary",
		RepositoryRoots{RepositoryID: "primary", Roots: []string{"/primary"}},
		RepositoryRoots{RepositoryID: "sibling", Roots: []string{"/sibling"}},
	))

	if len(result.AdditionalRepositories) != 0 {
		t.Fatalf("ambiguous cwd history produced additional evidence: %+v", result.AdditionalRepositories)
	}
}

func TestCorrelateKeepsCopiedCheckoutEvidenceInItsRepository(t *testing.T) {
	tree := newTree("/checkouts/primary", "main").
		nested("s-copy", "/checkouts/copy").
		touch("s-root", "/checkouts/primary/src/shared.go", session.TouchEdit).
		touch("s-copy", "/checkouts/copy/src/shared.go", session.TouchEdit)

	result := Correlate(tree.build(), "claude_code", WithRepositoryRoots("primary",
		RepositoryRoots{RepositoryID: "primary", Roots: []string{"/checkouts/primary"}},
		RepositoryRoots{RepositoryID: "copy", Roots: []string{"/checkouts/copy"}},
	))

	if got := paths(result.Files); !reflect.DeepEqual(got, []string{"src/shared.go"}) {
		t.Fatalf("primary copied-checkout evidence = %v", got)
	}
	if len(result.AdditionalRepositories) != 1 ||
		!reflect.DeepEqual(paths(result.AdditionalRepositories[0].Files), []string{"src/shared.go"}) {
		t.Fatalf("additional copied-checkout evidence = %+v", result.AdditionalRepositories)
	}
}

func TestCorrelateRelativizesForeignPathStyles(t *testing.T) {
	for _, test := range []struct {
		name, workingDir, touch, want, wantDir string
	}{
		{name: "any Windows drive", workingDir: `d:\Work\Repo`, touch: `D:\work\repo\pkg\edit.go`, want: "pkg/edit.go", wantDir: "D:/Work/Repo"},
		{name: "UNC", workingDir: `\\Server\Share\Repo`, touch: `\\server\share\repo\pkg\edit.go`, want: "pkg/edit.go", wantDir: "//Server/Share/Repo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := Correlate(newTree(test.workingDir, "").touch("s-root", test.touch, session.TouchEdit).build(), "cursor")
			if got := paths(result.Files); !reflect.DeepEqual(got, []string{test.want}) {
				t.Fatalf("files = %v, want [%s]", got, test.want)
			}
			if len(result.Warnings) != 0 {
				t.Fatalf("warnings = %v, want none", result.Warnings)
			}
			if !reflect.DeepEqual(result.WorkingDirs, []string{test.wantDir}) {
				t.Fatalf("working dirs = %v, want [%s]", result.WorkingDirs, test.wantDir)
			}
		})
	}
}

func TestCorrelateUsesRecordedRepositoryRoots(t *testing.T) {
	tree := newTree("", "").
		roots(`d:\work\repo`, `d:\work\repo\.worktrees\branch`).
		touch("s-root", `D:\work\repo\README.md`, session.TouchRead).
		touch("s-root", `D:\work\repo\.worktrees\branch\platform\main.go`, session.TouchEdit)

	result := Correlate(tree.build(), "cursor")

	want := []FileEvidence{
		{Path: "README.md", Kind: session.TouchRead},
		{Path: "platform/main.go", Kind: session.TouchEdit},
	}
	if !reflect.DeepEqual(result.Files, want) {
		t.Fatalf("files = %+v, want %+v", result.Files, want)
	}
	if len(result.WorkingDirs) != 0 {
		t.Fatalf("working dirs = %v, want roots kept out", result.WorkingDirs)
	}
}

func TestCorrelateRejectsIncompatibleAndDriveRelativePaths(t *testing.T) {
	tree := newTree(`D:\repo`, "").
		touch("s-root", `E:\repo\other.go`, session.TouchRead).
		touch("s-root", `D:repo\relative.go`, session.TouchEdit)

	result := Correlate(tree.build(), "cursor")

	if len(result.Files) != 0 {
		t.Fatalf("files = %+v, want none", result.Files)
	}
	if len(result.Warnings) != 2 {
		t.Fatalf("warnings = %v, want one for each unsafe path", result.Warnings)
	}
}

// Fail closed: an absolute path under no known root is a warning, not an
// invented repo-relative file.
func TestCorrelateWarnsOnPathOutsideEveryKnownRoot(t *testing.T) {
	tree := newTree("/home/dev/repo", "").
		touch("s-root", "/etc/hosts", session.TouchRead).
		touch("s-root", "/etc/hosts", session.TouchEdit)

	result := Correlate(tree.build(), "claude_code")

	if len(result.Files) != 0 {
		t.Fatalf("files = %v, want none", result.Files)
	}
	if len(result.Warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one per path", result.Warnings)
	}
}

// A session's own cwd wins over a deeper one a sibling reported, which is why
// the history is kept per session and not only pooled.
func TestCorrelatePrefersTheOwningSessionsWorkingDir(t *testing.T) {
	tree := newTree("/repo", "").
		nested("s-web", "/repo/web").
		touch("s-root", "/repo/web/app.tsx", session.TouchEdit).
		touch("s-web", "/repo/web/app.tsx", session.TouchRead)

	result := Correlate(tree.build(), "claude_code")

	want := []FileEvidence{
		{Path: "web/app.tsx", Kind: session.TouchEdit},
		{Path: "app.tsx", Kind: session.TouchRead},
	}
	if !reflect.DeepEqual(result.Files, want) {
		t.Fatalf("files = %+v, want %+v", result.Files, want)
	}
}

// A nested session that recorded no cwd of its own still ran inside the tree
// that spawned it, so the pooled history relativizes its paths.
func TestCorrelateFallsBackToTheTreesWorkingDirs(t *testing.T) {
	tree := newTree("/repo", "").
		nested("s-sub", "").
		touch("s-sub", "/repo/platform/main.go", session.TouchRead)

	result := Correlate(tree.build(), "claude_code")

	if got, want := paths(result.Files), []string{"platform/main.go"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("files = %v, want %v", got, want)
	}
}

// The anchor rule from internal/correlation/entire: with no cwd anywhere, an
// absolute path whose tail is a spelling the tree itself produced resolves to
// that spelling. Longest wins.
func TestCorrelateAnchorsAbsolutePathsAgainstRelativeOnes(t *testing.T) {
	tree := newTree("", "").
		touch("s-root", "web/app.tsx", session.TouchEdit).
		touch("s-root", "app.tsx", session.TouchEdit).
		touch("s-root", "/checkout/web/app.tsx", session.TouchRead)

	result := Correlate(tree.build(), "cursor")

	want := []FileEvidence{
		{Path: "web/app.tsx", Kind: session.TouchEdit},
		{Path: "app.tsx", Kind: session.TouchEdit},
		{Path: "web/app.tsx", Kind: session.TouchRead},
	}
	if !reflect.DeepEqual(result.Files, want) {
		t.Fatalf("files = %+v, want %+v", result.Files, want)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", result.Warnings)
	}
}

// Entire's session metadata lists the files that changed while the session ran.
// When the caller decides the list may stand as evidence, its repository-relative
// entries become edit rows directly, and they anchor the absolute tool paths of
// a session that recorded no working directory.
func TestCorrelateTakesExternallyReportedEditedFilesAsEvidence(t *testing.T) {
	tree := newTree("", "").
		touch("s-root", "/checkout/platform/parse.go", session.TouchRead)

	result := Correlate(tree.build(), "cursor", WithEditedFiles("platform/parse.go", "README.md", ""))

	want := []FileEvidence{
		{Path: "platform/parse.go", Kind: session.TouchEdit},
		{Path: "README.md", Kind: session.TouchEdit},
		{Path: "platform/parse.go", Kind: session.TouchRead},
	}
	if !reflect.DeepEqual(result.Files, want) {
		t.Fatalf("files = %+v, want %+v", result.Files, want)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", result.Warnings)
	}
}

// The same list as anchors only: it relativizes the tree's own touches and is
// otherwise absent from the evidence.
func TestCorrelateUsesFileAnchorsWithoutStoringThem(t *testing.T) {
	tree := newTree("", "").
		touch("s-root", "/checkout/platform/parse.go", session.TouchRead)

	result := Correlate(tree.build(), "cursor", WithFileAnchors("platform/parse.go", "README.md"))

	want := []FileEvidence{{Path: "platform/parse.go", Kind: session.TouchRead}}
	if !reflect.DeepEqual(result.Files, want) {
		t.Fatalf("files = %+v, want %+v", result.Files, want)
	}
	if len(result.Warnings) != 0 {
		t.Errorf("warnings = %v, want none", result.Warnings)
	}
}

// The list is declared repository-relative. An absolute entry is a
// contradiction, not a path to relativize, so it contributes nothing.
func TestCorrelateIgnoresAbsoluteEditedFiles(t *testing.T) {
	result := Correlate(newTree("/repo", "").build(), "claude_code", WithEditedFiles("/repo/main.go"))
	if len(result.Files) != 0 {
		t.Fatalf("files = %+v, want none", result.Files)
	}
}

// agent_session_files is keyed on (root_session_id, path, kind), so the same
// file read twice is one row and the same file read and edited is two.
func TestCorrelateDeduplicatesFilesOnPathAndKind(t *testing.T) {
	tree := newTree("/repo", "").
		touch("s-root", "/repo/main.go", session.TouchRead).
		touch("s-root", "/repo/main.go", session.TouchRead).
		touch("s-root", "/repo/main.go", session.TouchEdit)

	result := Correlate(tree.build(), "claude_code")

	want := []FileEvidence{
		{Path: "main.go", Kind: session.TouchRead},
		{Path: "main.go", Kind: session.TouchEdit},
	}
	if !reflect.DeepEqual(result.Files, want) {
		t.Fatalf("files = %+v, want %+v", result.Files, want)
	}
}

func TestCorrelateCollectsCwdAndBranchHistory(t *testing.T) {
	tree := newTree("/repo", "main").
		attribute("s-root", "cwd", "/repo").
		attribute("s-root", "cwd", "/repo/platform").
		attribute("s-root", "gitBranch", "feature/x").
		rawAttribute("s-root", "gitBranch", json.RawMessage("null"))

	result := Correlate(tree.build(), "claude_code")

	if got, want := result.WorkingDirs, []string{"/repo", "/repo/platform"}; !reflect.DeepEqual(got, want) {
		t.Errorf("working dirs = %v, want %v", got, want)
	}
	if got, want := result.Branches, []string{"main", "feature/x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("branches = %v, want %v", got, want)
	}
}

// Each vendor spells the same fact its own way; reading the wrong key loses the
// history without failing anything, which is why the table is tested directly.
func TestCorrelateReadsEachVendorsBranchSpelling(t *testing.T) {
	codex := newTree("", "").attribute("s-root", "git.branch", "main")
	if got := Correlate(codex.build(), "codex").Branches; !reflect.DeepEqual(got, []string{"main"}) {
		t.Errorf("codex branches = %v, want [main]", got)
	}

	// An unrecognised slug still gets every spelling: losing a tree's history
	// is silent, and a key no mapper writes matches nothing.
	unknown := newTree("", "").attribute("s-root", "gitBranch", "main")
	if got := Correlate(unknown.build(), "copilot").Branches; !reflect.DeepEqual(got, []string{"main"}) {
		t.Errorf("unknown-vendor branches = %v, want [main]", got)
	}
}

func TestCorrelateExtractsCreatedCommitRefs(t *testing.T) {
	tree := newTree("/repo", "").
		exec("t-1", json.RawMessage(`{"command":"git add -A && git commit -m 'x'"}`),
			session.StatusOK, "[main 3fa9b21] x\n 1 file changed").
		// A second commit echoing the same hash must not produce a second row.
		exec("t-2", json.RawMessage(`{"command":"git commit --amend --no-edit"}`),
			session.StatusOK, "[main 3fa9b21] x").
		exec("t-3", json.RawMessage(`{"cmd":"git commit -m 'y'"}`),
			session.StatusOK, "[main deadbeefcafe] y").
		// Quiet commits print no summary, so agents commonly expose the new HEAD
		// with git log in the same shell call.
		exec("t-7", json.RawMessage(`{"command":"git commit -q -F - && git log --oneline -1"}`),
			session.StatusOK, "a1b2c3d quiet commit").
		// Not a commit, failed, or printed no hash: no evidence from any.
		exec("t-4", json.RawMessage(`{"command":"git log --oneline"}`), session.StatusOK, "[main 1111111] old").
		exec("t-5", json.RawMessage(`{"command":"git commit -m 'z'"}`), session.StatusError, "[main 2222222] z").
		exec("t-6", json.RawMessage(`{"command":"git commit --dry-run"}`), session.StatusOK, "[main 3333333] dry")

	refs := Correlate(tree.build(), "claude_code").CommitRefs

	if len(refs) != 3 {
		t.Fatalf("commit refs = %+v, want 3", refs)
	}
	if refs[0].SHA != "3fa9b21" || refs[0].Role != RoleCreated {
		t.Errorf("first ref = %+v", refs[0])
	}
	if refs[0].ToolUseID == nil || *refs[0].ToolUseID != "t-1" {
		t.Errorf("first ref tool use = %v, want t-1 (the first observation)", refs[0].ToolUseID)
	}
	if refs[0].ObservedAt == nil || !refs[0].ObservedAt.Equal(*at(3)) {
		t.Errorf("first ref observed at %v, want the result timestamp", refs[0].ObservedAt)
	}
	if refs[1].SHA != "deadbeefcafe" {
		t.Errorf("second ref = %+v", refs[1])
	}
	if refs[2].SHA != "a1b2c3d" {
		t.Errorf("third ref = %+v", refs[2])
	}
}

// Codex keeps a shell call's terminal output in the structured payload, so a
// commit hash can appear only there.
func TestCorrelateReadsCommitHashesOutOfStructuredOutput(t *testing.T) {
	tree := newTree("/repo", "")
	tree.graph.ToolUses = append(tree.graph.ToolUses, session.ToolUse{
		ID: "t-1", SessionID: "s-root", UserInputID: "u-1", ModelInvocationID: "m-1",
		Name: "shell", Verb: session.VerbExecute, Status: session.StatusOK,
		Input: json.RawMessage(`{"command":["bash","-lc","git commit -m x"]}`), Timestamp: at(2),
	})
	tree.graph.ToolResults = append(tree.graph.ToolResults, session.ToolResult{
		ID: "r-1", ToolUseID: "t-1", SessionID: "s-root", UserInputID: "u-1",
		Status: session.StatusOK,
		Output: json.RawMessage(`{"completion_events":[{"aggregated_output":"[main 9c1d2e3] x\n"}]}`),
	})

	refs := Correlate(tree.build(), "codex").CommitRefs

	if len(refs) != 1 || refs[0].SHA != "9c1d2e3" || refs[0].Role != RoleCreated {
		t.Fatalf("commit refs = %+v, want one created 9c1d2e3", refs)
	}
}

func TestCorrelateTagsTheStartingCommit(t *testing.T) {
	tree := newTree("/repo", "").
		nested("s-child", "/repo").
		attribute("s-root", "git.commit_hash", "abc1234").
		// A nested thread's own starting commit describes that thread only.
		attribute("s-child", "git.commit_hash", "999ffff")

	refs := Correlate(tree.build(), "codex").CommitRefs

	if len(refs) != 1 {
		t.Fatalf("commit refs = %+v, want exactly the root's", refs)
	}
	if refs[0].SHA != "abc1234" || refs[0].Role != RoleStarting {
		t.Errorf("ref = %+v", refs[0])
	}
	if refs[0].ToolUseID != nil {
		t.Errorf("ref tool use = %v, want nil (metadata, not a tool call)", *refs[0].ToolUseID)
	}
	if refs[0].ObservedAt == nil || !refs[0].ObservedAt.Equal(*at(0)) {
		t.Errorf("observed at %v, want the session start", refs[0].ObservedAt)
	}
}

// A starting commit is whatever the vendor's metadata said, and only git's own
// output is guaranteed lowercase. The requeue's SHA arm prefix-matches a stored
// commit SHA against the ref with an index and folds neither side, so an
// uppercase ref would silently match nothing.
func TestCorrelateLowercasesCommitRefs(t *testing.T) {
	tree := newTree("/repo", "").
		attribute("s-root", "git.commit_hash", "ABC1234")

	refs := Correlate(tree.build(), "codex").CommitRefs

	if len(refs) != 1 || refs[0].SHA != "abc1234" {
		t.Fatalf("commit refs = %+v, want the SHA lowercased", refs)
	}
}

// role is part of the key, so one SHA can be both the tree's starting commit
// and a commit it created.
func TestCorrelateKeepsOneSHAInTwoRoles(t *testing.T) {
	tree := newTree("/repo", "").
		attribute("s-root", "git.commit_hash", "3fa9b21").
		exec("t-1", json.RawMessage(`{"command":"git commit -m x"}`), session.StatusOK, "[main 3fa9b21] x")

	refs := Correlate(tree.build(), "codex").CommitRefs

	if len(refs) != 2 {
		t.Fatalf("commit refs = %+v, want 2", refs)
	}
}

// The reason this package exists rather than internal/correlation: the graph is
// what gets stored, and it must still say what the vendor reported.
func TestCorrelateNeverMutatesTheGraph(t *testing.T) {
	tree := newTree("/repo", "main").
		nested("s-sub", "").
		attribute("s-root", "cwd", "/repo/platform").
		attribute("s-root", "git.commit_hash", "abc1234").
		touch("s-root", "/repo/platform/main.go", session.TouchEdit).
		touch("s-sub", "/elsewhere/other.go", session.TouchRead).
		exec("t-1", json.RawMessage(`{"command":"git commit -m x"}`), session.StatusOK, "[main 3fa9b21] x")
	graph := tree.build()

	before, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}
	result := Correlate(graph, "codex")
	after, err := json.Marshal(graph)
	if err != nil {
		t.Fatal(err)
	}

	if string(before) != string(after) {
		t.Fatal("Correlate modified the graph")
	}
	if len(result.Files) == 0 || len(result.CommitRefs) == 0 || len(result.Warnings) == 0 {
		t.Fatalf("fixture produced no evidence to speak of: %+v", result)
	}
}

// A value captured from an agent reaches a TEXT column; one NUL would abort the
// ingest transaction for every session behind it.
func TestCorrelateScrubsEvidenceForPostgres(t *testing.T) {
	tree := newTree("/repo", "ma\x00in").touch("s-root", "/repo/ma\x00in.go", session.TouchEdit)

	result := Correlate(tree.build(), "claude_code")

	if len(result.Files) != 1 || result.Files[0].Path != "ma�in.go" {
		t.Fatalf("files = %+v", result.Files)
	}
	if len(result.Branches) != 1 || result.Branches[0] != "ma�in" {
		t.Fatalf("branches = %+v", result.Branches)
	}
}

func TestCorrelateHandlesAnEmptyGraph(t *testing.T) {
	if got := Correlate(nil, "claude_code"); !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("Correlate(nil) = %+v", got)
	}
	if got := Correlate(&session.Graph{}, "claude_code"); !reflect.DeepEqual(got, Result{}) {
		t.Fatalf("Correlate(empty) = %+v", got)
	}
}
