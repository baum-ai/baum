package viewserver

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/baum-ai/baum/internal/locallink"
	"github.com/baum-ai/baum/internal/transcript"
	"github.com/baum-ai/baum/sessions/session"
)

const (
	testHome    = "/home/user"
	plantedMail = "ada.lovelace@example.com"
	plantedDSN  = "postgres://app:Tr0ub4dor-3xkq@db.internal:5432/app"
)

func ptr[T any](v T) *T { return &v }

// transcriptGraph is a linked session whose prompt names an e-mail address
// and whose tool call reads a file under the home directory and prints a
// connection string with its password.
func transcriptGraph(prompt string) *session.Graph {
	return &session.Graph{
		Sessions: []session.Session{{
			ID: "root", Title: ptr("Fix the parser"), Outcome: session.OutcomeCommitted,
			WorkingDir: ptr(testHome + "/src/widgets"), RepoRoot: ptr(testHome + "/src/widgets"),
		}},
		UserInputs: []session.UserInput{{
			ID: "p1", SessionID: "root", Ordinal: 1, Text: prompt, Kind: session.KindPrompt, Origin: session.OriginHuman,
		}},
		ToolUses: []session.ToolUse{{
			ID: "read", SessionID: "root", UserInputID: "p1", Name: "Read", Verb: session.VerbRead, Status: session.StatusOK,
			Input: json.RawMessage(`{"file_path":"` + testHome + `/src/widgets/.env"}`),
		}},
		ToolResults: []session.ToolResult{{
			ID: "r1", ToolUseID: "read", SessionID: "root", UserInputID: "p1", Status: session.StatusOK,
			OutputText: ptr("DATABASE_URL=" + plantedDSN + "\nlogs in " + testHome + "name stay"),
		}},
		VendorIDs: []session.VendorID{{OurID: "root", OurType: session.EntitySession, SessionID: "root", Vendor: "claude_code",
			VendorIDKind: "session", VendorID: "root", SourceRef: ptr(testHome + "/.claude/projects/x.jsonl")}},
	}
}

// transcriptLinker answers with session "s-1" linked to the head commit and
// session "s-2" read but not linked.
func transcriptLinker(graph *session.Graph) SessionsFunc {
	return func(_ context.Context, review Review) (locallink.Result, error) {
		return locallink.Result{
			Available: true,
			Sessions:  []locallink.Session{{ID: "s-1", Source: "claude_code", Vendor: "claude_code", Branch: "fix/parser", Outcome: "committed"}},
			Commits: []locallink.Commit{{OID: review.Head, Subject: "Fix", Links: []locallink.Link{
				{Session: "s-1", Method: "commit_ref", Confidence: 0.6},
			}}},
			Graphs: map[string]*session.Graph{"s-1": graph, "s-2": graph},
		}, nil
	}
}

func TestTranscriptServesALinkedSessionRedacted(t *testing.T) {
	graph := transcriptGraph("Mail " + plantedMail + " when the parser is fixed")
	graph.AssistantMessages = []session.AssistantMessage{{ID: "a1", SessionID: "root", UserInputID: "p1",
		Text: ptr("Mailed " + plantedMail + " from " + testHome + "/src/widgets")}}
	_, base := start(t, Options{
		Document: []byte(reviewDocument(headA, "one")), Home: testHome, Sessions: transcriptLinker(graph),
	})
	resp, body := get(t, base+"/api/sessions/s-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if resp.Header.Get("Content-Type") != "application/json" || resp.Header.Get("Cache-Control") != "no-store" ||
		resp.Header.Get("X-Content-Type-Options") != "nosniff" || !strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("headers = %v", resp.Header)
	}
	for _, leaked := range []string{plantedMail, "Tr0ub4dor-3xkq", testHome + "/", "/.claude/projects", "repo_root"} {
		if strings.Contains(body, leaked) {
			t.Errorf("the body holds %q: %s", leaked, body)
		}
	}
	var got struct {
		Session struct {
			RunID       string  `json:"run_id"`
			WorkingDir  string  `json:"working_dir"`
			CommitCount int     `json:"commit_count"`
			Confidence  float64 `json:"confidence"`
			Prompts     []struct {
				Text    string `json:"text"`
				Replies []struct {
					Text string `json:"text"`
				} `json:"replies"`
			} `json:"prompts"`
		} `json:"session"`
		Activity map[string]struct {
			Messages []struct {
				Text string `json:"text"`
			} `json:"messages"`
			ToolCalls []struct {
				Input  string `json:"input"`
				Output string `json:"output"`
			} `json:"tool_calls"`
		} `json:"activity"`
		Truncated bool `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	s := got.Session
	if s.RunID != "s-1" || s.WorkingDir != "~/src/widgets" || s.CommitCount != 1 || s.Confidence != 0.6 || got.Truncated {
		t.Errorf("session = %+v", s)
	}
	if len(s.Prompts) != 1 || !strings.HasPrefix(s.Prompts[0].Text, "Mail xxx.xxxxxxxx@xxxxxxx.xxx when") {
		t.Errorf("prompts = %+v", s.Prompts)
	}
	// The reply and the activity's message are both masked, with the home
	// directory as ~.
	const mailed = "Mailed xxx.xxxxxxxx@xxxxxxx.xxx from ~/src/widgets"
	if replies, messages := s.Prompts[0].Replies, got.Activity["p1"].Messages; len(replies) != 1 || replies[0].Text != mailed ||
		len(messages) != 1 || messages[0].Text != mailed {
		t.Errorf("replies = %+v, messages %+v", replies, messages)
	}
	calls := got.Activity["p1"].ToolCalls
	if len(calls) != 1 || calls[0].Input != `{"file_path":"~/src/widgets/.env"}` ||
		!strings.Contains(calls[0].Output, "logs in "+testHome+"name stay") {
		t.Errorf("tool calls = %+v", calls)
	}

	head, err := http.Head(base + "/api/sessions/s-1")
	if err != nil {
		t.Fatal(err)
	}
	_ = head.Body.Close()
	if head.StatusCode != http.StatusOK || head.Header.Get("Content-Length") != resp.Header.Get("Content-Length") {
		t.Errorf("HEAD: status %d, length %q", head.StatusCode, head.Header.Get("Content-Length"))
	}
}

// A tool's output holding an env file is masked line by line, as Redact
// masks the text itself, though each line follows an escaped newline.
func TestTranscriptMasksAnEnvFileInAToolsOutput(t *testing.T) {
	graph := transcriptGraph("Read the env file")
	env := "PASSWORD=Sup3r$ecret!Pass\nPASSWORD=An0ther$ecret!Pass\nPASSWORD=Th1rd$ecret!Pass\n" +
		"Authorization: Basic YWxhZGRpbjpvcGVuc2VzYW1l\nAuthorization: Basic Zm9vYmFyOmJhenF1dXg9\n"
	graph.ToolResults[0].OutputText = &env
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one")), Home: testHome, Sessions: transcriptLinker(graph)})
	resp, body := get(t, base+"/api/sessions/s-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	for _, secret := range []string{"Sup3r$ecret", "An0ther$ecret", "Th1rd$ecret", "YWxhZGRpbjpvcGVuc2VzYW1l", "Zm9vYmFyOmJhenF1dXg9"} {
		if strings.Contains(body, secret) {
			t.Errorf("%q survived: %s", secret, body)
		}
	}
	if !json.Valid([]byte(body)) {
		t.Error("the body is not JSON")
	}
}

// fetchTranscriptBody serves graph as session s-1 and returns the body.
func fetchTranscriptBody(t *testing.T, graph *session.Graph) string {
	t.Helper()
	_, base := start(t, Options{Document: []byte(reviewDocument(headA, "one")), Home: testHome, Sessions: transcriptLinker(graph)})
	resp, body := get(t, base+"/api/sessions/s-1")
	if resp.StatusCode != http.StatusOK || !json.Valid([]byte(body)) {
		t.Fatalf("status %d: %.200s", resp.StatusCode, body)
	}
	return body
}

// A connection string the prompt's or a tool output's limit cuts through is
// masked in the part shown: the text is redacted past the cut before it is
// cut, where the whole-body pass would see only a fragment.
func TestTranscriptMasksASecretTheLimitCutsThrough(t *testing.T) {
	// The cut falls inside the password, before the @ that marks the URL's
	// credentials.
	straddle := func(limit int) string {
		head := "postgres://app:Tr0ub4"
		return strings.Repeat("a", limit-len(head)-1) + " " + head + "dor-3xkq@db.internal:5432/app and more"
	}
	graph := transcriptGraph(straddle(transcript.DefaultLimits.Prompt))
	output := straddle(transcript.DefaultLimits.Tool)
	graph.ToolResults[0].OutputText = &output
	body := fetchTranscriptBody(t, graph)
	if n := strings.Count(body, "postgres://"); n != 2 {
		t.Fatalf("the cut texts are not in the body (%d)", n)
	}
	if strings.Contains(body, "Tr0ub4") {
		t.Errorf("the password's start survived the cut: ...%s", body[strings.Index(body, "Tr0ub4")-40:][:80])
	}
}

// Strings no text limit applies to, such as a tool's name and a model name,
// are redacted by the pass over the whole body.
func TestTranscriptMasksTheStringsOutsideTheTexts(t *testing.T) {
	graph := transcriptGraph("Fix the parser")
	graph.ToolUses[0].Name = "mail " + plantedMail
	model := "model of " + plantedMail
	graph.ModelInvocations = []session.ModelInvocation{{ID: "m1", SessionID: "root", UserInputID: "p1", Model: &model, InputTokens: 1}}
	body := fetchTranscriptBody(t, graph)
	if strings.Contains(body, plantedMail) || !strings.Contains(body, "mail xxx.xxxxxxxx@xxxxxxx.xxx") {
		t.Errorf("the address survived: %s", body)
	}
}

func TestTranscriptOfAnUnlinkedSessionIsNotFound(t *testing.T) {
	l := &linker{}
	cases := []struct {
		name string
		opts Options
		path string
	}{
		{"read but not linked", Options{Sessions: transcriptLinker(transcriptGraph("hi"))}, "/api/sessions/s-2"},
		{"unknown", Options{Sessions: transcriptLinker(transcriptGraph("hi"))}, "/api/sessions/nope"},
		{"linked without a graph", Options{Sessions: l.fn}, "/api/sessions/s-a"},
		{"sessions unavailable", Options{}, "/api/sessions/s-1"},
		{"nested path", Options{Sessions: transcriptLinker(transcriptGraph("hi"))}, "/api/sessions/s-1/extra"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			opts := c.opts
			opts.Document = []byte(reviewDocument(headA, "one"))
			_, base := start(t, opts)
			if resp, body := get(t, base+c.path); resp.StatusCode != http.StatusNotFound || strings.Contains(body, "prompts") {
				t.Errorf("status %d: %s", resp.StatusCode, body)
			}
		})
	}
}

func TestTranscriptReportsTruncation(t *testing.T) {
	_, base := start(t, Options{
		Document: []byte(reviewDocument(headA, "one")),
		Sessions: transcriptLinker(transcriptGraph(strings.Repeat("a long prompt ", 5000))),
	})
	resp, body := get(t, base+"/api/sessions/s-1")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var got struct {
		Session struct {
			Prompts []struct {
				Text       string `json:"text"`
				TextLength int    `json:"text_length"`
				Truncated  bool   `json:"truncated"`
			} `json:"prompts"`
		} `json:"session"`
		Truncated  bool `json:"truncated"`
		LimitBytes int  `json:"limit_bytes"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatal(err)
	}
	prompt := got.Session.Prompts[0]
	if !got.Truncated || got.LimitBytes == 0 || !prompt.Truncated || prompt.TextLength < 69990 || len(prompt.Text) >= prompt.TextLength {
		t.Errorf("truncated %v, limit %d, prompt %d of %d (%v)", got.Truncated, got.LimitBytes, len(prompt.Text), prompt.TextLength, prompt.Truncated)
	}
}

func TestTranscriptRouteIsBehindTheHostCheckAndReadOnly(t *testing.T) {
	var calls atomic.Int32
	linker := transcriptLinker(transcriptGraph("hi"))
	_, base := start(t, Options{
		Document: []byte(reviewDocument(headA, "one")),
		Sessions: func(ctx context.Context, review Review) (locallink.Result, error) {
			calls.Add(1)
			return linker(ctx, review)
		},
	})
	req, _ := http.NewRequest(http.MethodGet, base+"/api/sessions/s-1", nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "prompts") || calls.Load() != 0 {
		t.Errorf("foreign Host: status %d, calls %d", resp.StatusCode, calls.Load())
	}
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(method, base+"/api/sessions/s-1", strings.NewReader("{}"))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusMethodNotAllowed || !strings.Contains(resp.Header.Get("Allow"), "GET") {
			t.Errorf("%s: status %d, Allow %q", method, resp.StatusCode, resp.Header.Get("Allow"))
		}
	}
}
