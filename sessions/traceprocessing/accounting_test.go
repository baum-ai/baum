package traceprocessing_test

import (
	"encoding/json"
	"testing"

	"github.com/baum-ai/baum/sessions/claudesession"
	"github.com/baum-ai/baum/sessions/codexsession"
	"github.com/baum-ai/baum/sessions/cursorsession"
	"github.com/baum-ai/baum/sessions/entiresession"
	"github.com/baum-ai/baum/sessions/session"
	"github.com/baum-ai/baum/sessions/traceprocessing"
)

func TestAccountingCompatibility(t *testing.T) {
	lines := traceprocessing.LineAccounting{
		Total: 9, Mapped: 2, AttributeBearing: 1, Merged: 3,
		Discarded: map[string]int{"discarded": 3}, TornFinal: 4,
	}
	for _, account := range []traceprocessing.LineAccounting{lines} {
		if !account.Balanced() || account.Accounted() != 9 {
			t.Errorf("line account = %+v", account)
		}
	}
	provenance := traceprocessing.ProvenanceAccounting{Total: 6, Mapped: 2, Provenance: 1, Discarded: map[string]int{"unused": 3}}
	for _, account := range []traceprocessing.ProvenanceAccounting{provenance} {
		if !account.Balanced() || account.Accounted() != 6 {
			t.Errorf("provenance account = %+v", account)
		}
	}
	report := &entiresession.Report{Delegated: []entiresession.DelegateReport{{Codex: &codexsession.Report{UnsupportedTurns: 1}}}}
	if report.Balanced() {
		t.Fatal("delegated Codex unsupported turn accepted")
	}
	report.Delegated[0].Codex.UnsupportedTurns = 0
	if !report.Balanced() {
		t.Fatal("balanced delegated report rejected")
	}
}

func TestAccountingJSONShapes(t *testing.T) {
	assertJSON(t, claudesession.Lines{}, `{"total":0,"mapped":0,"attribute_bearing":0,"merged":0,"torn_final_lines":0}`)
	assertJSON(t, codexsession.Records{}, `{"total":0,"mapped":0,"attribute_bearing":0,"merged":0,"torn_final_lines":0}`)
	assertJSON(t, cursorsession.Bubbles{}, `{"total":0,"mapped":0,"provenance":0}`)
	assertJSON(t, cursorsession.PortableAccounting{}, `{"total":0,"mapped":0,"provenance":0}`)
	assertJSON(t, entiresession.Accounting{}, `{"total":0,"mapped":0,"envelope_evidence":0}`)
}

func TestRowJSONShapes(t *testing.T) {
	assertJSON(t, claudesession.Rows{}, `{"user_inputs":0,"model_invocations":0,"assistant_messages":0,"tool_uses":0,"tool_results":0,"file_touches":0,"session_attributes":0,"vendor_ids":0}`)
	assertJSON(t, codexsession.Rows{}, `{"user_inputs":0,"model_invocations":0,"assistant_messages":0,"tool_uses":0,"tool_results":0,"file_touches":0,"session_attributes":0,"token_usage":0,"vendor_ids":0}`)
	assertJSON(t, cursorsession.Rows{}, `{"user_inputs":0,"model_invocations":0,"assistant_messages":0,"tool_uses":0,"tool_results":0,"file_touches":0,"session_attributes":0,"token_usage":0,"vendor_ids":0}`)
	assertJSON(t, entiresession.Rows{}, `{"sessions":0,"user_inputs":0,"model_invocations":0,"assistant_messages":0,"tool_uses":0,"tool_results":0,"file_touches":0,"session_attributes":0,"token_usage":0,"vendor_ids":0}`)
}

func TestIDPrimitives(t *testing.T) {
	tests := []struct{ got, want string }{
		{traceprocessing.NewIDNamespace("claude_code", "S", "").Mint("session"), "3c6a03af-c9c5-53bc-8ebf-8850f3f25dac"},
		{traceprocessing.NewIDNamespace("codex", "T").Mint("user_input", "turn-1"), "a275e7bf-ea6c-5971-ad14-113c8b5dcad1"},
		{traceprocessing.NewIDNamespace("cursor", "C").Mint("tool_use", "bubble-1"), "41d8a9ed-b126-5406-be14-b8f86683c801"},
	}
	for _, test := range tests {
		if test.got != test.want {
			t.Errorf("minted %s, want %s", test.got, test.want)
		}
	}
	ref := "rollout.jsonl#4"
	rows := traceprocessing.AppendVendorID(nil, "codex", "session", "ours", session.EntityToolUse, "call_id", "call-1", &ref)
	want := session.VendorID{OurID: "ours", OurType: session.EntityToolUse, SessionID: "session", Vendor: "codex", VendorIDKind: "call_id", VendorID: "call-1", SourceRef: &ref}
	if len(rows) != 1 || rows[0] != want || len(traceprocessing.AppendVendorID(rows, "codex", "session", "ours", session.EntityToolUse, "call_id", "", &ref)) != 1 {
		t.Fatalf("vendor rows = %+v", rows)
	}
}

func TestPatchHeaderPathsPreserveScanLines(t *testing.T) {
	paths := traceprocessing.PatchHeaderPaths("*** Update File: old/name.go\r\n*** Move to: new/name.go\r\n")
	if len(paths) != 2 || paths[0] != "old/name.go" || paths[1] != "new/name.go" {
		t.Fatalf("paths = %q", paths)
	}
}

func assertJSON(t *testing.T, value any, want string) {
	if got, err := json.Marshal(value); err != nil || string(got) != want {
		t.Errorf("JSON = %s, %v; want %s", got, err, want)
	}
}
