package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// testPrompt stands in for a host's system prompt.
const testPrompt = "Extract the decisions. Cite ## Turn N ordinals."

var testExtractor = NewExtractor(testPrompt)

type fakeModel struct {
	reply    string
	err      error
	requests []ModelRequest
}

func (m *fakeModel) Complete(_ context.Context, req ModelRequest) (ModelResponse, error) {
	m.requests = append(m.requests, req)
	if m.err != nil {
		return ModelResponse{}, m.err
	}
	return ModelResponse{
		JSON:  []byte(m.reply),
		Model: "fake-model-1",
		Usage: Usage{InputTokens: 100, CachedInputTokens: 40, OutputTokens: 7},
	}, nil
}

func citationTranscript() *Transcript {
	return &Transcript{SessionID: "s1", Turns: []Turn{
		{Ordinal: 1, Human: "add a gate", EditedFiles: []string{"a.go", "b.go"}},
		{Ordinal: 3, Human: "continue", EditedFiles: []string{"c.go"}},
		{Ordinal: 4, Human: "thanks"},
	}}
}

func extract(t *testing.T, transcript *Transcript, reply string) Extraction {
	t.Helper()
	got, err := testExtractor.Extract(context.Background(), &fakeModel{reply: reply}, transcript, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestExtractSendsPromptTranscriptAndSchema(t *testing.T) {
	transcript := &Transcript{SessionID: "s1", Turns: []Turn{{Ordinal: 1, Human: "Gate the appliance", Events: []Event{
		{Kind: EventQuestion, Questions: []Question{{Text: "Where?", Options: []string{"API middleware +\nUI root loader (Recommended)", "Custom Node server"},
			Answer: "API middleware +\nUI root loader (Recommended)", Picked: true}}},
	}, EditedFiles: []string{"app/root.tsx", "internal/gate/http/middleware.go"}}}}
	model := &fakeModel{reply: `{"decisions":[{"summary":" Gate the appliance with an API middleware and a UI root loader. ",
		"title":" Gate the appliance ",
		"who":"agent_proposed_human_approved","category":"architecture","trigger":"request",
		"rationale":" It covers the API and the UI for the least effort. ","decided_turn":1,
		"evidence":"“API middleware +\nUI root loader (Recommended)”","rejected_alternative":"Custom Node server",
		"superseded":false,"turns":[1],"files":["internal/gate/http/middleware.go","./app/root.tsx"]}]}`}
	got, err := testExtractor.Extract(context.Background(), model, transcript, ExtractOptions{MaxInputTokens: 1 << 20, MaxOutputTokens: 4000})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 1 {
		t.Fatalf("model called %d times", len(model.requests))
	}
	req := model.requests[0]
	if req.System != testPrompt || req.User != transcript.Text() || !testExtractor.IsRequest(req) || testExtractor.SystemPrompt() != testPrompt {
		t.Error("request does not carry the extraction prompt and the cleaned transcript")
	}
	if req.SchemaName != ExtractSchemaName || !reflect.DeepEqual(req.Schema, ExtractSchema()) || req.MaxOutputTokens != 4000 {
		t.Errorf("request schema or output cap: %+v", req)
	}
	wantDecision := Decision{
		Title:               "Gate the appliance",
		Summary:             "Gate the appliance with an API middleware and a UI root loader.",
		Category:            CategoryArchitecture,
		Who:                 WhoApproved,
		Trigger:             TriggerRequest,
		Rationale:           "It covers the API and the UI for the least effort.",
		RejectedAlternative: "Custom Node server",
		Turns:               []int{1},
		DecidedTurn:         1,
		Evidence:            "API middleware + UI root loader (Recommended)",
		EvidenceTurn:        1,
		Files:               []string{"internal/gate/http/middleware.go", "app/root.tsx"},
	}
	if got.SessionID != transcript.SessionID || got.PromptVersion != testExtractor.PromptVersion() || got.Model != "fake-model-1" || got.Usage.CachedInputTokens != 40 ||
		len(got.Decisions) != 1 || !reflect.DeepEqual(got.Decisions[0], wantDecision) || got.Dropped != nil {
		t.Errorf("extraction = %+v", got)
	}
}

func TestPromptVersionCoversPromptAndSchema(t *testing.T) {
	version := testExtractor.PromptVersion()
	if len(version) != 12 || strings.Trim(version, "0123456789abcdef") != "" {
		t.Errorf("PromptVersion = %q, want 12 hex characters", version)
	}
	if again := NewExtractor(testPrompt).PromptVersion(); again != version {
		t.Errorf("version is not stable: %q then %q", version, again)
	}
	if other := NewExtractor(testPrompt + " ").PromptVersion(); other == version {
		t.Error("a different prompt has the same version")
	}
	if got := extract(t, citationTranscript(), `{"decisions":[]}`); got.PromptVersion != version {
		t.Errorf("extraction prompt version = %q, want %q", got.PromptVersion, version)
	}
}

func TestExtractTokenCap(t *testing.T) {
	transcript := citationTranscript()
	estimate := testExtractor.EstimateTokens(transcript, ExtractOptions{})
	if estimate != EstimateTokens(testPrompt, transcript.Text()) {
		t.Fatalf("estimate = %d", estimate)
	}
	model := &fakeModel{reply: `{"decisions":[]}`}
	_, err := testExtractor.Extract(context.Background(), model, transcript, ExtractOptions{MaxInputTokens: estimate - 1})
	if !errors.Is(err, ErrTranscriptTooLarge) {
		t.Fatalf("err = %v, want ErrTranscriptTooLarge", err)
	}
	if len(model.requests) != 0 {
		t.Error("the model was called over the cap")
	}
	if _, err := testExtractor.Extract(context.Background(), model, transcript, ExtractOptions{MaxInputTokens: estimate}); err != nil {
		t.Fatalf("at the cap: %v", err)
	}
}

func TestMaxTokensBoundsTextWithFewBytesPerToken(t *testing.T) {
	transcript := &Transcript{SessionID: "s", Turns: []Turn{{Ordinal: 1, Human: strings.Repeat("决定使用数据库", 2000)}}}
	request := testExtractor.Request(transcript.Text(), 0)
	schema, err := json.Marshal(request.Schema)
	if err != nil {
		t.Fatal(err)
	}
	bytes := int64(len(request.System) + len(request.User) + len(schema))
	if got := testExtractor.MaxTokens(transcript, ExtractOptions{}); got < bytes+FramingTokens || got < 4*testExtractor.EstimateTokens(transcript, ExtractOptions{})-4 {
		t.Fatalf("MaxTokens = %d, want at least the request's %d bytes plus framing", got, bytes)
	}
	if !testExtractor.IsRequest(request) || testExtractor.IsRequest(ModelRequest{System: "other", SchemaName: ExtractSchemaName}) {
		t.Fatal("IsRequest does not tell the extraction request apart")
	}
}

func TestExtractSchemaIsStrict(t *testing.T) {
	var objects int
	var walk func(string, any)
	walk = func(at string, node any) {
		schema, ok := node.(map[string]any)
		if !ok {
			return
		}
		if schema["type"] == "object" {
			objects++
			if schema["additionalProperties"] != false {
				t.Errorf("%s: additionalProperties is not false", at)
			}
			properties, _ := schema["properties"].(map[string]any)
			var required []string
			for _, name := range schema["required"].([]any) {
				required = append(required, name.(string))
			}
			var names []string
			for name := range properties {
				names = append(names, name)
			}
			slices.Sort(required)
			slices.Sort(names)
			if !slices.Equal(required, names) {
				t.Errorf("%s: required %v, properties %v", at, required, names)
			}
		}
		for key, value := range schema {
			if child, ok := value.(map[string]any); ok {
				if key == "properties" {
					for name, property := range child {
						walk(at+"."+name, property)
					}
				} else {
					walk(at+"."+key, child)
				}
			}
		}
	}
	data, err := json.Marshal(ExtractSchema())
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	walk("$", schema)
	if objects != 2 {
		t.Errorf("walked %d objects, want 2", objects)
	}
}

func TestExtractSchemaPropertyOrder(t *testing.T) {
	data, err := json.Marshal(ExtractSchema())
	if err != nil {
		t.Fatal(err)
	}
	schema := string(data)
	last := -1
	for _, name := range []string{"summary", "title", "who", "category", "trigger", "rationale", "rejected_alternative", "superseded", "turns", "decided_turn", "evidence", "files"} {
		at := strings.Index(schema, `"`+name+`":{`)
		if at < 0 || at < last {
			t.Fatalf("property %q is out of order in %s", name, schema)
		}
		last = at
	}
}

func TestExtractValidatesCitations(t *testing.T) {
	got := extract(t, citationTranscript(), `{"decisions":[
		{"summary":"bad turn and bad file","who":"agent","category":"implementation","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[3,2,1,1],"files":["a.go","z.go","a.go"]},
		{"summary":"file only from an invalid turn","who":"human","category":"scope","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":true,"turns":[4,9],"files":["c.go"]},
		{"summary":"no valid turns","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[0,7],"files":["a.go"]},
		{"summary":"no turns at all","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[],"files":[]},
		{"summary":"shell-only edit","who":"agent","category":"behavior","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[4],"files":[]}
	]}`)
	want := []Decision{
		{Summary: "bad turn and bad file", Who: WhoAgent, Category: CategoryImplementation, Trigger: TriggerRequest, Turns: []int{1, 3}, Files: []string{"a.go"}},
		{Summary: "file only from an invalid turn", Who: WhoHuman, Category: CategoryScope, Trigger: TriggerRequest, Superseded: true, Turns: []int{4}, Files: []string{}},
		{Summary: "shell-only edit", Who: WhoAgent, Category: CategoryBehavior, Trigger: TriggerRequest, Turns: []int{4}, Files: []string{}},
	}
	if !reflect.DeepEqual(got.Decisions, want) {
		t.Errorf("decisions = %+v", got.Decisions)
	}
	wantDropped := []Dropped{
		{Decision: 0, Summary: "bad turn and bad file", Turn: ptr(2), Reason: "turn is not in the transcript"},
		{Decision: 0, Summary: "bad turn and bad file", File: "z.go", Reason: "file was not edited in a cited turn"},
		{Decision: 1, Summary: "file only from an invalid turn", Turn: ptr(9), Reason: "turn is not in the transcript"},
		{Decision: 1, Summary: "file only from an invalid turn", File: "c.go", Reason: "file was not edited in a cited turn"},
		{Decision: 2, Summary: "no valid turns", Reason: "no cited turn is in the transcript (cited 0, 7)"},
		{Decision: 3, Summary: "no turns at all", Reason: "no turn is cited"},
	}
	if !reflect.DeepEqual(got.Dropped, wantDropped) {
		t.Errorf("dropped = %+v", got.Dropped)
	}
}

func ptr(turn int) *int { return &turn }

func TestExtractValidatesTriggerAndDecidedTurn(t *testing.T) {
	got := extract(t, citationTranscript(), `{"decisions":[
		{"summary":"unknown trigger, uncited decided turn","who":"human","category":"scope","trigger":"boss","rationale":"Asked for",
		 "rejected_alternative":"","superseded":false,"turns":[1],"decided_turn":3,"evidence":"","files":[]},
		{"summary":"negative decided turn","who":"agent","category":"scope","trigger":"finding","rationale":"",
		 "rejected_alternative":"","superseded":false,"turns":[1],"decided_turn":-1,"evidence":"","files":[]},
		{"summary":"decided turn kept","who":"agent","category":"scope","trigger":"review","rationale":"",
		 "rejected_alternative":"","superseded":false,"turns":[1,3],"decided_turn":3,"evidence":"","files":[]}
	]}`)
	want := []Decision{
		{Summary: "unknown trigger, uncited decided turn", Who: WhoHuman, Category: CategoryScope, Rationale: "Asked for", Turns: []int{1}, Files: []string{}},
		{Summary: "negative decided turn", Who: WhoAgent, Category: CategoryScope, Trigger: TriggerFinding, Turns: []int{1}, Files: []string{}},
		{Summary: "decided turn kept", Who: WhoAgent, Category: CategoryScope, Trigger: TriggerReview, Turns: []int{1, 3}, DecidedTurn: 3, Files: []string{}},
	}
	if !reflect.DeepEqual(got.Decisions, want) {
		t.Errorf("decisions = %+v", got.Decisions)
	}
	wantDropped := []Dropped{
		{Decision: 0, Summary: "unknown trigger, uncited decided turn", Field: "trigger", Reason: `unknown trigger "boss"`},
		{Decision: 0, Summary: "unknown trigger, uncited decided turn", Field: "decided_turn", Turn: ptr(3), Reason: "decided turn is not a cited turn"},
		{Decision: 1, Summary: "negative decided turn", Field: "decided_turn", Turn: ptr(-1), Reason: "decided turn is not a cited turn"},
	}
	if !reflect.DeepEqual(got.Dropped, wantDropped) {
		t.Errorf("dropped = %+v", got.Dropped)
	}
}

func TestExtractKeepsEvidenceQuotedFromOnePassageOfACitedTurn(t *testing.T) {
	transcript := &Transcript{SessionID: "s1", Turns: []Turn{
		{Ordinal: 1, Human: "Put a **gate** in front of the API.", Events: []Event{
			{Kind: EventAgentText, Text: "I'll add a gate in front of the API."},
		}},
		{Ordinal: 3, Human: "continue", Events: []Event{
			{Kind: EventAgentText, Text: "We add a gate in the `middleware`."},
			{Kind: EventQuestion, Questions: []Question{{Text: "Where should it live?", Options: []string{"Middleware", "Proxy"}, Answer: "Middleware please", Picked: true}}},
		}},
		{Ordinal: 4, Human: `Use "strict" mode, not "lax".`},
		{Ordinal: 5, Human: "plan it", Events: []Event{
			{Kind: EventPlan, Text: "# Plan\n\n- Keep the gate\n  in middleware", Outcome: PlanRejected, Feedback: "Move it to the proxy instead"},
			{Kind: EventSubagent, Text: "The proxy already strips headers."},
		}},
	}}
	for _, tc := range []struct {
		name, evidence string
		turns          string
		decided        int
		wantEvidence   string
		wantTurn       int
	}{
		{name: "case and markdown differ", evidence: "put a gate in\n front of the API", turns: "[1]", wantEvidence: "put a gate in front of the API", wantTurn: 1},
		{name: "decided turn preferred", evidence: "add a gate", turns: "[1,3]", decided: 3, wantEvidence: "add a gate", wantTurn: 3},
		{name: "first cited turn when the decided turn was removed", evidence: "add a gate", turns: "[1,3]", decided: 9, wantEvidence: "add a gate", wantTurn: 1},
		{name: "answer to a question", evidence: "“Middleware please”", turns: "[3]", wantEvidence: "Middleware please", wantTurn: 3},
		{name: "quote that is itself quoted", evidence: `"strict" mode, not "lax"`, turns: "[4]", wantEvidence: `"strict" mode, not "lax"`, wantTurn: 4},
		{name: "single quotes around it", evidence: `'Use "strict" mode'`, turns: "[4]", wantEvidence: `Use "strict" mode`, wantTurn: 4},
		{name: "typographic single quotes", evidence: "‘Use \"strict\" mode’", turns: "[4]", wantEvidence: `Use "strict" mode`, wantTurn: 4},
		{name: "plan across lines", evidence: "Keep the gate in middleware", turns: "[5]", wantEvidence: "Keep the gate in middleware", wantTurn: 5},
		{name: "plan feedback", evidence: "Move it to the proxy", turns: "[5]", wantEvidence: "Move it to the proxy", wantTurn: 5},
		{name: "subagent report", evidence: "proxy already strips headers", turns: "[5]", wantEvidence: "proxy already strips headers", wantTurn: 5},
		{name: "label", evidence: "Human: Put a gate", turns: "[1]"},
		{name: "offered option", evidence: "Middleware | Proxy", turns: "[3]"},
		{name: "across passages", evidence: "front of the API. I'll add a gate", turns: "[1]"},
		{name: "one word", evidence: "continue", turns: "[3]"},
		{name: "uncited turn", evidence: "strict mode", turns: "[1]"},
		{name: "invented", evidence: "we chose a gate", turns: "[1]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			evidence, err := json.Marshal(tc.evidence)
			if err != nil {
				t.Fatal(err)
			}
			got := extract(t, transcript, fmt.Sprintf(`{"decisions":[{"summary":"Gate the API","who":"human","category":"scope",
				"trigger":"request","rationale":"","rejected_alternative":"","superseded":false,"turns":%s,"decided_turn":%d,
				"evidence":%s,"files":[]}]}`, tc.turns, tc.decided, evidence))
			if len(got.Decisions) != 1 {
				t.Fatalf("decisions = %+v", got.Decisions)
			}
			if d := got.Decisions[0]; d.Evidence != tc.wantEvidence || d.EvidenceTurn != tc.wantTurn {
				t.Errorf("evidence = %q in turn %d, want %q in turn %d", d.Evidence, d.EvidenceTurn, tc.wantEvidence, tc.wantTurn)
			}
			var removed []Dropped
			for _, dropped := range got.Dropped {
				if dropped.Field == "evidence" {
					removed = append(removed, dropped)
				}
			}
			if tc.wantTurn != 0 && len(removed) != 0 {
				t.Errorf("dropped = %+v", got.Dropped)
			}
			if tc.wantTurn == 0 && (len(removed) != 1 || !strings.Contains(removed[0].Reason, string(evidence))) {
				t.Errorf("dropped = %+v, want the quote's removal", got.Dropped)
			}
		})
	}
}

// A quote over maxEvidenceWords is removed even when the turn holds it.
func TestExtractRemovesOverlongEvidence(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("keep the gate ", 21))
	transcript := &Transcript{SessionID: "s1", Turns: []Turn{{Ordinal: 1, Human: long}}}
	got := extract(t, transcript, fmt.Sprintf(`{"decisions":[{"summary":"Gate the API","who":"human","category":"scope",
		"trigger":"request","rationale":"","rejected_alternative":"","superseded":false,"turns":[1],"decided_turn":1,
		"evidence":%q,"files":[]}]}`, long))
	if len(got.Decisions) != 1 || got.Decisions[0].Evidence != "" || got.Decisions[0].EvidenceTurn != 0 {
		t.Fatalf("decisions = %+v", got.Decisions)
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Field != "evidence" || got.Dropped[0].Reason != "evidence has 63 words, more than 60" {
		t.Fatalf("dropped = %+v", got.Dropped)
	}
}

func TestExtractRecordsRemovedTurnZero(t *testing.T) {
	got := extract(t, citationTranscript(), `{"decisions":[
		{"summary":"cites turn zero","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[0,1],"files":["a.go"]}
	]}`)
	if len(got.Decisions) != 1 || !slices.Equal(got.Decisions[0].Turns, []int{1}) {
		t.Errorf("decisions = %+v", got.Decisions)
	}
	data, err := json.Marshal(got.Dropped)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"decision":0,"summary":"cites turn zero","turn":0,"reason":"turn is not in the transcript"}]`; string(data) != want {
		t.Errorf("dropped = %s, want %s", data, want)
	}
}

func TestExtractRemovesUncleanFiles(t *testing.T) {
	got := extract(t, citationTranscript(), `{"decisions":[
		{"summary":"escapes the repository","who":"agent","category":"implementation","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[1],"files":["../a.go","/abs/b.go","a.go"]}
	]}`)
	if len(got.Decisions) != 1 || !slices.Equal(got.Decisions[0].Files, []string{"a.go"}) {
		t.Errorf("decisions = %+v", got.Decisions)
	}
	var files []string
	for _, dropped := range got.Dropped {
		files = append(files, dropped.File)
	}
	if !slices.Equal(files, []string{"../a.go", "/abs/b.go"}) {
		t.Errorf("dropped = %+v", got.Dropped)
	}
}

func TestExtractEmptyTranscriptSkipsModel(t *testing.T) {
	model := &fakeModel{reply: `{"decisions":[]}`}
	got, err := testExtractor.Extract(context.Background(), model, &Transcript{SessionID: "s0"}, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(model.requests) != 0 {
		t.Error("the model was called for an empty transcript")
	}
	if got.SessionID != "s0" || got.Decisions == nil || len(got.Decisions) != 0 || got.PromptVersion != testExtractor.PromptVersion() {
		t.Errorf("extraction = %+v", got)
	}
}

func TestExtractPropagatesModelError(t *testing.T) {
	cause := errors.New("provider unavailable")
	_, err := testExtractor.Extract(context.Background(), &fakeModel{err: cause}, citationTranscript(), ExtractOptions{})
	if !errors.Is(err, cause) {
		t.Errorf("err = %v, want %v", err, cause)
	}
}

func TestExtractDropsInvalidDecisions(t *testing.T) {
	got := extract(t, citationTranscript(), `{"decisions":[
		{"summary":"unknown who","who":"pair","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"","superseded":false,"turns":[1],"files":[]},
		{"summary":"unknown category","who":"agent","category":"ops","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"","superseded":false,"turns":[1],"files":[]},
		{"summary":"  ","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"","superseded":false,"turns":[1],"files":[]},
		{"summary":"valid","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"","superseded":false,"turns":[1],"files":["b.go"]}
	]}`)
	if len(got.Decisions) != 1 || got.Decisions[0].Summary != "valid" {
		t.Errorf("decisions = %+v", got.Decisions)
	}
	if len(got.Dropped) != 3 {
		t.Fatalf("dropped = %+v", got.Dropped)
	}
	for i, reason := range []string{`unknown who "pair"`, `unknown category "ops"`, "summary is empty"} {
		if got.Dropped[i].Decision != i || got.Dropped[i].Reason != reason {
			t.Errorf("dropped[%d] = %+v, want reason %q", i, got.Dropped[i], reason)
		}
	}
}

// NUL characters, which text stores such as PostgreSQL reject, are removed
// from the reply's text, so a decision can be stored.
func TestExtractRemovesNULCharacters(t *testing.T) {
	got := extract(t, citationTranscript(), `{"decisions":[
		{"summary":"Use\u0000 postgres","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"sql\u0000ite","superseded":false,"turns":[1],"files":[]},
		{"summary":"\u0000","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"","superseded":false,"turns":[1],"files":[]}
	]}`)
	if len(got.Decisions) != 1 || got.Decisions[0].Summary != "Use postgres" || got.Decisions[0].RejectedAlternative != "sqlite" {
		t.Fatalf("decisions = %+v", got.Decisions)
	}
	if len(got.Dropped) != 1 || got.Dropped[0].Reason != "summary is empty" {
		t.Fatalf("dropped = %+v", got.Dropped)
	}
}

func TestExtractRejectsMalformedReplies(t *testing.T) {
	for name, reply := range map[string]string{
		"not json":        `{"decisions":[`,
		"wrong type":      `{"decisions":{}}`,
		"missing list":    `{}`,
		"null list":       `{"decisions":null}`,
		"unknown field":   `{"decisions":[],"notes":"x"}`,
		"missing field":   `{"decisions":[{"summary":"x","who":"agent","category":"testing","superseded":false,"turns":[1],"files":[]}]}`,
		"string turns":    `{"decisions":[{"summary":"x","who":"agent","category":"testing","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"","superseded":false,"turns":["1"],"files":[]}]}`,
		"trailing object": `{"decisions":[]}{}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := testExtractor.Extract(context.Background(), &fakeModel{reply: reply}, citationTranscript(), ExtractOptions{}); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func extensionTranscript() *Transcript {
	return &Transcript{SessionID: "s1", Turns: []Turn{
		{Ordinal: 1, Human: "add a gate", EditedFiles: []string{"a.go"}},
		{Ordinal: 2, Human: "use a header", EditedFiles: []string{"b.go"}},
		{Ordinal: 3, Human: "drop the header, use a cookie", EditedFiles: []string{"b.go"}},
		{Ordinal: 5, Human: "add tests", EditedFiles: []string{"b_test.go"}},
	}}
}

func existingDecisions() []Decision {
	return []Decision{
		{ID: "0f9e8d7c-0000-4000-8000-000000000001", Title: "Gate the API", Summary: "Gate the API with a middleware.",
			Who: WhoHuman, Category: CategoryArchitecture, Superseded: true, Turns: []int{1}, Files: []string{"a.go"}},
		{ID: "0f9e8d7c-0000-4000-8000-000000000002", Summary: "Authenticate with a header.",
			Who: WhoHuman, Category: CategoryInterface, Turns: []int{2, 3}, Files: []string{"b.go"}},
	}
}

func TestExtractExtendsExistingDecisions(t *testing.T) {
	transcript := extensionTranscript()
	existing := existingDecisions()
	model := &fakeModel{reply: `{"decisions":[
		{"summary":"Authenticate with a header again.","who":"human","category":"interface","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[2,9],"files":[]},
		{"summary":"Authenticate with a cookie.","who":"human","category":"interface","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[2,3],"files":["b.go"]},
		{"summary":"Test the cookie.","who":"agent","category":"testing","trigger":"initiative","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[5],"files":["b_test.go"]}
	],"superseded_existing":["D2 (turns 2, 3)","d9","x"]}`}
	opts := ExtractOptions{Existing: existing, Cutoff: 2}
	got, err := testExtractor.Extract(context.Background(), model, transcript, opts)
	if err != nil {
		t.Fatal(err)
	}
	marker := "--- Decisions through turn 2 are already extracted; decisions after this line may already be listed ---\n\n"
	wantUser := "# Existing decisions\n" +
		"- d1 (turns 1; superseded): Gate the API: Gate the API with a middleware.\n" +
		"- d2 (turns 2, 3): Authenticate with a header.\n" +
		"\n# Transcript\n\n" + strings.Replace(transcript.Text(), "## Turn 3\n", marker+"## Turn 3\n", 1)
	user := model.requests[0].User
	if user != wantUser {
		t.Errorf("user message =\n%s\nwant\n%s", user, wantUser)
	}
	if strings.Contains(user, "0f9e8d7c") {
		t.Error("user message shows a decision ID")
	}
	cookie := Decision{Summary: "Authenticate with a cookie.", Who: WhoHuman, Category: CategoryInterface, Trigger: TriggerRequest, Turns: []int{2, 3}, Files: []string{"b.go"}}
	tests := Decision{Summary: "Test the cookie.", Who: WhoAgent, Category: CategoryTesting, Trigger: TriggerInitiative, Turns: []int{5}, Files: []string{"b_test.go"}}
	superseded := existingDecisions()[1]
	superseded.Superseded = true
	want := []Decision{existingDecisions()[0], superseded, cookie, tests}
	if !reflect.DeepEqual(got.Decisions, want) {
		t.Errorf("decisions = %+v", got.Decisions)
	}
	if !reflect.DeepEqual(existing, existingDecisions()) {
		t.Error("Extract changed opts.Existing")
	}
	wantDropped := []Dropped{
		{Decision: 0, Summary: "Authenticate with a header again.", Reason: "no cited turn is after the cutoff turn 2"},
	}
	if !reflect.DeepEqual(got.Dropped, wantDropped) {
		t.Errorf("dropped = %+v", got.Dropped)
	}
	if estimate := testExtractor.EstimateTokens(transcript, opts); estimate != EstimateTokens(testPrompt, user) {
		t.Errorf("EstimateTokens = %d, want the sent message's %d", estimate, EstimateTokens(testPrompt, user))
	}
	if max := testExtractor.MaxTokens(transcript, opts); max <= testExtractor.MaxTokens(transcript, ExtractOptions{}) {
		t.Errorf("MaxTokens = %d, not more than the plain text's", max)
	}
	opts.MaxInputTokens = testExtractor.EstimateTokens(transcript, ExtractOptions{})
	if _, err := testExtractor.Extract(context.Background(), model, transcript, opts); !errors.Is(err, ErrTranscriptTooLarge) {
		t.Errorf("cap of the plain text: err = %v, want ErrTranscriptTooLarge", err)
	}
}

func TestExtractWithExistingAndNoCutoff(t *testing.T) {
	transcript := extensionTranscript()
	model := &fakeModel{reply: `{"decisions":[
		{"summary":"Name the gate middleware.","who":"agent","category":"implementation","trigger":"initiative","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[1],"files":["a.go"]}
	],"superseded_existing":["d1"]}`}
	got, err := testExtractor.Extract(context.Background(), model, transcript, ExtractOptions{Existing: existingDecisions()})
	if err != nil {
		t.Fatal(err)
	}
	user := model.requests[0].User
	if !strings.HasPrefix(user, "# Existing decisions\n- d1 ") || !strings.HasSuffix(user, "\n# Transcript\n\n"+transcript.Text()) {
		t.Errorf("user message =\n%s", user)
	}
	if len(got.Decisions) != 3 || got.Decisions[2].Summary != "Name the gate middleware." || !got.Decisions[0].Superseded || got.Decisions[1].Superseded {
		t.Errorf("decisions = %+v", got.Decisions)
	}
}

func TestExtractWithCutoffAndNoExisting(t *testing.T) {
	transcript := extensionTranscript()
	model := &fakeModel{reply: `{"decisions":[],"superseded_existing":[]}`}
	if _, err := testExtractor.Extract(context.Background(), model, transcript, ExtractOptions{Cutoff: 5}); err != nil {
		t.Fatal(err)
	}
	// A cutoff at the last turn puts the marker after all of them.
	want := "# Existing decisions\n(none)\n\n# Transcript\n\n" + transcript.Text() +
		"\n--- Decisions through turn 5 are already extracted; decisions after this line may already be listed ---\n\n"
	if user := model.requests[0].User; user != want {
		t.Errorf("user message =\n%s\nwant\n%s", user, want)
	}
}

func TestExtractPlainIgnoresSupersededHandles(t *testing.T) {
	transcript := extensionTranscript()
	model := &fakeModel{reply: `{"decisions":[],"superseded_existing":["d1"]}`}
	got, err := testExtractor.Extract(context.Background(), model, transcript, ExtractOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if model.requests[0].User != transcript.Text() || len(got.Decisions) != 0 {
		t.Errorf("user message = %q, decisions = %+v", model.requests[0].User, got.Decisions)
	}
	empty, err := testExtractor.Extract(context.Background(), model, &Transcript{SessionID: "s0"}, ExtractOptions{Existing: existingDecisions()})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(empty.Decisions, existingDecisions()) {
		t.Errorf("no turns: decisions = %+v, want the existing ones", empty.Decisions)
	}
}

func TestExtractSupersedesOnlyWithANewDecision(t *testing.T) {
	// The only new decision is at the cutoff and dropped, so nothing
	// replaces d2.
	model := &fakeModel{reply: `{"decisions":[
		{"summary":"Authenticate with a header again.","who":"human","category":"interface","trigger":"request","rationale":"","decided_turn":0,"evidence":"","rejected_alternative":"",
		 "superseded":false,"turns":[2],"files":[]}
	],"superseded_existing":["d2"]}`}
	got, err := testExtractor.Extract(context.Background(), model, extensionTranscript(), ExtractOptions{Existing: existingDecisions(), Cutoff: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Decisions, existingDecisions()) {
		t.Errorf("decisions = %+v, want the existing ones unchanged", got.Decisions)
	}
}

func TestExtendedReadsHandles(t *testing.T) {
	found := []Decision{{Summary: "new"}}
	for handle, want := range map[string]bool{"d2": true, "D2": true, "d2 (turns 2, 3)": true, "d02": true, "d3": false, "d0": false, "x2": false, " d2": false} {
		got := extended(existingDecisions(), []string{handle}, found)
		if got[1].Superseded != want || got[0].Superseded != true {
			t.Errorf("%q: decisions = %+v", handle, got)
		}
	}
}

// The published prompt's version names it, so its extractions never share a
// cache with a host's prompt of the same text.
func TestPublicExtractorVersionNamesThePublishedPrompt(t *testing.T) {
	public := NewPublicExtractor()
	hashed := NewExtractor(public.SystemPrompt()).PromptVersion()
	if public.SystemPrompt() == "" || public.PromptVersion() != PublicPromptName+"-"+hashed {
		t.Fatalf("PromptVersion = %q, want %s-%s", public.PromptVersion(), PublicPromptName, hashed)
	}
}
