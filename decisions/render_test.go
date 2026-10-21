package decisions

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/goldentest"
	"github.com/baum-ai/baum/linkage/sessioncorrelate"
	"github.com/baum-ai/baum/sessions/session"
)

func newRedactor(t *testing.T) *bundle.Redactor {
	t.Helper()
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	return redactor
}

// caseGraph builds a synthetic one-session Claude Code tree under
// /home/alice/repo; alice is a made-up account name the masking rules must
// catch.
type caseGraph struct{ graph session.Graph }

func newCaseGraph() *caseGraph {
	return &caseGraph{graph: session.Graph{
		Sessions:         []session.Session{{ID: "s", Outcome: session.OutcomeUnknown, WorkingDir: new("/home/alice/repo")}},
		ModelInvocations: []session.ModelInvocation{{ID: "m", SessionID: "s", UserInputID: "u1"}},
	}}
}

func (c *caseGraph) input(id string, ordinal int, origin session.InputOrigin, text string) *caseGraph {
	c.graph.UserInputs = append(c.graph.UserInputs, session.UserInput{
		ID: id, SessionID: "s", Ordinal: ordinal, Text: text, Kind: session.KindPrompt, Origin: origin, Metadata: json.RawMessage(`{}`),
	})
	return c
}

func (c *caseGraph) say(inputID, text string) *caseGraph {
	c.graph.AssistantMessages = append(c.graph.AssistantMessages, session.AssistantMessage{
		ID: "a" + text, SessionID: "s", UserInputID: inputID, ModelInvocationID: "m", Text: new(text),
	})
	return c
}

func (c *caseGraph) tool(inputID, id, name, input string, result session.ToolResult) *caseGraph {
	c.graph.ToolUses = append(c.graph.ToolUses, session.ToolUse{
		ID: id, SessionID: "s", UserInputID: inputID, ModelInvocationID: "m", Name: name,
		Verb: session.VerbOther, Input: json.RawMessage(input), Status: result.Status, Metadata: json.RawMessage(`{}`),
	})
	result.ID, result.ToolUseID, result.SessionID, result.UserInputID = "r"+id, id, "s", inputID
	c.graph.ToolResults = append(c.graph.ToolResults, result)
	return c
}

func (c *caseGraph) edit(inputID, path string) *caseGraph {
	c.graph.FileTouches = append(c.graph.FileTouches, session.FileTouch{
		ID: "f" + path, SessionID: "s", UserInputID: inputID, Path: path, Kind: session.TouchEdit,
	})
	return c
}

// editBy records an edit touch made by a tool call.
func (c *caseGraph) editBy(inputID, toolUseID, path string) *caseGraph {
	c.edit(inputID, path)
	c.graph.FileTouches[len(c.graph.FileTouches)-1].ToolUseID = new(toolUseID)
	return c
}

func (c *caseGraph) render(t *testing.T, opts ...Option) *Transcript {
	t.Helper()
	transcript, err := Render(context.Background(), &c.graph, newRedactor(t), opts...)
	if err != nil {
		t.Fatal(err)
	}
	return transcript
}

// at records where a row was read, as an adapter's vendor id would.
func (c *caseGraph) at(id string, line int) *caseGraph {
	c.graph.VendorIDs = append(c.graph.VendorIDs, session.VendorID{
		OurID: id, SessionID: "s", Vendor: "cursor", VendorIDKind: "line.block", VendorID: id + ":0",
		SourceRef: new("full.jsonl#" + strconv.Itoa(line)),
	})
	return c
}

func TestRenderOrdersRowsBySourcePositionWithoutTimestamps(t *testing.T) {
	text := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Go").
		say("u1", "second").at("asecond", 20).
		say("u1", "first").at("afirst", 10).
		tool("u1", "q", "AskQuestion", `{"questions":[{"prompt":"third?"}]}`, session.ToolResult{Status: session.StatusNoResult}).at("q", 30).
		render(t).Text()

	if first, second, third := strings.Index(text, "first"), strings.Index(text, "second"), strings.Index(text, "third?"); first >= second || second >= third {
		t.Errorf("rows not in source order:\n%s", text)
	}
}

func TestRenderIgnoresQuotedRejectionText(t *testing.T) {
	text := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Debug ingestion").
		tool("u1", "r", "Read", `{}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new("The user doesn't want to proceed with this tool use. ... the user said:\nsecret output"),
		}).
		tool("u1", "b", "Bash", `{}`, session.ToolResult{
			Status: session.StatusError, ErrorText: new("grep: the user said:\nmore output"),
		}).
		render(t).Text()

	if strings.Contains(text, "rejected") || strings.Contains(text, "output") {
		t.Errorf("tool output rendered as a denial:\n%s", text)
	}
}

func TestRenderDropsBashModeAndCompactionInputs(t *testing.T) {
	transcript := newCaseGraph().
		input("u1", 1, session.OriginHuman, "<bash-input>cat .env</bash-input>").
		input("u2", 2, session.OriginHuman, "<bash-stdout>TOKEN=abc</bash-stdout><bash-stderr></bash-stderr>").
		input("u3", 3, session.OriginHuman, "This session is being continued from a previous conversation that ran out of context.").
		render(t)

	if len(transcript.Turns) != 0 {
		t.Errorf("turns = %+v, want none", transcript.Turns)
	}
}

func TestRenderQuestionAnswerEdgeCases(t *testing.T) {
	question := `{"questions":[{"question":"Policy?","options":[{"label":"Report, no threshold"},{"label":"Global threshold"},{"label":"Lint"}]}]}`
	picked := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Go").
		tool("u1", "q", "AskUserQuestion", question, session.ToolResult{
			Status: session.StatusOK,
			Output: json.RawMessage(`{"answers":{"Policy?":"Report, no threshold, Lint"},"annotations":{"Policy?":{"notes":"Only for CI","preview":"ignored"}}}`),
		}).
		render(t).Text()
	for _, want := range []string{"Answer (offered option): Report, no threshold, Lint", "Human note: Only for CI"} {
		if !strings.Contains(picked, want) {
			t.Errorf("transcript lacks %q:\n%s", want, picked)
		}
	}
	if strings.Contains(picked, "ignored") {
		t.Errorf("transcript keeps an option preview:\n%s", picked)
	}

	discussed := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Go").
		tool("u1", "q", "AskUserQuestion", question, session.ToolResult{
			Status: session.StatusInterrupted,
			ErrorText: new("The user doesn't want to proceed with this tool use. The tool use was rejected. To tell you how to proceed, the user said:\n" +
				"The user wants to clarify these questions.\n    This means they may have additional information."),
		}).
		render(t).Text()
	if !strings.Contains(discussed, "chose to discuss the questions") || strings.Contains(discussed, "additional information") {
		t.Errorf("clarify choice not rendered as such:\n%s", discussed)
	}
}

func TestRenderSessionFilesAnchorAndFilterEdits(t *testing.T) {
	c := newCaseGraph()
	c.graph.Sessions[0].WorkingDir = nil
	transcript := c.
		input("u1", 1, session.OriginHuman, "Edit").
		edit("u1", "/elsewhere/repo/ui/app.ts").
		edit("u1", "/elsewhere/other-repo/lib/x.go").
		render(t, WithSessionFiles(
			sessioncorrelate.FileEvidence{Path: "ui/app.ts", Kind: session.TouchEdit},
			sessioncorrelate.FileEvidence{Path: "x.go", Kind: session.TouchRead},
		))

	if got := transcript.Turns[0].EditedFiles; len(got) != 1 || got[0] != "ui/app.ts" {
		t.Errorf("edited files = %v, want [ui/app.ts]", got)
	}
}

func TestMaskLocalPathsKeepsTrailingPunctuation(t *testing.T) {
	for text, want := range map[string]string{
		"see /tmp/a.log.":         "see <local path>.",
		"in /tmp/a, /tmp/b; done": "in <local path>, <local path>; done",
		"files in ~/notes:":       "files in <local path>:",
		"is it /home/alice/x?":    "is it <local path>?",
		"found /home/alice/x!":    "found <local path>!",
		"at /tmp/x.go:12 now":     "at <local path> now",
		"wrote C:\\Users\\a.txt.": "wrote <local path>.",
	} {
		if got := maskLocalPaths(text); got != want {
			t.Errorf("maskLocalPaths(%q) = %q, want %q", text, got, want)
		}
	}
}

func TestRenderRedactsSecretsEmailsAndLocalPaths(t *testing.T) {
	text := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Mail bob@example.com about /home/alice/repo/platform/main.go").
		say("u1", "Using key ghp_JTeKdNnFRIBXuDL7DxtpYlSXpfKtHF4vUCsM from /home/alice/secrets.txt").
		say("u1", "Logs in /tmp/claude/-Users-Alice-work-repo/x and ~/.cursor/projects/Users-alice-repo; tunnel alice.example.dev").
		render(t).Text()

	// alice is the account in the session's recorded working directory, so it
	// is masked in every form, not only inside its home directory.
	for _, leaked := range []string{"bob@example.com", "ghp_JTeKdNnFRIBXuDL7DxtpYlSXpfKtHF4vUCsM", "/home/alice", "/tmp/claude", ".cursor", "lice"} {
		if strings.Contains(text, leaked) {
			t.Errorf("transcript leaks %q:\n%s", leaked, text)
		}
	}
	for _, want := range []string{"about platform/main.go", "Logs in <local path> and <local path>", "tunnel user.example.dev"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
}

func TestRenderMasksJoinedAccountNamesOnly(t *testing.T) {
	c := newCaseGraph()
	c.graph.Sessions[0].WorkingDir = new("/home/will/repo")
	text := c.
		input("u1", 1, session.OriginHuman, "We will ship it; see https://example.com/home/dev and will.example.dev").
		// A home directory inside the repository names nobody.
		edit("u1", "/home/will/repo/src/home/dev/Button.tsx").
		say("u1", "The components render.").
		render(t).Text()

	for _, want := range []string{"We will ship it", "https://example.com/home/dev", "user.example.dev", "The components render."} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
}

func TestRenderAcceptsOlderStoredEditSpellings(t *testing.T) {
	c := newCaseGraph()
	c.graph.Sessions[0].RepoRoot = new("/home/alice/repo")
	transcript := c.
		input("u1", 1, session.OriginHuman, "Edit").
		edit("u1", "/home/alice/repo/ui/app/x.ts").
		edit("u1", "/home/alice/repo/.claude/worktrees/w/platform/a.go").
		edit("u1", "/home/alice/repo/platform/other.go").
		render(t, WithSessionFiles(
			// Stored before roots were preferred and worktree prefixes removed.
			sessioncorrelate.FileEvidence{Path: "app/x.ts", Kind: session.TouchEdit},
			sessioncorrelate.FileEvidence{Path: ".claude/worktrees/w/platform/a.go", Kind: session.TouchEdit},
		))

	if got, want := transcript.Turns[0].EditedFiles, []string{"platform/a.go", "ui/app/x.ts"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("edited files = %v, want %v", got, want)
	}
}

func TestRenderSessionFilesRestoreEditsCutBySubdirectoryCwd(t *testing.T) {
	// Uploaded without repository roots, from an agent working in ui/: its
	// cwd cuts every edit short of the repository root.
	c := newCaseGraph()
	c.graph.Sessions[0].WorkingDir = new("/home/alice/repo/ui")
	c.input("u1", 1, session.OriginHuman, "Edit").
		edit("u1", "/home/alice/repo/ui/app/x.ts").
		edit("u1", "/home/alice/repo/ui/lib/util.ts").
		edit("u1", "/home/alice/repo/ui/app/y.ts").
		edit("u1", "/home/alice/repo/ui/app/z.ts")
	stored := []sessioncorrelate.FileEvidence{
		{Path: "ui/app/x.ts", Kind: session.TouchEdit},
		// Two stored edits end in lib/util.ts, so the edit could be either.
		{Path: "ui/lib/util.ts", Kind: session.TouchEdit},
		{Path: "platform/lib/util.ts", Kind: session.TouchEdit},
		// A row stored with the same short cut confirms the edit, no better.
		{Path: "y.ts", Kind: session.TouchEdit},
		// One file stored in two spellings is still one file.
		{Path: "ui/app/z.ts", Kind: session.TouchEdit},
		{Path: ".claude/worktrees/w/ui/app/z.ts", Kind: session.TouchEdit},
		// A read is no evidence for an edit's path.
		{Path: "oss/app/x.ts", Kind: session.TouchRead},
	}

	got := c.render(t, WithSessionFiles(stored...)).Turns[0].EditedFiles
	if want := []string{"app/y.ts", "ui/app/x.ts", "ui/app/z.ts"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("edited files = %v, want %v", got, want)
	}
	// Without the session's file evidence nothing restores the cut.
	got = c.render(t).Turns[0].EditedFiles
	if want := []string{"app/x.ts", "app/y.ts", "app/z.ts", "lib/util.ts"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("edited files without session files = %v, want %v", got, want)
	}
}

func TestRenderSessionFilesKeepExactStoredSpelling(t *testing.T) {
	c := newCaseGraph()
	c.graph.Sessions[0].RepoRoot = new("/home/alice/repo")
	transcript := c.
		input("u1", 1, session.OriginHuman, "Edit").
		edit("u1", "/home/alice/repo/README.md").
		render(t, WithSessionFiles(
			sessioncorrelate.FileEvidence{Path: "README.md", Kind: session.TouchEdit},
			sessioncorrelate.FileEvidence{Path: "platform/README.md", Kind: session.TouchEdit},
		))

	if got := transcript.Turns[0].EditedFiles; strings.Join(got, ",") != "README.md" {
		t.Errorf("edited files = %v, want [README.md]", got)
	}
}

func TestRenderSessionFilesLeaveTextCutByCwd(t *testing.T) {
	// Text is cut by recorded roots and working directories only; the list
	// takes the stored spelling. Decision files are cited from the list.
	c := newCaseGraph()
	c.graph.Sessions[0].WorkingDir = new("/home/alice/repo/ui")
	transcript := c.
		input("u1", 1, session.OriginHuman, "Edit /home/alice/repo/ui/app/x.ts please").
		edit("u1", "/home/alice/repo/ui/app/x.ts").
		render(t, WithSessionFiles(sessioncorrelate.FileEvidence{Path: "ui/app/x.ts", Kind: session.TouchEdit}))

	text := transcript.Text()
	for _, want := range []string{"Edit app/x.ts please", "Edited files: ui/app/x.ts"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
}

func TestRenderSessionFilesExactSpellingBeatsSeveralLonger(t *testing.T) {
	c := newCaseGraph()
	c.graph.Sessions[0].WorkingDir = new("/home/alice/repo/ui")
	got := c.
		input("u1", 1, session.OriginHuman, "Edit").
		edit("u1", "/home/alice/repo/ui/app/x.ts").
		render(t, WithSessionFiles(
			// The longer edits come first, so ambiguity is seen before the exact one.
			sessioncorrelate.FileEvidence{Path: "ui/app/x.ts", Kind: session.TouchEdit},
			sessioncorrelate.FileEvidence{Path: "platform/app/x.ts", Kind: session.TouchEdit},
			sessioncorrelate.FileEvidence{Path: "app/x.ts", Kind: session.TouchEdit},
		)).Turns[0].EditedFiles

	// Two longer stored edits alone would be ambiguous; the exact one wins.
	if strings.Join(got, ",") != "app/x.ts" {
		t.Errorf("edited files = %v, want [app/x.ts]", got)
	}
}

func TestRenderSessionFilesMatchWholePathSegments(t *testing.T) {
	c := newCaseGraph()
	c.graph.Sessions[0].WorkingDir = new("/home/alice/repo/ui/app")
	got := c.
		input("u1", 1, session.OriginHuman, "Edit").
		edit("u1", "/home/alice/repo/ui/app/x.ts").
		render(t, WithSessionFiles(
			// ui/appx.ts ends in x.ts but not in the segment /x.ts.
			sessioncorrelate.FileEvidence{Path: "ui/appx.ts", Kind: session.TouchEdit},
		)).Turns[0].EditedFiles

	if len(got) != 0 {
		t.Errorf("edited files = %v, want none", got)
	}
}

func TestRenderSessionFilesNestedCwdListsBothSpellings(t *testing.T) {
	// The root works at the repository root and its subagent in ui/, with no
	// roots recorded. Both edit ui/app/x.ts, so Correlate stores the file in
	// both cuts, and each touch's spelling matches its own stored row exactly.
	c := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Edit").
		tool("u1", "spawn", "Agent", `{"prompt":"edit ui"}`, session.ToolResult{Status: session.StatusOK, OutputText: new("Done")}).
		edit("u1", "/home/alice/repo/ui/app/x.ts")
	c.graph.Sessions = append(c.graph.Sessions, session.Session{
		ID: "child", Outcome: session.OutcomeUnknown, WorkingDir: new("/home/alice/repo/ui"),
		ParentSessionID: new("s"), SpawnedByToolUseID: new("spawn"),
	})
	c.graph.FileTouches = append(c.graph.FileTouches, session.FileTouch{
		ID: "fchild", SessionID: "child", Path: "/home/alice/repo/ui/app/x.ts", Kind: session.TouchEdit,
	})

	got := c.render(t, WithSessionFiles(
		sessioncorrelate.FileEvidence{Path: "ui/app/x.ts", Kind: session.TouchEdit},
		sessioncorrelate.FileEvidence{Path: "app/x.ts", Kind: session.TouchEdit},
	)).Turns[0].EditedFiles
	if want := "app/x.ts,ui/app/x.ts"; strings.Join(got, ",") != want {
		t.Errorf("edited files = %v, want [%s]", got, want)
	}
}

func TestRenderEditedFilesAreRepoRelativeWithoutWorktreePrefix(t *testing.T) {
	transcript := newCaseGraph().
		input("u1", 4, session.OriginHuman, "Fix it").
		edit("u1", "/home/alice/repo/.claude/worktrees/eng-1/platform/a.go").
		edit("u1", "/home/alice/repo/ui/b.ts").
		edit("u1", "/home/alice/.claude/plans/plan.md").
		render(t)

	got := transcript.Turns[0].EditedFiles
	if want := []string{"platform/a.go", "ui/b.ts"}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("edited files = %v, want %v", got, want)
	}
	if transcript.Turns[0].Ordinal != 4 {
		t.Errorf("ordinal = %d, want the stored 4", transcript.Turns[0].Ordinal)
	}
}

func TestRenderDialogue(t *testing.T) {
	question := `{"questions":[{"question":"Which store?","options":[{"label":"Postgres"},{"label":"SQLite"}]},` +
		`{"question":"Which checks?","multiSelect":true,"options":[{"label":"lint"},{"label":"test"}]}]}`
	text := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Build it").
		tool("u1", "q", "AskUserQuestion", question, session.ToolResult{
			Status: session.StatusOK, Output: json.RawMessage(`{"answers":{"Which store?":"Use the existing Redis","Which checks?":"lint, test"}}`),
		}).
		tool("u1", "d", "Bash", `{"command":"rm -rf build"}`, session.ToolResult{
			Status:    session.StatusInterrupted,
			ErrorText: new("The user doesn't want to proceed with this tool use. The tool use was rejected. To tell you how to proceed, the user said:\nKeep the build directory"),
		}).
		tool("u1", "e", "Read", `{"file_path":"/home/alice/repo/a.go"}`, session.ToolResult{Status: session.StatusOK, OutputText: new("package a")}).
		render(t).Text()

	for _, want := range []string{
		"Answer (free text): Use the existing Redis",
		"Answer (offered option): lint, test",
		"Human rejected the Bash call and said: Keep the build directory",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
	for _, dropped := range []string{"rm -rf", "package a", "Read"} {
		if strings.Contains(text, dropped) {
			t.Errorf("transcript keeps tool content %q:\n%s", dropped, text)
		}
	}
}

func TestRenderSubagentReports(t *testing.T) {
	text := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Review it").
		tool("u1", "sync", "Agent", `{"prompt":"review"}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new("Sync report"), Output: json.RawMessage(`{"agentId":"a1"}`),
		}).
		tool("u1", "async", "Agent", `{"prompt":"review more"}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new(`Agent "more" finished`), Output: json.RawMessage(`{"agentId":"a2","isAsync":true}`),
		}).
		input("u2", 2, session.OriginSystem, "<task-notification>\n<task-id>a2</task-id>\n<result>Async report</result>\n</task-notification>").
		input("u3", 3, session.OriginSystem, "<task-notification>\n<task-id>shell1</task-id>\n<result>raw shell output</result>\n</task-notification>").
		render(t).Text()

	for _, want := range []string{"Subagent report: Sync report", "## Turn 2", "Subagent report: Async report"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
	for _, dropped := range []string{`"more" finished`, "raw shell output", "## Turn 3"} {
		if strings.Contains(text, dropped) {
			t.Errorf("transcript keeps %q:\n%s", dropped, text)
		}
	}
}

// reported marks the last tool call as one whose result the mapper made its
// subagent's report.
func (c *caseGraph) reported() *caseGraph {
	c.graph.ToolUses[len(c.graph.ToolUses)-1].Metadata = json.RawMessage(`{"subagent_detail":"report"}`)
	return c
}

// A background subagent's report the mapper put on its call is shown there,
// once, for Claude Code and Codex alike.
func TestRenderReportsTheMapperPutOnTheCall(t *testing.T) {
	text := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Review it").
		tool("u1", "async", "Agent", `{"prompt":"review","run_in_background":true}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new("Hand-back report"), Output: json.RawMessage(`{"agentId":"a1","isAsync":true}`),
		}).reported().
		tool("u1", "notified", "Agent", `{"prompt":"probe","run_in_background":true}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new("Notified report"), Output: json.RawMessage(`{"agentId":"a2","isAsync":true}`),
		}).reported().
		tool("u1", "spawn", "spawn_agent", `{"task_name":"review"}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new("Codex final answer"), Output: json.RawMessage(`{"response_output":"{}"}`),
		}).reported().
		tool("u1", "stub", "Agent", `{"prompt":"scan","run_in_background":true}`, session.ToolResult{
			Status: session.StatusOK, OutputText: new("Async agent launched successfully."), Output: json.RawMessage(`{"agentId":"a3","isAsync":true}`),
		}).
		input("u2", 2, session.OriginSystem, "<task-notification>\n<task-id>a2</task-id>\n<result>Notified report</result>\n</task-notification>").
		render(t).Text()

	for _, want := range []string{"Subagent report: Hand-back report", "Subagent report: Codex final answer"} {
		if !strings.Contains(text, want) {
			t.Errorf("transcript lacks %q:\n%s", want, text)
		}
	}
	if got := strings.Count(text, "Notified report"); got != 1 {
		t.Errorf("the notified report appears %d times, want once:\n%s", got, text)
	}
	if strings.Contains(text, "Async agent launched") {
		t.Errorf("transcript keeps a launch stub:\n%s", text)
	}
}

// goldenRender compares the transcript text and the per-turn edited files
// with the synthetic goldens in dir. -update rewrites them.
func goldenRender(t *testing.T, dir string, graph *session.Graph, opts ...Option) *Transcript {
	t.Helper()
	transcript, err := Render(context.Background(), graph, newRedactor(t), opts...)
	if err != nil {
		t.Fatal(err)
	}
	goldentest.Text(t, filepath.Join(dir, "transcript.txt"), transcript.Text())
	goldentest.Golden(t, filepath.Join(dir, "turns.json"), transcript.Turns)
	return transcript
}

func TestRenderDenialFeedbackFixture(t *testing.T) {
	rejected := "The user doesn't want to proceed with this tool use. The tool use was rejected (eg. if it was a file edit, the new_string was NOT written to the file)."
	c := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Add a migration for the uploads table").
		say("u1", "I'll write the migration.").at("aI'll write the migration.", 1).
		tool("u1", "w", "Write", `{"file_path":"/home/alice/repo/migrations/0007_uploads.up.sql","content":"ALTER TABLE runs ADD COLUMN upload_id UUID;"}`, session.ToolResult{
			Status: session.StatusError, ErrorText: new(rejected + " To tell you how to proceed, the user said:\nDon't alter runs; change the create in 0001 instead"),
		}).at("w", 2).
		editBy("u1", "w", "/home/alice/repo/migrations/0007_uploads.up.sql").
		say("u1", "Changing the create statement instead.").at("aChanging the create statement instead.", 3).
		edit("u1", "/home/alice/repo/migrations/0001_init.up.sql").
		tool("u1", "b", "Bash", `{"command":"psql -c 'drop table runs'"}`, session.ToolResult{
			Status: session.StatusInterrupted, ErrorText: new(rejected + " STOP what you are doing and wait for the user to tell you how to proceed."),
		}).at("b", 4).
		tool("u1", "w2", "Write", `{"file_path":"/home/alice/repo/migrations/0008_cleanup.up.sql","content":"DELETE FROM runs;"}`, session.ToolResult{
			Status: session.StatusError, ErrorText: new(rejected + " The user provided the following reason for the rejection:  no cleanup migration; as the user said:\nkeep it minimal"),
		}).at("w2", 5).
		editBy("u1", "w2", "/home/alice/repo/migrations/0008_cleanup.up.sql")
	c.graph.Sessions[0].RepoRoot = new("/home/alice/repo")
	text := goldenRender(t, filepath.Join("testdata", "synthetic_denial_feedback"), &c.graph).Text()
	for _, dropped := range []string{"ALTER TABLE", "drop table", "DELETE FROM", "0007_uploads", "0008_cleanup"} {
		if strings.Contains(text, dropped) {
			t.Errorf("transcript keeps denied tool input or edit %q", dropped)
		}
	}
}

func TestRenderQuestionNoteFixture(t *testing.T) {
	question := `{"questions":[{"question":"Where should retries live?","options":[{"label":"In the worker"},{"label":"In the queue"}]}]}`
	c := newCaseGraph().
		input("u1", 1, session.OriginHuman, "Make uploads retry on failure").
		tool("u1", "q", "AskUserQuestion", question, session.ToolResult{
			Status: session.StatusOK,
			Output: json.RawMessage(`{"answers":{"Where should retries live?":"In the worker"},` +
				`"annotations":{"Where should retries live?":{"notes":"Cap it at three attempts","preview":"worker.go diff"}}}`),
		}).at("q", 1).
		say("u1", "Retries go in the worker, capped at three attempts.").at("aRetries go in the worker, capped at three attempts.", 2).
		edit("u1", "/home/alice/repo/platform/worker.go")
	goldenRender(t, filepath.Join("testdata", "synthetic_question_note"), &c.graph)
}

// TestRenderRedactionFixture keeps a golden of what masking leaves. The fake
// secrets are assembled here so no committed file holds one whole.
func TestRenderRedactionFixture(t *testing.T) {
	var (
		password = "Tr0ub4dor" + "-3xkq9Lm"
		bearer   = "eyJhbGciOiJIUzI1NiJ9" + "." + "eyJzdWIiOiJkZXYifQ" + "." + "c2lnbmF0dXJlLXZhbHVl"
		apiKey   = "sk_live_" + "Zq7Rw2Lp9Xv4Mt6Ny8Kb3Hd5"
		pem      = "-----BEGIN RSA " + "PRIVATE KEY-----\nMIIEpAIBAAKCAQEA7Qx1\n-----END RSA PRIVATE KEY-----"
	)
	c := newCaseGraph()
	c.graph.Sessions[0].RepoRoot = new("/home/alice/repo")
	c.graph.Sessions[0].WorktreeRoot = new("/home/alice/repo/.claude/worktrees/eng-1")
	c.graph.Sessions[0].WorkingDir = new("/home/alice/repo/.claude/worktrees/eng-1")
	c.input("u1", 1, session.OriginHuman, "Deploy with postgres://app:"+password+"@db.example.com:5432/app and mail bob@example.com when done.\n"+
		"Then fix /home/alice/repo/.claude/worktrees/eng-1/platform/deploy.go and /home/alice/repo/ui/app.ts.").
		say("u1", "The request sends `Authorization: Bearer "+bearer+"` and the config has api_key = "+apiKey+".").
		say("u1", "The key file reads:\n"+pem).
		say("u1", "Logs are in /tmp/build-42/out.log, ~/.ssh/config and /home/alice/notes.txt; Cursor keeps -Users-alice-repo-.\n"+
			"Pushed alice/feature-retry to alice.example.dev for Alice's review; alice will merge.").
		edit("u1", "/home/alice/repo/.claude/worktrees/eng-1/platform/deploy.go").
		edit("u1", "/home/alice/repo/.claude/worktrees/eng-1/ui/app.ts")
	text := goldenRender(t, filepath.Join("testdata", "synthetic_redaction"), &c.graph).Text()
	for _, leaked := range []string{password, bearer, apiKey, "MIIEpAIBAAKCAQEA7Qx1", "bob@example.com", "/home/alice", "/tmp/build", ".ssh", "alice.example", "alice/feature", "-alice-"} {
		if strings.Contains(text, leaked) {
			t.Errorf("transcript leaks %q", leaked)
		}
	}
}

func TestRenderNeedsARootSession(t *testing.T) {
	if _, err := Render(context.Background(), &session.Graph{}, newRedactor(t)); err == nil {
		t.Fatal("Render accepted a graph without a root session")
	}
	if _, err := NewTree(nil); err == nil {
		t.Fatal("NewTree accepted a nil graph")
	}
}
