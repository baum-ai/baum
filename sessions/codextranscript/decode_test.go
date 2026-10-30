package codextranscript

import (
	"strings"
	"testing"
)

func decodeText(t *testing.T, text string) ([]Line, bool, error) {
	t.Helper()
	return Decode(strings.NewReader(text))
}

func envelope(typ, payload string, extras ...string) string {
	extra := ""
	if len(extras) > 0 {
		extra = extras[0]
	}
	return `{"timestamp":"2026-08-01T12:00:00Z"` + extra + `,"type":"` + typ + `","payload":` + payload + "}\n"
}

func sessionMeta(fields string) string {
	return envelope("session_meta", `{"id":"thread",`+fields+`}`)
}

func TestFirstSessionMetaClassifiesThread(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   ThreadClass
	}{
		{"root", `"cli"`, ThreadRoot},
		{"child", `{"subagent":{"thread_spawn":{"parent_thread_id":"parent","depth":1,"agent_path":"/root/a"}}}`, ThreadChild},
		{"guardian", `{"subagent":{"other":"guardian"}}`, ThreadInternal},
		{"internal", `{"internal":"compact"}`, ThreadInternal},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lines, _, err := decodeText(t, sessionMeta(`"timestamp":"2026-08-01T12:00:00Z","cwd":"/repo","source":`+tt.source))
			if err != nil {
				t.Fatal(err)
			}
			got, err := ClassifyThread(lines)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("class = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSelfContainedPaginatedHistory(t *testing.T) {
	text := envelope("session_meta", `{"id":"thread","history_mode":"paginated","history_base":null,"source":"cli"}`, `,"ordinal":0`) +
		envelope("turn_context", `{"turn_id":"turn","model":"gpt-5"}`, `,"ordinal":1`)
	lines, _, err := decodeText(t, text)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 || lines[0].Ordinal != 1 || lines[1].Ordinal != 2 {
		t.Fatalf("line ordinals = %+v, want physical ordinals 1 and 2", lines)
	}
	first, err := DecodeFirst(strings.NewReader(text))
	if err != nil || first.SessionMeta.HistoryMode != "paginated" {
		t.Fatalf("DecodeFirst() = %+v, %v", first, err)
	}
}

func TestNullHistoryBaseIsNotPaginated(t *testing.T) {
	if _, _, err := decodeText(t, sessionMeta(`"history_mode":"legacy","history_base":null,"source":"cli"`)); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyOrdinalEnvelopeRefused(t *testing.T) {
	_, _, err := decodeText(t, envelope("session_meta", `{"id":"thread","source":"cli"}`, `,"ordinal":0`))
	if err == nil || !strings.Contains(err.Error(), "paginated ordinal in a legacy rollout") {
		t.Fatalf("error = %v, want ordinal refusal", err)
	}
}

func TestPaginatedOrdinalsMustMatchPhysicalPosition(t *testing.T) {
	for _, tt := range []struct {
		name, extras string
	}{
		{"missing", ""},
		{"duplicate", `,"ordinal":0`},
		{"out of order", `,"ordinal":2`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			text := envelope("session_meta", `{"id":"thread","history_mode":"paginated","source":"cli"}`, `,"ordinal":0`) +
				envelope("turn_context", `{}`, tt.extras)
			if _, _, err := decodeText(t, text); err == nil || !strings.Contains(err.Error(), "line 2") {
				t.Fatalf("error = %v, want line 2 ordinal error", err)
			}
		})
	}
}

func TestExternalHistoryBaseRefused(t *testing.T) {
	_, _, err := decodeText(t, envelope("session_meta", `{"id":"thread","history_mode":"paginated","history_base":{"ordinal":42},"source":"cli"}`, `,"ordinal":0`))
	if err == nil || !strings.Contains(err.Error(), "external history_base") {
		t.Fatalf("error = %v, want external-history refusal", err)
	}
}

func TestPaginatedReasoningMayOmitOrdinal(t *testing.T) {
	for _, extras := range []string{"", `,"ordinal":1`} {
		text := envelope("session_meta", `{"id":"thread","history_mode":"paginated","source":"cli"}`, `,"ordinal":0`) +
			envelope("response_item", `{"type":"reasoning","summary":[]}`, extras) +
			envelope("turn_context", `{}`, `,"ordinal":2`)
		lines, _, err := decodeText(t, text)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != 3 || lines[1].Ordinal != 2 || lines[2].Ordinal != 3 {
			t.Fatalf("physical ordinals changed: %+v", lines)
		}
	}
}

func TestPaginatedReasoningStillRequiresCorrectSuppliedOrdinal(t *testing.T) {
	for _, extras := range []string{`,"ordinal":0`, `,"ordinal":2`} {
		text := envelope("session_meta", `{"id":"thread","history_mode":"paginated","source":"cli"}`, `,"ordinal":0`) +
			envelope("response_item", `{"type":"reasoning","summary":[]}`, extras)
		if _, _, err := decodeText(t, text); err == nil {
			t.Fatal("accepted incorrect reasoning ordinal")
		}
	}
	text := envelope("session_meta", `{"id":"thread","history_mode":"paginated","source":"cli"}`, `,"ordinal":0`) +
		envelope("response_item", `{"type":"reasoning","summary":[]}`) +
		envelope("turn_context", `{}`, `,"ordinal":1`)
	if _, _, err := decodeText(t, text); err == nil {
		t.Fatal("reasoning omission shifted following ordinal")
	}
}

func TestPaginatedCompactionMayOmitOrdinal(t *testing.T) {
	for _, extras := range []string{"", `,"ordinal":1`} {
		text := envelope("session_meta", `{"id":"thread","history_mode":"paginated","source":"cli"}`, `,"ordinal":0`) +
			envelope("compacted", `{}`, extras) +
			envelope("turn_context", `{}`, `,"ordinal":2`)
		lines, _, err := decodeText(t, text)
		if err != nil {
			t.Fatal(err)
		}
		if len(lines) != 3 || lines[1].Ordinal != 2 || lines[2].Ordinal != 3 {
			t.Fatalf("physical ordinals changed: %+v", lines)
		}
	}
	for _, extras := range []string{`,"ordinal":0`, `,"ordinal":2`} {
		text := envelope("session_meta", `{"id":"thread","history_mode":"paginated","source":"cli"}`, `,"ordinal":0`) +
			envelope("compacted", `{}`, extras)
		if _, _, err := decodeText(t, text); err == nil {
			t.Fatal("accepted incorrect compaction ordinal")
		}
	}
}

func TestTokenUsageRecordDecodes(t *testing.T) {
	usage := `{"input_tokens":20,"cached_input_tokens":5,"cache_write_input_tokens":0,"output_tokens":3,"reasoning_output_tokens":1,"total_tokens":23}`
	lines, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+envelope("token_usage_record", `{"thread_id":"th","turn_id":"tu","session_id":"se","root_turn_id":"ro","response_id":"resp","usage":`+usage+`,"turn_token_usage":`+usage+`,"thread_token_usage":{"input_tokens":50,"total_tokens":60}}`))
	if err != nil {
		t.Fatal(err)
	}
	line := lines[len(lines)-1]
	record := line.TokenUsageRecord
	if record == nil || line.Class != ClassTokenUsageRecord {
		t.Fatalf("line = %+v", line)
	}
	if record.ThreadID != "th" || record.TurnID != "tu" || record.SessionID != "se" || record.RootTurnID != "ro" || record.ResponseID != "resp" {
		t.Errorf("ids = %+v", record)
	}
	if record.Usage.InputTokens == nil || *record.Usage.InputTokens != 20 || record.Usage.TotalTokens == nil || *record.Usage.TotalTokens != 23 ||
		record.ThreadTokenUsage.InputTokens == nil || *record.ThreadTokenUsage.InputTokens != 50 || len(record.Raw) == 0 {
		t.Errorf("usage = %+v", record)
	}
}

func TestUnknownTypesAreKeptAsUnknown(t *testing.T) {
	lines, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+
		envelope("event_msg", `{"type":"future_event"}`)+
		envelope("response_item", `{"type":"future_item"}`)+
		envelope("future_record", `{"type":"x"}`))
	if err != nil {
		t.Fatal(err)
	}
	if !lines[1].Unknown || lines[1].EventMsg == nil || lines[1].EventMsg.Type != "future_event" {
		t.Errorf("event line = %+v", lines[1])
	}
	if !lines[2].Unknown || lines[2].ResponseItem == nil || lines[2].ResponseItem.Type != "future_item" {
		t.Errorf("item line = %+v", lines[2])
	}
	if !lines[3].Unknown || lines[3].Type != "future_record" {
		t.Errorf("record line = %+v", lines[3])
	}
	if lines[0].Unknown {
		t.Errorf("session_meta is unknown")
	}
}

func TestUntypedItemIsAnError(t *testing.T) {
	for _, class := range []string{"response_item", "event_msg"} {
		if _, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+envelope(class, `{}`)); err == nil {
			t.Errorf("untyped %s did not fail the rollout", class)
		}
	}
}

func TestMalformedKnownItemIsAnError(t *testing.T) {
	_, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+envelope("response_item", `{"type":"function_call","call_id":7}`))
	if err == nil {
		t.Fatal("malformed function_call did not fail the rollout")
	}
}

func TestTornFinalLineIsReported(t *testing.T) {
	lines, torn, err := decodeText(t, sessionMeta(`"source":"cli"`)+`{"timestamp":`)
	if err != nil {
		t.Fatal(err)
	}
	if !torn || len(lines) != 1 {
		t.Fatalf("got %d lines, torn=%v", len(lines), torn)
	}
}

func TestReadValidatesAndLabelsRollout(t *testing.T) {
	file, err := Read(strings.NewReader(sessionMeta(`"source":"cli"`)+`{"timestamp":`), "full.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if file.Path != "full.jsonl" || file.Meta.ID != "thread" || !file.TornFinalLine {
		t.Fatalf("file = %+v", file)
	}
	for _, source := range []string{"", envelope("turn_context", `{}`)} {
		if _, err := Read(strings.NewReader(source), "full.jsonl"); err == nil {
			t.Fatalf("Read(%q) succeeded", source)
		}
	}
}

func TestTurnContextAccessorsKeepEveryField(t *testing.T) {
	lines, _, err := decodeText(t, envelope("turn_context", `{"turn_id":"turn","model":"gpt-5","cwd":"/repo","future":{"on":true}}`))
	if err != nil {
		t.Fatal(err)
	}
	context := lines[0].TurnContext
	if context.TurnID() != "turn" || context.Model() != "gpt-5" || context.CWD() != "/repo" {
		t.Fatalf("typed context fields not decoded: %+v", context)
	}
	if _, ok := context.AttributeFields()["future"]; !ok {
		t.Fatal("future top-level field was not retained")
	}
	if _, ok := context.AttributeFields()["turn_id"]; ok {
		t.Fatal("turn_id was returned as an attribute")
	}
}

func TestCompactionItemAndRealtimeItemDecode(t *testing.T) {
	lines, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+
		envelope("response_item", `{"type":"compaction","id":"c","encrypted_content":"x","internal_chat_message_metadata_passthrough":{}}`)+
		envelope("realtime_item", `{"type":"realtime_session_started","id":"r","realtime_session_id":"s"}`))
	if err != nil {
		t.Fatal(err)
	}
	item := lines[1].ResponseItem
	if item == nil || item.Type != "compaction" || item.Compaction == nil || len(item.Compaction.Raw) == 0 {
		t.Fatalf("compaction line = %+v", lines[1])
	}
	if lines[2].Class != ClassRealtime || lines[2].Realtime == nil || len(lines[2].Realtime.Raw) == 0 {
		t.Fatalf("realtime line = %+v", lines[2])
	}
}

func TestGoalUpdateAndImageGenerationDecode(t *testing.T) {
	lines, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+
		envelope("event_msg", `{"type":"thread_goal_updated","threadId":"t","turnId":"u","goal":{}}`)+
		envelope("response_item", `{"type":"image_generation_call","id":"ig","status":"generating","result":"AAAA"}`))
	if err != nil {
		t.Fatal(err)
	}
	if lines[1].EventMsg == nil || lines[1].EventMsg.ThreadGoalUpdated == nil {
		t.Fatalf("goal line = %+v", lines[1])
	}
	if lines[2].ResponseItem == nil || lines[2].ResponseItem.ImageGeneration == nil {
		t.Fatalf("image line = %+v", lines[2])
	}
}

func TestConfigurationUpdateIsKeptAsWritten(t *testing.T) {
	payload := `{"type":"configuration_update","reasoning":{"effort":"medium"}}`
	lines, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+envelope("response_item", payload))
	if err != nil {
		t.Fatal(err)
	}
	item := lines[1].ResponseItem
	if lines[1].Unknown || item == nil || item.ConfigurationUpdate == nil || string(item.ConfigurationUpdate.Raw) != payload {
		t.Fatalf("item = %+v", item)
	}
}

func TestAdditionalToolsAreKeptAsWritten(t *testing.T) {
	payload := `{"type":"additional_tools","id":"at","role":"developer","tools":[{"name":"linear__save_comment"}]}`
	lines, _, err := decodeText(t, sessionMeta(`"source":"cli"`)+envelope("response_item", payload))
	if err != nil {
		t.Fatal(err)
	}
	item := lines[1].ResponseItem
	if item == nil || item.AdditionalTools == nil || string(item.AdditionalTools.Raw) != payload {
		t.Fatalf("item = %+v", item)
	}
}
