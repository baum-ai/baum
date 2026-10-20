package locallink

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/bundle"
	"github.com/baum-ai/baum/internal/locallink/linktest"
	"github.com/baum-ai/baum/linkage"
	"github.com/baum-ai/baum/sessions"
	"github.com/baum-ai/baum/sessions/session"
)

func minute(n int) *time.Time {
	at := time.Date(2026, 7, 1, 10, 0, 0, 0, time.UTC).Add(time.Duration(n) * time.Minute)
	return &at
}

func text(s string) *string { return &s }

// evidenceTree is a synthetic session tree: a top-level session whose second
// prompt spawned a subagent, which spawned one of its own, with file touches
// and shell commands at every level.
func evidenceTree() sessions.Session {
	root, child, spawn, spawnNested := "s-root", "s-child", "t-spawn", "t-spawn-nested"
	model := func(id, sid, inputID string, name *string) session.ModelInvocation {
		return session.ModelInvocation{ID: id, SessionID: sid, UserInputID: inputID, Model: name}
	}
	input := func(id, sid string, ordinal int, kind session.InputKind, origin session.InputOrigin, prompt string) session.UserInput {
		return session.UserInput{ID: id, SessionID: sid, Ordinal: ordinal, Kind: kind, Origin: origin, Text: prompt, TriggeredAt: minute(ordinal)}
	}
	use := func(id, sid, inputID string, verb session.ToolVerb, raw string, at *time.Time) session.ToolUse {
		return session.ToolUse{ID: id, SessionID: sid, UserInputID: inputID, Verb: verb, Input: json.RawMessage(raw), Timestamp: at}
	}
	touch := func(id, sid, inputID, path string, kind session.TouchKind) session.FileTouch {
		return session.FileTouch{ID: id, SessionID: sid, UserInputID: inputID, Path: path, Kind: kind}
	}
	graph := &session.Graph{
		Sessions: []session.Session{
			{
				ID: root, Title: text("Build the widget"), GitBranch: text("feature"), WorkingDir: text("/home/dev/widgets"),
				StartedAt: minute(0), EndedAt: minute(60), Outcome: session.OutcomeCommitted,
			},
			{ID: child, ParentSessionID: &root, SpawnedByToolUseID: &spawn, Outcome: session.OutcomeUnknown},
			{ID: "s-nested", ParentSessionID: &child, SpawnedByToolUseID: &spawnNested, Outcome: session.OutcomeUnknown},
		},
		UserInputs: []session.UserInput{
			input("u-1", root, 1, session.KindPrompt, session.OriginHuman, "Start the widget"),
			input("u-2", root, 2, session.KindPrompt, session.OriginHuman, "Delegate the parts"),
			input("u-interrupt", root, 3, session.KindInterrupt, session.OriginHuman, "[Request interrupted by user]"),
			input("u-system", root, 4, session.KindPrompt, session.OriginSystem, "injected context"),
			input("u-3", root, 5, session.KindPrompt, session.OriginHuman, "Polish the page"),
			input("u-4", root, 6, session.KindSlashCommand, session.OriginHuman, "/review"),
			input("u-blank", root, 7, session.KindPrompt, session.OriginHuman, "  \n"),
			input("u-child", child, 1, session.KindPrompt, session.OriginAgent, "Write the parts"),
			input("u-nested", "s-nested", 1, session.KindPrompt, session.OriginAgent, "Document the parts"),
		},
		ModelInvocations: []session.ModelInvocation{
			model("m-1", root, "u-1", text("model-b")),
			model("m-2", child, "u-child", text("model-a")),
			model("m-3", "s-nested", "u-nested", nil),
			model("m-4", root, "u-3", text("model-b")),
		},
		AssistantMessages: []session.AssistantMessage{
			{ID: "a-1", SessionID: root, UserInputID: "u-1", ModelInvocationID: "m-1", Text: text("Earlier reply"), Timestamp: minute(1)},
			{ID: "a-2", SessionID: root, UserInputID: "u-1", ModelInvocationID: "m-1", Text: text("Final reply"), Timestamp: minute(2)},
			{ID: "a-3", SessionID: root, UserInputID: "u-1", ModelInvocationID: "m-1", Text: text("   "), Timestamp: minute(3)},
			{ID: "a-4", SessionID: child, UserInputID: "u-child", ModelInvocationID: "m-2", Text: text("Subagent reply")},
		},
		ToolUses: []session.ToolUse{
			use(spawn, root, "u-2", session.VerbSpawnSubagent, `{"prompt":"Write the parts"}`, minute(10)),
			use(spawnNested, child, "u-child", session.VerbSpawnSubagent, `{"prompt":"Document the parts"}`, minute(11)),
			use("e-1", root, "u-1", session.VerbExecute, `{"command":"go build ./... && go test ./... | tee /tmp/build-output.log"}`, minute(5)),
			use("e-2", child, "u-child", session.VerbExecute, `{"command":"/usr/local/go/bin/go vet ./pkg/..."}`, minute(12)),
			use("e-3", "s-nested", "u-nested", session.VerbExecute, `{"cmd":["git","status"]}`, minute(13)),
			use("e-4", root, "u-3", session.VerbExecute, `{"command":"npm test"}`, minute(20)),
			use("e-5", root, "u-3", session.VerbExecute, `{"shell_id":"poll"}`, minute(21)),
			use("e-6", root, "u-4", session.VerbExecute, `{"command":"make lint"}`, nil),
			use("e-7", root, "u-4", session.VerbExecute, `{"command":"ssh db.internal.example.com && cat ./main.go docs/notes.md && ls docs"}`, nil),
		},
		ToolResults: []session.ToolResult{
			{ID: "r-1", ToolUseID: "e-1", SessionID: root, UserInputID: "u-1", Status: session.StatusOK, OutputText: text("ok\tpkg built from /home/dev/widgets")},
			{ID: "r-2", ToolUseID: "e-2", SessionID: child, UserInputID: "u-child", Status: session.StatusError, ErrorText: text("vet: failure in /home/dev/widgets/pkg")},
			{ID: "r-4", ToolUseID: "e-4", SessionID: root, UserInputID: "u-3", Status: session.StatusInterrupted},
		},
		FileTouches: []session.FileTouch{
			touch("f-1", root, "u-1", "/home/dev/widgets/main.go", session.TouchEdit),
			touch("f-2", root, "u-1", "/home/dev/widgets/main.go", session.TouchRead),
			touch("f-3", child, "u-child", "/home/dev/widgets/pkg/parts.go", session.TouchEdit),
			touch("f-4", "s-nested", "u-nested", "/home/dev/widgets/docs/parts.md", session.TouchRead),
			touch("f-5", root, "u-2", "/etc/hosts", session.TouchRead),
			touch("f-6", root, "u-3", "web/page.ts", session.TouchEdit),
			touch("f-7", root, "u-system", "/home/dev/widgets/ignored.go", session.TouchRead),
		},
	}
	return sessions.Session{Source: "claude_code", Vendor: "claude_code", ID: "vendor-session", Graph: graph}
}

func evidenceOf(t *testing.T, s sessions.Session, maxPrompts, maxCommandEvents int) bundle.Session {
	t.Helper()
	item, ok := sessionEvidence(s, 0.9, []string{"/home/dev/widgets"}, bundle.KnownPaths("docs/notes.md"), maxPrompts, maxCommandEvents)
	if !ok {
		t.Fatal("no top-level session")
	}
	return item
}

func TestSessionEvidenceMetadata(t *testing.T) {
	got := evidenceOf(t, evidenceTree(), 10, 0)
	if got.RootSessionID != "vendor-session" || got.Vendor != "claude_code" || got.Restricted ||
		got.Title != "Build the widget" || got.Branch != "feature" || got.Outcome != "committed" {
		t.Errorf("session = %+v", got)
	}
	if !reflect.DeepEqual(got.Models, []string{"model-a", "model-b"}) {
		t.Errorf("models = %v", got.Models)
	}
	if got.StartedAt == nil || !got.StartedAt.Equal(*minute(0)) || got.EndedAt == nil || !got.EndedAt.Equal(*minute(60)) ||
		!got.HasStartedAt || !got.StartedAtValue.Equal(*minute(0)) || got.Confidence != 0.9 {
		t.Errorf("timing or sort keys = %+v", got)
	}
}

// Only the top-level session's human prompts are prompts; what the sessions a
// prompt spawned touched, at any depth, counts toward that prompt.
func TestSessionEvidenceAttributesNestedSessionsToTheirRootPrompt(t *testing.T) {
	got := evidenceOf(t, evidenceTree(), 10, 0)
	type prompt struct {
		Index   int
		ID      string
		Text    string
		Reply   string
		Touched []string
	}
	var prompts []prompt
	for _, p := range got.Prompts {
		prompts = append(prompts, prompt{p.Index, p.ID, p.Text, p.Reply, p.TouchedPaths})
	}
	want := []prompt{
		{1, "u-1", "Start the widget", "Final reply", []string{"main.go"}},
		{2, "u-2", "Delegate the parts", "", []string{"docs/parts.md", "pkg/parts.go"}},
		{5, "u-3", "Polish the page", "", []string{"web/page.ts"}},
		{6, "u-4", "/review", "", nil},
	}
	if !reflect.DeepEqual(prompts, want) {
		t.Errorf("prompts =\n%+v\nwant\n%+v", prompts, want)
	}
	if at := got.Prompts[1].At; at == nil || !at.Equal(*minute(2)) {
		t.Errorf("prompt time = %v", at)
	}
}

// A nested session whose spawn does not reach the top-level session is not
// credited to any prompt.
func TestSessionEvidenceIgnoresUnreachableNestedSessions(t *testing.T) {
	tree := evidenceTree()
	tree.Graph.ToolUses = tree.Graph.ToolUses[1:] // the subagent's spawn is gone
	got := evidenceOf(t, tree, 10, 0)
	if paths := got.Prompts[1].TouchedPaths; paths != nil {
		t.Errorf("touched paths of the spawning prompt = %v, want none", paths)
	}
}

func TestSessionEvidenceKeepsFirstAndLatestPrompts(t *testing.T) {
	for limit, want := range map[int][]int{0: nil, 1: {1}, 2: {1, 6}, 3: {1, 5, 6}, 4: {1, 2, 5, 6}, 9: {1, 2, 5, 6}} {
		var got []int
		for _, p := range evidenceOf(t, evidenceTree(), limit, 0).Prompts {
			got = append(got, p.Index)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("limit %d: prompt indexes = %v, want %v", limit, got, want)
		}
	}
}

func TestSessionEvidenceCommandEvents(t *testing.T) {
	got := evidenceOf(t, evidenceTree(), 0, 0).CommandEvents
	want := []bundle.CommandEvent{
		{ID: "e-1", Time: minute(5), Outcome: "succeeded", Commands: []bundle.CommandEntry{
			{Program: "go", Category: "build"},
			{Program: "go", Category: "test"},
			{Program: "tee", Category: "other"},
		}},
		{ID: "e-2", Time: minute(12), Outcome: "failed", Commands: []bundle.CommandEntry{{Program: "go", Category: "lint", Target: "pkg/..."}}},
		{ID: "e-3", Time: minute(13), Outcome: "unknown", Commands: []bundle.CommandEntry{{Program: "git", Category: "git"}}},
		{ID: "e-4", Time: minute(20), Outcome: "interrupted", Commands: []bundle.CommandEntry{{Program: "npm", Category: "test"}}},
		{ID: "e-6", Outcome: "unknown", Commands: []bundle.CommandEntry{{Program: "make", Category: "build"}}},
		// A target needs a known path: one the session touched, a directory
		// above one, or one the host knows (here docs/notes.md).
		{ID: "e-7", Outcome: "unknown", Commands: []bundle.CommandEntry{
			{Program: "ssh", Category: "other"},
			{Program: "cat", Category: "other", Target: "main.go"},
			{Program: "ls", Category: "other"},
		}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("command events =\n%+v\nwant\n%+v", got, want)
	}

	var ids []string
	for _, event := range evidenceOf(t, evidenceTree(), 0, 2).CommandEvents {
		ids = append(ids, event.ID)
	}
	if !reflect.DeepEqual(ids, []string{"e-3", "e-4"}) {
		t.Errorf("latest two command events = %v, want [e-3 e-4]", ids)
	}
}

// No raw command line, tool output or absolute path leaves the session tree.
func TestSessionEvidenceCarriesNoRawCommandsOutputsOrAbsolutePaths(t *testing.T) {
	encoded, err := json.Marshal(evidenceOf(t, evidenceTree(), 10, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"/home/dev", "/tmp", "/usr/local", "/etc/hosts", "&&", "| tee", "go build", "pkg built", "vet: failure", "shell_id"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("session evidence leaks %q:\n%s", leaked, encoded)
		}
	}
}

func TestSessionEvidenceWithoutTopLevelSession(t *testing.T) {
	if _, ok := sessionEvidence(sessions.Session{ID: "x", Graph: &session.Graph{}}, 1, nil, nil, 1, 1); ok {
		t.Error("an empty graph has no session evidence")
	}
}

func TestWithoutGenerationRuns(t *testing.T) {
	marked := evidenceTree()
	marked.ID = "generation"
	marked.Graph.UserInputs[0].Text = sessions.GenerationRunMarker + "\nReview the pull request."
	ordinary := evidenceTree()
	kept, truth := withoutGenerationRuns(
		[]sessions.Session{ordinary, marked, {ID: "generation", Graph: &session.Graph{}}},
		map[string][]string{"c1": {"vendor-session", "generation"}, "c2": {"generation"}},
	)
	if len(kept) != 1 || kept[0].ID != "vendor-session" {
		t.Errorf("kept = %v", kept)
	}
	if !reflect.DeepEqual(truth, map[string][]string{"c1": {"vendor-session"}}) {
		t.Errorf("truth = %v", truth)
	}
}

func buildEvidence(t *testing.T, req EvidenceRequest) []bundle.Session {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := BuildSessionEvidence(ctx, req)
	if err != nil {
		t.Fatalf("BuildSessionEvidence: %v", err)
	}
	return got
}

func evidenceRequest(t *testing.T, f linktest.Fixture) EvidenceRequest {
	return EvidenceRequest{Dir: f.Repo, Commits: commitsOf(t, f), Sessions: f.Store, MaxPromptsPerSession: 5, MaxCommandEventsPerSession: 5}
}

func TestBuildSessionEvidenceLinksTheSessionThatCommitted(t *testing.T) {
	f := linktest.New(t)
	got := buildEvidence(t, evidenceRequest(t, f))
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	s := got[0]
	if s.RootSessionID != linktest.SessionID || s.Vendor != "claude_code" || s.Branch != "feature" ||
		!reflect.DeepEqual(s.Models, []string{"claude-opus-5"}) || s.Confidence < 0.9 || s.StartedAt == nil {
		t.Errorf("session = %+v", s)
	}
	if len(s.Prompts) != 1 || s.Prompts[0].Text != "add the feature" || s.Prompts[0].ID == "" {
		t.Errorf("prompts = %+v", s.Prompts)
	}
	want := []bundle.CommandEntry{{Program: "git", Category: "git"}}
	if len(s.CommandEvents) != 1 || s.CommandEvents[0].Outcome != "succeeded" || !reflect.DeepEqual(s.CommandEvents[0].Commands, want) {
		t.Errorf("command events = %+v", s.CommandEvents)
	}
}

func TestBuildSessionEvidenceWithoutCommits(t *testing.T) {
	got := buildEvidence(t, EvidenceRequest{Dir: t.TempDir()})
	if got == nil || len(got) != 0 {
		t.Errorf("sessions = %#v, want an empty list", got)
	}
}

// Text the sessions return is masked by the bundle's input redaction, which
// runs before trimming, and never reaches the encoded bundle.
func TestBuildSessionEvidenceIsRedactedByTheBundle(t *testing.T) {
	f := linktest.New(t)
	f.AddSessionWithSubagent(t, "bbbbbbbb-0000-4000-8000-000000000002", f.Repo, "mail "+linktest.Email+" the key "+linktest.Secret)
	sessionsFound := buildEvidence(t, evidenceRequest(t, f))
	if len(sessionsFound) != 2 {
		t.Fatalf("sessions = %+v", sessionsFound)
	}
	redactor, err := bundle.NewRedactor()
	if err != nil {
		t.Fatal(err)
	}
	b := bundle.Bundle{Repository: linktest.Repository, Sessions: sessionsFound, Sources: bundle.Sources{"sessions": true}}
	caps := bundle.Caps{MaxSessions: 10, MaxBytes: 1 << 20}
	bundle.ApplyCaps(&b, caps)
	if err := bundle.Redact(context.Background(), &b, redactor); err != nil {
		t.Fatal(err)
	}
	if err := bundle.Fit(&b, caps); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	// The subagent read and ran commands on absolute paths in the clone, and
	// its command set a token: only the repository-relative path is kept.
	for _, leaked := range []string{linktest.Email, "ada.lovelace", linktest.Secret, f.Repo, filepath.Base(f.Repo) + "/", "hunter2", "API_TOKEN", "FAIL in"} {
		if strings.Contains(string(encoded), leaked) {
			t.Errorf("bundle leaks %q:\n%s", leaked, encoded)
		}
	}
	if !strings.Contains(string(encoded), `"touched_paths":["b.txt"]`) {
		t.Errorf("the subagent's read should be kept as a repository-relative path:\n%s", encoded)
	}
	if b.MaskedSpanCount < 4 || !strings.Contains(string(encoded), `"text":"mail `) || !strings.Contains(string(encoded), "about the feature") {
		t.Errorf("masked %d spans; the masked text should survive:\n%s", b.MaskedSpanCount, encoded)
	}
}

func TestBuildSessionEvidenceExcludesGenerationRuns(t *testing.T) {
	f := linktest.New(t)
	f.AddSession(t, "cccccccc-0000-4000-8000-000000000003", f.Repo, sessions.GenerationRunMarker+"\nReview the pull request.")
	got := buildEvidence(t, evidenceRequest(t, f))
	if len(got) != 1 || got[0].RootSessionID != linktest.SessionID {
		t.Errorf("sessions = %+v, want only the developer's", got)
	}

	alone := f.NoSession(t)
	alone.AddSession(t, "cccccccc-0000-4000-8000-000000000003", f.Repo, sessions.GenerationRunMarker+"\nReview the pull request.")
	if got := buildEvidence(t, evidenceRequest(t, alone)); len(got) != 0 {
		t.Errorf("sessions = %+v, want none", got)
	}
}

func TestBuildSessionEvidenceReadsPreviousPaths(t *testing.T) {
	f := linktest.New(t).NoSession(t)
	moved := filepath.Join(t.TempDir(), "old-widgets")
	f.AddSession(t, "dddddddd-0000-4000-8000-000000000004", moved, "add the feature where it used to live")
	if got := buildEvidence(t, evidenceRequest(t, f)); len(got) != 0 {
		t.Fatalf("without the previous path: sessions = %+v", got)
	}
	req := evidenceRequest(t, f)
	req.Sessions.PreviousPaths = []string{moved}
	got := buildEvidence(t, req)
	if len(got) != 1 || got[0].RootSessionID != "dddddddd-0000-4000-8000-000000000004" {
		t.Errorf("with the previous path: sessions = %+v", got)
	}
}

func TestSessionEvidenceBranchIsTheTopLevelSessions(t *testing.T) {
	tree := evidenceTree()
	tree.Graph.Sessions[0].GitBranch = nil
	tree.Graph.Sessions[1].GitBranch = text("another-repositorys-branch")
	if got := evidenceOf(t, tree, 1, 0); got.Branch != "" {
		t.Errorf("branch = %q, want none: only the top-level session's counts", got.Branch)
	}
}

// A touched path outside the clone's checkouts is dropped, even when the
// session ran in a directory above the clone that would make it relative.
func TestSessionEvidenceKeepsOnlyPathsInTheClone(t *testing.T) {
	tree := evidenceTree()
	tree.Graph.Sessions[0].WorkingDir = text("/home/dev")
	tree.Graph.FileTouches = append(tree.Graph.FileTouches,
		session.FileTouch{ID: "f-8", SessionID: "s-root", UserInputID: "u-1", Path: "/home/dev/other-client/secrets.env", Kind: session.TouchRead},
		session.FileTouch{ID: "f-9", SessionID: "s-root", UserInputID: "u-1", Path: "/home/dev/widgets/.claude/worktrees/feat/pkg/a.go", Kind: session.TouchEdit})
	got := evidenceOf(t, tree, 1, 0)
	if want := []string{"main.go", "pkg/a.go"}; !reflect.DeepEqual(got.Prompts[0].TouchedPaths, want) {
		t.Errorf("touched paths = %v, want %v", got.Prompts[0].TouchedPaths, want)
	}
}

func TestSessionEvidenceBoundsPromptAndReplyText(t *testing.T) {
	tree := evidenceTree()
	long := strings.Repeat("é", bundle.PromptScanChars+10)
	tree.Graph.UserInputs[0].Text = long
	tree.Graph.AssistantMessages[1].Text = &long
	prompt := evidenceOf(t, tree, 1, 0).Prompts[0]
	if want := strings.Repeat("é", bundle.PromptScanChars); prompt.Text != want || prompt.Reply != want {
		t.Errorf("prompt and reply are %d and %d characters, want %d", len([]rune(prompt.Text)), len([]rune(prompt.Reply)), bundle.PromptScanChars)
	}
}

func TestSortSessionEvidence(t *testing.T) {
	session := func(id string, confidence float64, started *time.Time) bundle.Session {
		s := bundle.Session{RootSessionID: id, Confidence: confidence}
		if started != nil {
			s.StartedAt, s.HasStartedAt, s.StartedAtValue = started, true, *started
		}
		return s
	}
	got := []bundle.Session{
		session("e-unknown-start", 0.9, nil),
		session("f-low", 0.5, minute(0)),
		session("d-late", 0.9, minute(30)),
		session("b-tie", 0.9, minute(10)),
		session("a-tie", 0.9, minute(10)),
		session("c-certain", 1, minute(50)),
		session("a-unknown-start", 0.9, nil),
	}
	sortSessionEvidence(got)
	var ids []string
	for _, s := range got {
		ids = append(ids, s.RootSessionID)
	}
	want := []string{"c-certain", "a-tie", "b-tie", "d-late", "a-unknown-start", "e-unknown-start", "f-low"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("order = %v, want %v", ids, want)
	}
}

func TestLinkConfidenceIsTheBestEdge(t *testing.T) {
	got := linkConfidence([]linkage.Edge{
		{CommitSHA: "c1", RunID: "a", Confidence: 0.55},
		{CommitSHA: "c2", RunID: "a", Confidence: 0.95},
		{CommitSHA: "c3", RunID: "a", Confidence: 0.7},
		{CommitSHA: "c1", RunID: "b", Confidence: 0.6},
	})
	if want := map[string]float64{"a": 0.95, "b": 0.6}; !reflect.DeepEqual(got, want) {
		t.Errorf("confidence = %v, want %v", got, want)
	}
}

// A subagent's file reads and commands count toward the top-level prompt that
// spawned it, end to end from a Claude Code store.
func TestBuildSessionEvidenceAttributesASubagent(t *testing.T) {
	f := linktest.New(t).NoSession(t)
	f.AddSessionWithSubagent(t, "eeeeeeee-0000-4000-8000-000000000005", f.Repo, "add the feature")
	got := buildEvidence(t, evidenceRequest(t, f))
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	s := got[0]
	if !reflect.DeepEqual(s.Models, []string{"claude-haiku-5", "claude-opus-5"}) {
		t.Errorf("models = %v", s.Models)
	}
	if len(s.Prompts) != 1 || s.Prompts[0].Text != "add the feature" || !reflect.DeepEqual(s.Prompts[0].TouchedPaths, []string{"b.txt"}) {
		t.Errorf("prompts = %+v", s.Prompts)
	}
	type event struct {
		Outcome  string
		Commands []bundle.CommandEntry
	}
	var events []event
	for _, e := range s.CommandEvents {
		events = append(events, event{e.Outcome, e.Commands})
	}
	want := []event{
		{"succeeded", []bundle.CommandEntry{{Program: "git", Category: "git"}}},
		{"failed", []bundle.CommandEntry{{Program: "cat", Category: "other"}, {Program: "go", Category: "test"}}},
	}
	if !reflect.DeepEqual(events, want) {
		t.Errorf("command events = %+v, want %+v", events, want)
	}
}

// baum internal view's /api/sessions leaves baum's own generation runs out too, so it
// links the same sessions the bundle does.
func TestResolveExcludesGenerationRuns(t *testing.T) {
	f := linktest.New(t)
	f.AddSession(t, "cccccccc-0000-4000-8000-000000000003", f.Repo, sessions.GenerationRunMarker+"\nReview the pull request.")
	result := resolve(t, request(f))
	if !result.Available || len(result.Sessions) != 1 || result.Sessions[0].ID != linktest.SessionID {
		t.Fatalf("sessions = %+v", result.Sessions)
	}
	if links := result.Commits[0].Links; len(links) != 1 || links[0].Session != linktest.SessionID {
		t.Errorf("first commit links = %+v", links)
	}
	if _, kept := result.Graphs["cccccccc-0000-4000-8000-000000000003"]; kept {
		t.Error("the generation run's graph must not be kept")
	}
}

func TestExistsInClone(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	for _, dir := range []string{filepath.Join(root, "src"), filepath.Join(root, "docs"), filepath.Join(outside, "private")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "src", "x.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Skip("symbolic links are not available:", err)
	}
	known := existsInClone(root)
	for candidate, want := range map[string]bool{
		"src/x.go": true, "src": true, "./src/x.go": true, "docs": true,
		"src/new.go": false, "docs/hunter2.pw": false, "src/Main.go": false,
		"new.go": false, "db.internal.example.com": false, "src/missing/x.go": false,
		"escape": false, "escape/private": false, "escape/private/new.txt": false,
		"../x.go": false, ".": false,
	} {
		if got := known(candidate); got != want {
			t.Errorf("existsInClone(%q) = %v, want %v", candidate, got, want)
		}
	}
	// A path-shaped leaf under an existing directory is not a target unless
	// the file itself exists.
	for command, want := range map[string]string{
		"cat docs/hunter2.pw": "", "cat src/Main.go": "", "cat src/x.go": "src/x.go",
	} {
		if entries := bundle.ParseCommandParaphrase(command, known); len(entries) != 1 || entries[0].Target != want {
			t.Errorf("%s: entries %+v, want target %q", command, entries, want)
		}
	}
}

func TestTrustedRoot(t *testing.T) {
	home := filepath.Join(string(filepath.Separator), "home", "user")
	for dir, want := range map[string]bool{
		string(filepath.Separator):         false,
		home:                               false,
		filepath.Dir(home):                 false,
		filepath.Join(home, "src", "repo"): true,
		filepath.Join(string(filepath.Separator), "srv", "repo"): true,
	} {
		if got := trustedRoot(dir, home); got != want {
			t.Errorf("trustedRoot(%q) = %v, want %v", dir, got, want)
		}
	}
}

// A previous path that would make every file pass for the repository's is
// not a clone root, and a clone whose worktrees cannot be listed says so.
func TestCloneRootsDropsUntrustedPreviousPaths(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory:", err)
	}
	notAClone := t.TempDir()
	previous := filepath.Join(t.TempDir(), "old-widgets")
	var warnings bytes.Buffer
	roots := cloneRoots(context.Background(), notAClone, notAClone, []string{string(filepath.Separator), home, previous}, &warnings)
	if want := []string{notAClone, previous}; !reflect.DeepEqual(roots, want) {
		t.Errorf("roots = %v, want %v", roots, want)
	}
	text := warnings.String()
	if !strings.Contains(text, "listing the clone's worktrees") || strings.Count(text, "is not used to bound") != 2 {
		t.Errorf("warnings =\n%s", text)
	}
}
