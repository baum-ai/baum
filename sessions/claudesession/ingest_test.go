package claudesession

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lastpersonlabs/reviewpal/internal/goldentest"
	"github.com/lastpersonlabs/reviewpal/sessions/claudetranscript"
	"github.com/lastpersonlabs/reviewpal/sessions/collector/claudecode"
	"github.com/lastpersonlabs/reviewpal/sessions/session"
)

func buildClaudeLines(t *testing.T, lines ...string) (*session.Graph, *Report) {
	t.Helper()
	decoded, torn, err := claudetranscript.Decode(bytes.NewBufferString(strings.Join(lines, "\n") + "\n"))
	if err != nil || torn {
		t.Fatalf("decode: torn=%v err=%v", torn, err)
	}
	tree, err := claudetranscript.SingleFileTree(&claudetranscript.File{
		Path:      "11111111-2222-4333-8444-555555555555.jsonl",
		SessionID: "11111111-2222-4333-8444-555555555555", Lines: decoded,
	})
	if err != nil {
		t.Fatal(err)
	}
	graph, report, err := Build(tree)
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return graph, report
}

// fixturePath is the shared transcript tree, which lives with the parser that
// reads it rather than being copied here.
func fixturePath() string {
	return filepath.Join("..", "claudetranscript", "testdata",
		"11111111-2222-3333-4444-555555555555.jsonl")
}

func buildFixture(t *testing.T) (*session.Graph, *Report) {
	t.Helper()
	tree, err := claudetranscript.DecodeLocalFiles(claudecode.SessionFiles(fixturePath()))
	if err != nil {
		t.Fatalf("DecodeLocalFiles: %v", err)
	}
	graph, report, err := Build(tree)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := session.Validate(graph); err != nil {
		t.Fatalf("the mapped tree is not storable: %v", err)
	}
	return graph, report
}

func TestFixtureGolden(t *testing.T) {
	graph, report := buildFixture(t)
	goldentest.Golden(t, filepath.Join("testdata", "golden", "fixture_graph.json"), graph)
	goldentest.Golden(t, filepath.Join("testdata", "golden", "fixture_report.json"), report)
}

// Every line has to land somewhere. This is the check that catches a mapping
// change which quietly stops storing a kind of line.
func TestReportAccountsForEveryLine(t *testing.T) {
	_, report := buildFixture(t)
	if !report.Lines.Balanced() {
		t.Fatalf("%d lines read but %d accounted for: %+v",
			report.Lines.Total, report.Lines.Accounted(), report.Lines)
	}
}

func findInput(t *testing.T, graph *session.Graph, text string) session.UserInput {
	t.Helper()
	for _, input := range graph.UserInputs {
		if input.Text == text {
			return input
		}
	}
	t.Fatalf("no user input with text %q", text)
	return session.UserInput{}
}

func sessionOf(t *testing.T, graph *session.Graph, id string) session.Session {
	t.Helper()
	for _, s := range graph.Sessions {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("no session %s", id)
	return session.Session{}
}

func TestNestedSessionsAreBuiltToFullDepth(t *testing.T) {
	graph, _ := buildFixture(t)

	if len(graph.Sessions) != 3 {
		t.Fatalf("built %d sessions, want the main thread and two subagents", len(graph.Sessions))
	}
	root := graph.Root()
	if root == nil {
		t.Fatal("no top-level session")
	}

	// The tree is main → a1 → a2, and the depth-2 transcript sits flat in the
	// same directory as a1 with the root session's id: only the sidecar says it
	// belongs to a1.
	var depths []int
	for _, s := range graph.Sessions {
		depth := 0
		for at := s; at.ParentSessionID != nil; {
			depth++
			at = sessionOf(t, graph, *at.ParentSessionID)
		}
		depths = append(depths, depth)
	}
	want := []int{0, 1, 2}
	for i, got := range depths {
		if got != want[i] {
			t.Fatalf("session depths = %v, want %v", depths, want)
		}
	}
}

// A subagent's root line carries its parent's prompt id. Sharing one index across
// transcripts would file every subagent row under the parent's ask.
func TestSubagentWorkStaysInItsOwnSession(t *testing.T) {
	graph, _ := buildFixture(t)

	inputSession := map[string]string{}
	for _, input := range graph.UserInputs {
		inputSession[input.ID] = input.SessionID
	}
	for _, invocation := range graph.ModelInvocations {
		if inputSession[invocation.UserInputID] != invocation.SessionID {
			t.Errorf("model invocation in session %s is attributed to an ask in %s",
				invocation.SessionID, inputSession[invocation.UserInputID])
		}
	}
	for _, use := range graph.ToolUses {
		if inputSession[use.UserInputID] != use.SessionID {
			t.Errorf("tool use %s in session %s is attributed to an ask in %s",
				use.Name, use.SessionID, inputSession[use.UserInputID])
		}
	}
}

// A slash command emits its own echo and its local output before the prompt the
// person typed, all under one prompt id. First-carrier-wins would attribute the
// whole turn to the echo.
func TestASlashCommandDoesNotStealItsTurn(t *testing.T) {
	graph, _ := buildFixture(t)

	typed := findInput(t, graph, "Now commit it")
	if typed.Kind != session.KindPrompt {
		t.Fatalf("the typed prompt has kind %q, want %q", typed.Kind, session.KindPrompt)
	}

	var commit *session.ToolUse
	for i := range graph.ToolUses {
		if graph.ToolUses[i].Name == "Bash" && graph.ToolUses[i].Verb == session.VerbExecute {
			if commit == nil {
				commit = &graph.ToolUses[i]
			}
		}
	}
	if commit == nil {
		t.Fatal("no command was recorded")
	}
	if commit.UserInputID != typed.ID {
		t.Fatalf("the commit is attributed to %s, want the typed prompt %s", commit.UserInputID, typed.ID)
	}
}

func TestInputKinds(t *testing.T) {
	graph, _ := buildFixture(t)

	kinds := map[session.InputKind]int{}
	for _, input := range graph.UserInputs {
		kinds[input.Kind]++
	}
	// Two prompts and one subagent ask per subagent transcript, one slash command,
	// one interrupt, and two `other`: the slash command's local output and the
	// notification the harness injected.
	want := map[session.InputKind]int{
		session.KindPrompt:       4,
		session.KindSlashCommand: 1,
		session.KindInterrupt:    1,
		session.KindOther:        2,
	}
	for kind, count := range want {
		if kinds[kind] != count {
			t.Errorf("%s inputs = %d, want %d", kind, kinds[kind], count)
		}
	}
}

func TestNestedPromptsComeFromTheParentAgent(t *testing.T) {
	graph, _ := buildFixture(t)
	count := 0
	for _, input := range graph.UserInputs {
		s := sessionOf(t, graph, input.SessionID)
		if s.ParentSessionID != nil && input.Kind == session.KindPrompt {
			count++
			if input.Origin != session.OriginAgent {
				t.Errorf("nested prompt %q has origin %q", input.Text, input.Origin)
			}
		}
	}
	if count != 2 {
		t.Fatalf("nested prompts = %d, want 2", count)
	}
}

// A completion the harness feeds back in is not something a person asked for.
func TestAnInjectedNotificationIsNotAPrompt(t *testing.T) {
	graph, _ := buildFixture(t)
	for _, input := range graph.UserInputs {
		if input.Kind == session.KindPrompt && len(input.Text) > 20 && input.Text[:20] == "<task-notification>\n" {
			t.Fatalf("a task notification was stored as a prompt: %q", input.Text[:40])
		}
	}
}

func TestInterruptMarksTheAskItCut(t *testing.T) {
	graph, _ := buildFixture(t)

	typed := findInput(t, graph, "Now commit it")
	if !typed.Interrupted {
		t.Error("the ask the interrupt cut short is not marked")
	}
	first := findInput(t, graph, "Audit the parser")
	if first.Interrupted {
		t.Error("an unrelated ask was marked as interrupted")
	}
}

func TestToolStatuses(t *testing.T) {
	graph, _ := buildFixture(t)

	byName := map[string][]session.ToolUse{}
	for _, use := range graph.ToolUses {
		byName[use.Name] = append(byName[use.Name], use)
	}

	// A refused spawn is an interrupt, not a failure: the tool never ran because
	// a person said no. The signal the mapping document names for this —
	// toolUseResult.interrupted — is never true in any real transcript.
	var denied *session.ToolUse
	for i, use := range byName["Agent"] {
		if use.Status == session.StatusInterrupted {
			denied = &byName["Agent"][i]
		}
	}
	if denied == nil {
		t.Error("the refused spawn was not recorded as interrupted")
	} else if denied.Verb != session.VerbOther {
		t.Errorf("the unlinked refused Agent verb = %q, want other", denied.Verb)
	}

	// A call the transcript never answers has no result at all.
	var open *session.ToolUse
	for i, use := range byName["Grep"] {
		open = &byName["Grep"][i]
		_ = use
	}
	if open == nil {
		t.Fatal("the unanswered call is missing")
	}
	if open.Status != session.StatusNoResult {
		t.Errorf("the unanswered call has status %q, want %q", open.Status, session.StatusNoResult)
	}
	for _, result := range graph.ToolResults {
		if result.ToolUseID == open.ID {
			t.Error("a result was invented for a call the transcript never answered")
		}
	}
}

func TestSpawnVerbRequiresALinkedTranscript(t *testing.T) {
	for _, name := range []string{"Agent", "Task"} {
		if got := verbFor(name, false); got != session.VerbOther {
			t.Errorf("unlinked %s verb = %q, want other", name, got)
		}
	}
	if got := verbFor("renamed-spawn-tool", true); got != session.VerbSpawnSubagent {
		t.Errorf("linked renamed tool verb = %q, want spawn_subagent", got)
	}
}

// The outcome of a background command arrives on a control line long after its
// tool result said it had been handed off.
func TestBackgroundCompletionIsMergedIntoItsCall(t *testing.T) {
	graph, report := buildFixture(t)

	var background *session.ToolUse
	for i := range graph.ToolUses {
		if graph.ToolUses[i].Name == "Bash" && contains(string(graph.ToolUses[i].Input), "run_in_background") {
			background = &graph.ToolUses[i]
		}
	}
	if background == nil {
		t.Fatal("the backgrounded command is missing")
	}
	if background.Status != session.StatusOK {
		t.Errorf("the backgrounded command has status %q, want %q after its completion",
			background.Status, session.StatusOK)
	}
	if !contains(string(background.Metadata), "bg7x2") {
		t.Errorf("the task id was not kept on the call: %s", background.Metadata)
	}

	var merged *session.ToolResult
	for i := range graph.ToolResults {
		if graph.ToolResults[i].ToolUseID == background.ID {
			merged = &graph.ToolResults[i]
		}
	}
	if merged == nil {
		t.Fatal("the backgrounded command has no result")
	}
	if merged.OutputText == nil || !contains(*merged.OutputText, "completed") {
		t.Errorf("the completion summary was not stored: %v", merged.OutputText)
	}
	// The interim handoff is kept beside the completion: the record should still
	// show that the command was backgrounded.
	if !contains(string(merged.Output), "moved to the background") {
		t.Errorf("the background handoff was overwritten: %s", merged.Output)
	}
	if report.Lines.Merged != 1 {
		t.Errorf("merged %d lines, want 1", report.Lines.Merged)
	}
}

func TestSessionFields(t *testing.T) {
	graph, _ := buildFixture(t)
	root := graph.Root()

	if root.Title == nil || *root.Title != "Audit the transcript parser" {
		t.Errorf("title = %v, want the ai-title control value", root.Title)
	}
	if root.WorkingDir == nil || *root.WorkingDir != "/repo" {
		t.Errorf("working dir = %v", root.WorkingDir)
	}
	if root.GitBranch == nil || *root.GitBranch != "main" {
		t.Errorf("branch = %v", root.GitBranch)
	}
	if root.StartedAt == nil || root.EndedAt == nil || !root.EndedAt.After(*root.StartedAt) {
		t.Errorf("session ran from %v to %v", root.StartedAt, root.EndedAt)
	}
	// A successful `git commit` naming a hash outranks the interrupt that follows.
	if root.Outcome != session.OutcomeCommitted {
		t.Errorf("outcome = %q, want %q", root.Outcome, session.OutcomeCommitted)
	}
}

func TestAttributeLog(t *testing.T) {
	graph, _ := buildFixture(t)

	byKey := map[string][]session.SessionAttribute{}
	for _, attribute := range graph.Attributes {
		byKey[attribute.Key] = append(byKey[attribute.Key], attribute)
	}
	for _, key := range []string{"mode", "permissionMode", "aiTitle", "leafUuid", "bridgeSessionId"} {
		if len(byKey[key]) == 0 {
			t.Errorf("no attribute row for %q", key)
		}
	}

	// The two control lines that open the transcript come before anything
	// timestamped, so their effective time can only be the session's start.
	root := graph.Root()
	for _, attribute := range byKey["permissionMode"] {
		if attribute.ValidFrom.Before(*root.StartedAt) {
			t.Errorf("permissionMode is effective at %v, before the session started at %v",
				attribute.ValidFrom, *root.StartedAt)
		}
	}
}

func TestAmbientHistoryBookkeepingAndQueuedPrompts(t *testing.T) {
	graph, report := buildClaudeLines(t,
		`{"type":"agent-name","agentName":"mapper-hardening","sessionId":"11111111-2222-4333-8444-555555555555"}`,
		`{"parentUuid":null,"isSidechain":false,"cwd":"/repo","gitBranch":"main","type":"user","uuid":"user-1","sessionId":"11111111-2222-4333-8444-555555555555","timestamp":"2026-08-06T10:00:00Z","promptId":"prompt-1","message":{"role":"user","content":"Start"}}`,
		`{"type":"relocated","sessionId":"11111111-2222-4333-8444-555555555555","relocatedCwd":"/repo/.claude/worktrees/hardening"}`,
		`{"type":"worktree-state","worktreeSession":{"worktreePath":"/repo/.claude/worktrees/hardening","worktreeBranch":"hardening"},"sessionId":"11111111-2222-4333-8444-555555555555"}`,
		`{"type":"pr-link","sessionId":"11111111-2222-4333-8444-555555555555","prNumber":42,"prUrl":"https://example.test/pull/42","prRepository":"example/repo","timestamp":"2026-08-06T10:00:01Z"}`,
		`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:02Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Queued instruction"}`,
		`{"parentUuid":"user-1","isSidechain":false,"attachment":{"type":"queued_command","prompt":"Queued instruction","commandMode":"prompt","origin":{"kind":"human"},"timestamp":"2026-08-06T10:00:02Z"},"type":"attachment","uuid":"queued-1","timestamp":"2026-08-06T10:00:02Z","cwd":"/repo/worktree","gitBranch":"feature","sessionId":"11111111-2222-4333-8444-555555555555"}`,
		`{"type":"queue-operation","operation":"remove","timestamp":"2026-08-06T10:00:03Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Queued instruction"}`,
		`{"parentUuid":"queued-1","isSidechain":false,"cwd":"/repo/worktree","gitBranch":"feature","type":"assistant","uuid":"assistant-1","sessionId":"11111111-2222-4333-8444-555555555555","timestamp":"2026-08-06T10:00:04Z","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"On it"}]}}`,
		`{"parentUuid":"assistant-1","isSidechain":false,"cwd":"/repo/worktree","gitBranch":"feature","type":"user","uuid":"user-2","sessionId":"11111111-2222-4333-8444-555555555555","timestamp":"2026-08-06T10:00:05Z","promptId":"prompt-2","message":{"role":"user","content":"Queued instruction"}}`,
	)
	if !report.Lines.Balanced() {
		t.Fatalf("report = %+v", report.Lines)
	}
	if root := graph.Root(); root.StartedAt == nil || root.EndedAt == nil || root.StartedAt.Format(time.RFC3339) != "2026-08-06T10:00:00Z" || root.EndedAt.Format(time.RFC3339) != "2026-08-06T10:00:05Z" {
		t.Fatalf("envelope time range = %v to %v", root.StartedAt, root.EndedAt)
	}
	if len(graph.UserInputs) != 2 {
		t.Fatalf("inputs = %d, want initial plus one deduplicated queued prompt", len(graph.UserInputs))
	}
	queued := findInput(t, graph, "Queued instruction")
	if !strings.Contains(string(queued.Metadata), `"queued":true`) ||
		!strings.Contains(string(queued.Metadata), `"operation":"enqueue"`) ||
		!strings.Contains(string(queued.Metadata), `"operation":"remove"`) {
		t.Fatalf("queued metadata = %s", queued.Metadata)
	}
	if len(graph.AssistantMessages) != 1 || graph.AssistantMessages[0].UserInputID != queued.ID {
		t.Fatalf("assistant was not attributed to queued input: %+v", graph.AssistantMessages)
	}

	byKey := map[string][]session.SessionAttribute{}
	for _, attribute := range graph.Attributes {
		byKey[attribute.Key] = append(byKey[attribute.Key], attribute)
	}
	for _, key := range []string{"agent-name", "relocated", "worktree-state", "pr-link"} {
		if len(byKey[key]) != 1 {
			t.Errorf("%s attributes = %d, want 1", key, len(byKey[key]))
		}
	}
	if len(byKey["cwd"]) != 2 || len(byKey["gitBranch"]) != 2 {
		t.Fatalf("cwd/gitBranch history = %d/%d, want 2/2", len(byKey["cwd"]), len(byKey["gitBranch"]))
	}
	if !strings.Contains(string(byKey["pr-link"][0].Value), `"prNumber":42`) {
		t.Fatalf("pr-link value = %s", byKey["pr-link"][0].Value)
	}
}

// TestQueuedPromptCarrierShapes covers the carrier orderings the corpus actually
// contains, one subtest each. At the audited commit the 28 queue identities are
// `enqueue`+`remove`+attachment (24), `enqueue`+`popAll` (2), and
// `enqueue`+ordinary row (2); the single-carrier cases below are the contract
// boundary, where a transcript ends before delivery.
func TestQueuedPromptCarrierShapes(t *testing.T) {
	t.Run("enqueue only", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Queued only"}`,
		)
		if !report.Lines.Balanced() || len(graph.UserInputs) != 1 || graph.UserInputs[0].Text != "Queued only" {
			t.Fatalf("graph/report = %+v / %+v", graph.UserInputs, report.Lines)
		}
	})

	t.Run("attachment only", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"parentUuid":null,"isSidechain":false,"attachment":{"type":"queued_command","prompt":"Attachment only","commandMode":"prompt","origin":{"kind":"human"},"timestamp":"2026-08-06T10:00:00Z"},"type":"attachment","uuid":"queued-only","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555"}`,
		)
		if !report.Lines.Balanced() || len(graph.UserInputs) != 1 || graph.UserInputs[0].Text != "Attachment only" {
			t.Fatalf("graph/report = %+v / %+v", graph.UserInputs, report.Lines)
		}
	})

	// The dominant live shape: 24 of the 28 audited identities.
	t.Run("enqueue then remove then attachment", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Ship it"}`,
			`{"type":"queue-operation","operation":"remove","timestamp":"2026-08-06T10:00:01Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Ship it"}`,
			`{"parentUuid":null,"isSidechain":false,"attachment":{"type":"queued_command","prompt":"Ship it","commandMode":"prompt","origin":{"kind":"human"},"timestamp":"2026-08-06T10:00:00Z"},"type":"attachment","uuid":"attachment-1","timestamp":"2026-08-06T10:00:02Z","sessionId":"11111111-2222-4333-8444-555555555555"}`,
			`{"parentUuid":"attachment-1","isSidechain":false,"type":"assistant","uuid":"assistant-1","sessionId":"11111111-2222-4333-8444-555555555555","timestamp":"2026-08-06T10:00:03Z","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"On it"}]}}`,
		)
		if !report.Lines.Balanced() {
			t.Fatalf("report = %+v", report.Lines)
		}
		if len(graph.UserInputs) != 1 {
			t.Fatalf("inputs = %d, want the three carriers to collapse to one", len(graph.UserInputs))
		}
		queued := graph.UserInputs[0]
		if queued.Text != "Ship it" || queued.Kind != session.KindPrompt || queued.Origin != session.OriginHuman {
			t.Fatalf("queued input = %+v", queued)
		}
		// The attachment is provenance for the same input, not a second one.
		if !hasVendorID(graph, queued.ID, "uuid", "attachment-1") {
			t.Errorf("attachment uuid is not recorded on the queued input: %+v", graph.VendorIDs)
		}
		for _, operation := range []string{`"operation":"enqueue"`, `"operation":"remove"`} {
			if !strings.Contains(string(queued.Metadata), operation) {
				t.Errorf("metadata %s does not record %s", queued.Metadata, operation)
			}
		}
		if len(graph.AssistantMessages) != 1 || graph.AssistantMessages[0].UserInputID != queued.ID {
			t.Errorf("the reply after delivery is not owned by the queued input: %+v", graph.AssistantMessages)
		}
	})

	// 2 of the 28: nothing but queue operations ever names the prompt.
	t.Run("enqueue then popAll", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Drain me"}`,
			`{"type":"queue-operation","operation":"popAll","timestamp":"2026-08-06T10:00:01Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Drain me"}`,
			`{"parentUuid":null,"isSidechain":false,"type":"assistant","uuid":"assistant-1","sessionId":"11111111-2222-4333-8444-555555555555","timestamp":"2026-08-06T10:00:02Z","message":{"id":"message-1","role":"assistant","content":[{"type":"text","text":"Draining"}]}}`,
		)
		if !report.Lines.Balanced() || len(graph.UserInputs) != 1 {
			t.Fatalf("graph/report = %+v / %+v", graph.UserInputs, report.Lines)
		}
		queued := graph.UserInputs[0]
		if queued.Text != "Drain me" {
			t.Fatalf("queued input = %+v", queued)
		}
		if !strings.Contains(string(queued.Metadata), `"operation":"popAll"`) {
			t.Errorf("metadata %s does not record popAll", queued.Metadata)
		}
		if len(graph.AssistantMessages) != 1 || graph.AssistantMessages[0].UserInputID != queued.ID {
			t.Errorf("popAll did not activate the queued input: %+v", graph.AssistantMessages)
		}
	})

	// 2 of the 28: the later ordinary row is evidence merged into the queued
	// input, never a replacement for it.
	t.Run("enqueue then ordinary prompt", func(t *testing.T) {
		const enqueue = `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Rebase it"}`
		alone, _ := buildClaudeLines(t, enqueue)
		graph, report := buildClaudeLines(t, enqueue,
			`{"parentUuid":null,"isSidechain":false,"type":"user","uuid":"user-1","sessionId":"11111111-2222-4333-8444-555555555555","timestamp":"2026-08-06T10:00:05Z","promptId":"prompt-1","message":{"role":"user","content":"Rebase it"}}`,
		)
		if !report.Lines.Balanced() || len(graph.UserInputs) != 1 {
			t.Fatalf("graph/report = %+v / %+v", graph.UserInputs, report.Lines)
		}
		queued := graph.UserInputs[0]
		// The append-only transcript grew an ordinary carrier; the id must not move.
		if queued.ID != alone.UserInputs[0].ID {
			t.Errorf("queue-derived id changed from %s to %s when the ordinary carrier arrived",
				alone.UserInputs[0].ID, queued.ID)
		}
		if queued.TriggeredAt == nil || !queued.TriggeredAt.Equal(*alone.UserInputs[0].TriggeredAt) {
			t.Errorf("triggered_at = %v, want the queue timestamp %v", queued.TriggeredAt, alone.UserInputs[0].TriggeredAt)
		}
		if !hasVendorID(graph, queued.ID, "uuid", "user-1") {
			t.Errorf("the ordinary row's uuid was not merged in: %+v", graph.VendorIDs)
		}
		if !strings.Contains(string(queued.Metadata), `"promptId":"prompt-1"`) {
			t.Errorf("metadata %s does not carry the ordinary row's prompt id", queued.Metadata)
		}
	})

	// Claude publishes no queue uuid or operation id, so identity is the queue
	// timestamp plus the exact text.
	t.Run("repeated text stays distinct by timestamp", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"again"}`,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:01Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"again"}`,
		)
		if !report.Lines.Balanced() || len(graph.UserInputs) != 2 {
			t.Fatalf("inputs = %+v, want two distinct queue identities", graph.UserInputs)
		}
		if graph.UserInputs[0].ID == graph.UserInputs[1].ID {
			t.Fatal("two queue identities minted the same id")
		}
	})

	// With no timestamp there is nothing left to tell two identical texts apart,
	// so source order is the fallback key. It is a fallback, not an identity:
	// repeated text without timestamps is inherently ambiguous.
	t.Run("repeated text without timestamps falls back to source order", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"type":"queue-operation","operation":"enqueue","sessionId":"11111111-2222-4333-8444-555555555555","content":"again"}`,
			`{"type":"queue-operation","operation":"enqueue","sessionId":"11111111-2222-4333-8444-555555555555","content":"again"}`,
		)
		if !report.Lines.Balanced() || len(graph.UserInputs) != 2 {
			t.Fatalf("inputs = %+v, want one per source line", graph.UserInputs)
		}
		if graph.UserInputs[0].ID == graph.UserInputs[1].ID {
			t.Fatal("the source-order fallback did not separate two identical texts")
		}
	})

	// §4.7 rule 4: a dequeue neither creates an input nor activates one.
	t.Run("dequeue is discarded", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Never asked"}`,
		)
		if !report.Lines.Balanced() {
			t.Fatalf("report = %+v", report.Lines)
		}
		if len(graph.UserInputs) != 0 {
			t.Fatalf("inputs = %+v, want none", graph.UserInputs)
		}
	})

	t.Run("dequeue does not activate an enqueued input", func(t *testing.T) {
		graph, report := buildClaudeLines(t,
			`{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-06T10:00:00Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Cancelled"}`,
			`{"type":"queue-operation","operation":"dequeue","timestamp":"2026-08-06T10:00:01Z","sessionId":"11111111-2222-4333-8444-555555555555","content":"Cancelled"}`,
		)
		if !report.Lines.Balanced() || len(graph.UserInputs) != 1 {
			t.Fatalf("graph/report = %+v / %+v", graph.UserInputs, report.Lines)
		}
		if strings.Contains(string(graph.UserInputs[0].Metadata), `"operation":"dequeue"`) {
			t.Errorf("metadata %s records a discarded dequeue", graph.UserInputs[0].Metadata)
		}
	})
}

func hasVendorID(graph *session.Graph, ourID, kind, value string) bool {
	for _, vendorID := range graph.VendorIDs {
		if vendorID.OurID == ourID && vendorID.VendorIDKind == kind && vendorID.VendorID == value {
			return true
		}
	}
	return false
}

func TestFileTouches(t *testing.T) {
	graph, _ := buildFixture(t)

	byPath := map[string]session.FileTouch{}
	for _, touch := range graph.FileTouches {
		byPath[touch.Path] = touch
	}
	read, ok := byPath["/repo/parse.go"]
	if !ok {
		t.Fatal("the file the session read was not recorded")
	}
	if read.Kind != session.TouchRead {
		t.Errorf("kind = %q, want %q", read.Kind, session.TouchRead)
	}
	// The span is refined from the result, which reports what the tool actually
	// returned rather than what was asked for.
	if read.StartLine == nil || *read.StartLine != 1 || read.EndLine == nil || *read.EndLine != 12 {
		t.Errorf("span = %v..%v, want 1..12 from the result", read.StartLine, read.EndLine)
	}
	if _, recorded := byPath["/repo/migrations/000001.sql"]; !recorded {
		t.Error("the file the nested subagent read was not recorded")
	}
}

func TestTokensComeFromOneLinePerReply(t *testing.T) {
	graph, _ := buildFixture(t)

	// The fixture's first reply spans three lines whose output_tokens differ, the
	// last being 41. Summing them would report 57.
	var first *session.ModelInvocation
	for i := range graph.ModelInvocations {
		if graph.ModelInvocations[i].ParentModelInvocationID == nil &&
			graph.ModelInvocations[i].SessionID == graph.Root().ID {
			first = &graph.ModelInvocations[i]
			break
		}
	}
	if first == nil {
		t.Fatal("no first reply")
	}
	if first.OutputTokens != 41 {
		t.Errorf("output tokens = %d, want 41 from the reply's last line", first.OutputTokens)
	}
	if want := first.InputTokens + first.OutputTokens + first.CacheReadTokens + first.CacheWriteTokens; first.TotalTokens != want {
		t.Errorf("total = %d, want %d", first.TotalTokens, want)
	}
}

func TestRepliesChainWithinOneAsk(t *testing.T) {
	graph, _ := buildFixture(t)

	byInput := map[string][]session.ModelInvocation{}
	for _, invocation := range graph.ModelInvocations {
		byInput[invocation.UserInputID] = append(byInput[invocation.UserInputID], invocation)
	}
	for input, chain := range byInput {
		for i, invocation := range chain {
			if i == 0 {
				if invocation.ParentModelInvocationID != nil {
					t.Errorf("the first reply under ask %s has a parent", input)
				}
				continue
			}
			if invocation.ParentModelInvocationID == nil {
				t.Errorf("reply %d under ask %s has no parent", i, input)
				continue
			}
			if *invocation.ParentModelInvocationID != chain[i-1].ID {
				t.Errorf("reply %d under ask %s is chained to the wrong parent", i, input)
			}
		}
	}
}

func TestVendorMappingPointsBackAtTheTranscript(t *testing.T) {
	graph, _ := buildFixture(t)

	kinds := map[string]int{}
	for _, mapping := range graph.VendorIDs {
		if mapping.Vendor != Vendor {
			t.Errorf("vendor = %q, want %q", mapping.Vendor, Vendor)
		}
		if mapping.SourceRef == nil || *mapping.SourceRef == "" {
			t.Errorf("mapping for %s %s has no source reference", mapping.OurType, mapping.OurID)
		}
		kinds[mapping.VendorIDKind]++
	}
	for _, kind := range []string{
		"sessionId", "agentId", "promptId", "uuid", "uuid.block",
		"message.id", "tool_use.id", "tool_use_id",
	} {
		if kinds[kind] == 0 {
			t.Errorf("nothing is mapped by %q", kind)
		}
	}
}

// One line can carry several blocks of one kind, and every block is its own row.
// The line's id names them all at once, so the mapping carries the block's
// position as well: without it two rows claim one vendor id, which §6 rule 2
// forbids and Validate rejects — taking the whole session with it.
func TestBlocksOfOneLineAreMappedApart(t *testing.T) {
	graph, _ := buildFixture(t)

	// In the fixture u22 carries thinking and text, and u23 two tool calls.
	for _, want := range []struct {
		what      session.EntityType
		vendorIDs []string
	}{
		{session.EntityAssistantMessage, []string{"u22:0", "u22:1"}},
		{session.EntityToolUse, []string{"u23:0", "u23:1"}},
	} {
		mapped := map[string]string{}
		for _, mapping := range graph.VendorIDs {
			if mapping.OurType == want.what && mapping.VendorIDKind == "uuid.block" {
				mapped[mapping.VendorID] = mapping.OurID
			}
		}
		claimed := map[string]string{}
		for _, vendorID := range want.vendorIDs {
			ourID, found := mapped[vendorID]
			if !found {
				t.Errorf("no %s is mapped from %s", want.what, vendorID)
				continue
			}
			if other, taken := claimed[ourID]; taken {
				t.Errorf("%s %s is mapped from both %s and %s", want.what, ourID, other, vendorID)
			}
			claimed[ourID] = vendorID
		}
	}
}

// A result is one per call rather than one per block, so the call's id names it
// exactly and the line does not name it at all: one line can answer several
// calls, and a result merged from a later line no longer comes from the line it
// was first seen on.
func TestAResultIsMappedByItsCallAlone(t *testing.T) {
	graph, _ := buildFixture(t)

	for _, mapping := range graph.VendorIDs {
		if mapping.OurType == session.EntityToolResult && mapping.VendorIDKind != "tool_use_id" {
			t.Errorf("a result is mapped by %q, want the call's id alone", mapping.VendorIDKind)
		}
	}

	// Both calls the fixture answers on one line keep their own result.
	name := map[string]string{}
	for _, use := range graph.ToolUses {
		name[use.ID] = use.Name
	}
	answered := map[string]bool{}
	for _, result := range graph.ToolResults {
		answered[name[result.ToolUseID]] = true
	}
	for _, tool := range []string{"Glob", "WebFetch"} {
		if !answered[tool] {
			t.Errorf("the %s call answered on the shared line has no result", tool)
		}
	}
}

// Thinking is most of what a reply contains, and dropping it would lose a third
// of the model's output.
func TestThinkingIsStored(t *testing.T) {
	graph, _ := buildFixture(t)
	for _, message := range graph.AssistantMessages {
		if contains(string(message.Content), `"type":"thinking"`) {
			return
		}
	}
	t.Fatal("no thinking block was stored")
}

func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
