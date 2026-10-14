package traceprocessing

import (
	"reflect"
	"testing"

	"github.com/baumai/baum/sessions/session"
)

func TestTallyRows(t *testing.T) {
	graph := &session.Graph{
		Sessions: make([]session.Session, 1), UserInputs: make([]session.UserInput, 2), ModelInvocations: make([]session.ModelInvocation, 3),
		AssistantMessages: make([]session.AssistantMessage, 4), ToolUses: make([]session.ToolUse, 5), ToolResults: make([]session.ToolResult, 6),
		FileTouches: make([]session.FileTouch, 7), Attributes: make([]session.SessionAttribute, 8), TokenUsage: make([]session.TokenUsage, 9), VendorIDs: make([]session.VendorID, 10),
	}
	if got, want := TallyRows(graph), (RowTally{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}); got != want {
		t.Fatalf("row tally = %+v, want %+v", got, want)
	}
}

func TestAppendSessionRowsPreservesEverySlice(t *testing.T) {
	sessions := []session.Session{{ID: "s1"}, {ID: "s2"}}
	attributes := []session.SessionAttribute{{ID: "a1"}, {ID: "a2"}}
	inputs := []session.UserInput{{ID: "i1"}, {ID: "i2"}}
	invocations := []session.ModelInvocation{{ID: "n1"}, {ID: "n2"}}
	messages := []session.AssistantMessage{{ID: "m1"}, {ID: "m2"}}
	uses, results := []session.ToolUse{{ID: "u1"}, {ID: "u2"}}, []session.ToolResult{{ID: "r1"}, {ID: "r2"}}
	touches, usage := []session.FileTouch{{ID: "f1"}, {ID: "f2"}}, []session.TokenUsage{{ID: "t1"}, {ID: "t2"}}
	vendorIDs := []session.VendorID{{OurID: "v1"}, {OurID: "v2"}}
	graph := &session.Graph{}
	AppendSessionRows(graph, &sessions[0], rowPointers(attributes...), rowPointers(inputs...), rowPointers(invocations...), rowPointers(messages...), rowPointers(uses...), rowPointers(results...), rowPointers(touches...), rowPointers(usage...), vendorIDs)
	AppendSessionRows(graph, &sessions[1], nil, nil, nil, nil, nil, nil, nil, nil, nil)
	want := &session.Graph{Sessions: sessions, Attributes: attributes, UserInputs: inputs, ModelInvocations: invocations, AssistantMessages: messages, ToolUses: uses, ToolResults: results, FileTouches: touches, TokenUsage: usage, VendorIDs: vendorIDs}
	if !reflect.DeepEqual(graph, want) {
		t.Fatalf("graph = %+v, want %+v", graph, want)
	}
}

func rowPointers[T any](rows ...T) []*T {
	result := make([]*T, len(rows))
	for i := range rows {
		result[i] = &rows[i]
	}
	return result
}
