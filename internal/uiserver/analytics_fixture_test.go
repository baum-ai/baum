package uiserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baum-ai/baum/internal/planusage"
	"github.com/baum-ai/baum/sessions/collector/claudecode"
)

// The browser fixture's analytics: two Claude Code sessions in the acme/demo
// clone under the fake home, recent commits of the person's in acme/other (one
// an agent co-authored), a plan usage history and GitHub's answer to the
// authored pull request search. The browser test checks the figures they make.

// writeAnalyticsSessions writes the sessions: one yesterday with two prompts,
// a failed and a passing command and a priced model; one today on a model the
// price list does not know, so the cost estimate is incomplete.
func writeAnalyticsSessions(t *testing.T, home, dir string) {
	t.Helper()
	project := filepath.Join(home, ".claude", "projects", claudecode.EncodeProjectDir(dir))
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	session := func(id string, start time.Time, model string) string {
		at := func(d time.Duration) string { return start.Add(d).UTC().Format("2006-01-02T15:04:05.000Z") }
		line := func(format string, args ...any) string {
			return fmt.Sprintf(`{"cwd":%q,"gitBranch":"feature","sessionId":%q,"version":"2.1.220","isSidechain":false,`, dir, id) + fmt.Sprintf(format, args...) + "}\n"
		}
		usage := `"usage":{"input_tokens":1200,"output_tokens":300,"cache_creation_input_tokens":0,"cache_read_input_tokens":8000}`
		return line(`"type":"user","uuid":"u1","parentUuid":null,"timestamp":%q,"message":{"role":"user","content":"Add the parser tests"},"promptId":"P1","promptSource":"typed","origin":{"kind":"human"}`, at(0)) +
			line(`"type":"assistant","uuid":"u2","parentUuid":"u1","timestamp":%q,"requestId":"req_A","message":{"id":"msg_A","role":"assistant","model":%q,"stop_reason":"tool_use",%s,"content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"go test ./..."}}]}`, at(2*time.Minute), model, usage) +
			line(`"type":"user","uuid":"u3","parentUuid":"u2","timestamp":%q,"message":{"role":"user","content":[{"tool_use_id":"toolu_1","type":"tool_result","content":"FAIL","is_error":true}]},"promptId":"P1","sourceToolAssistantUUID":"u2"`, at(4*time.Minute)) +
			line(`"type":"assistant","uuid":"u4","parentUuid":"u3","timestamp":%q,"requestId":"req_B","message":{"id":"msg_B","role":"assistant","model":%q,"stop_reason":"tool_use",%s,"content":[{"type":"tool_use","id":"toolu_2","name":"Bash","input":{"command":"go test ./parser"}}]}`, at(7*time.Minute), model, usage) +
			line(`"type":"user","uuid":"u5","parentUuid":"u4","timestamp":%q,"message":{"role":"user","content":[{"tool_use_id":"toolu_2","type":"tool_result","content":"ok"}]},"promptId":"P1","sourceToolAssistantUUID":"u4"`, at(10*time.Minute)) +
			line(`"type":"user","uuid":"u6","parentUuid":"u5","timestamp":%q,"message":{"role":"user","content":"Now tidy it"},"promptId":"P2","promptSource":"typed","origin":{"kind":"human"}`, at(12*time.Minute)) +
			line(`"type":"assistant","uuid":"u7","parentUuid":"u6","timestamp":%q,"requestId":"req_C","message":{"id":"msg_C","role":"assistant","model":%q,"stop_reason":"end_turn",%s,"content":[{"type":"text","text":"Tidied."}]}`, at(15*time.Minute), model, usage)
	}
	now := time.Now()
	yesterday := time.Date(now.Year(), now.Month(), now.Day(), 10, 0, 0, 0, time.Local).AddDate(0, 0, -1)
	files := map[string]string{
		"aaaaaaaa-0000-4000-8000-0000000000a1": session("aaaaaaaa-0000-4000-8000-0000000000a1", yesterday, "claude-sonnet-4-5"),
		"aaaaaaaa-0000-4000-8000-0000000000a2": session("aaaaaaaa-0000-4000-8000-0000000000a2", now.Add(-30*time.Minute), "fixture-model-x"),
	}
	for id, content := range files {
		if err := os.WriteFile(filepath.Join(project, id+".jsonl"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// commitRecently gives dir's person two commits today, one an agent
// co-authored, on its current branch.
func commitRecently(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"config", "user.email", "test@example.com"},
		{"commit", "-q", "--allow-empty", "-m", "Tidy the parser\n\nCo-Authored-By: Claude <noreply@anthropic.com>"},
		{"commit", "-q", "--allow-empty", "-m", "Rename a helper"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1",
			"GIT_AUTHOR_NAME=Test", "GIT_AUTHOR_EMAIL=test@example.com", "GIT_COMMITTER_NAME=Test", "GIT_COMMITTER_EMAIL=test@example.com")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}

// writePlanHistory writes a day of Claude plan samples whose 5-hour window
// reaches its limit once.
func writePlanHistory(t *testing.T, path string) {
	t.Helper()
	now := time.Now().UTC()
	var samples []planusage.Sample
	for i, used := range []float64{20, 55, 100, 100, 10, 35} {
		samples = append(samples, planusage.Sample{At: now.Add(time.Duration(i-6) * 3 * time.Hour), Provider: "claude", Window: "5-hour", UsedPercent: used})
	}
	for i, used := range []float64{40, 52, 61} {
		samples = append(samples, planusage.Sample{At: now.Add(time.Duration(i-3) * 6 * time.Hour), Provider: "claude", Window: "weekly", UsedPercent: used})
	}
	raw, _ := json.Marshal(samples)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// fixedPlans is a plan source that is never read: the history is written
// beforehand and the fixture does not sample.
type fixedPlans struct{}

func (fixedPlans) Read(context.Context) planusage.Plan { return planusage.Plan{Provider: "claude"} }

// authoredPullRequests is GitHub's answer to the delivery search: on
// acme/demo, a merged pull request reviewed after an hour, an open one with a
// conflict and a draft; nothing elsewhere.
func authoredPullRequests(search string) any {
	nodes := []any{}
	if strings.Contains(search, "repo:acme/demo ") {
		now := time.Now().UTC()
		at := func(d time.Duration) string { return now.Add(d).Format(time.RFC3339) }
		pr := func(number int, draft bool, state string, merged any, mergeable string, reviews []any) map[string]any {
			return map[string]any{"number": number, "isDraft": draft, "state": state, "createdAt": at(-48 * time.Hour), "mergedAt": merged, "mergeable": mergeable,
				"author": map[string]any{"login": "fixture"}, "reviews": map[string]any{"nodes": reviews},
				"commits": map[string]any{"nodes": []any{map[string]any{"commit": map[string]any{"statusCheckRollup": map[string]any{"state": "SUCCESS"}}}}}}
		}
		nodes = []any{
			pr(21, false, "MERGED", at(-42*time.Hour), "UNKNOWN", []any{map[string]any{"submittedAt": at(-47 * time.Hour), "author": map[string]any{"login": "reviewer"}}}),
			pr(22, false, "OPEN", nil, "CONFLICTING", []any{}),
			pr(23, true, "OPEN", nil, "MERGEABLE", []any{}),
		}
	}
	return map[string]any{"data": map[string]any{"search": map[string]any{"nodes": nodes, "pageInfo": map[string]any{"hasNextPage": false, "endCursor": nil}}}}
}
