package traceprocessing

import "github.com/baum-ai/baum/sessions/session"

// RowTally is the non-serialized count of every graph row type.
type RowTally struct {
	Sessions, UserInputs, ModelInvocations, AssistantMessages, ToolUses int
	ToolResults, FileTouches, Attributes, TokenUsage, VendorIDs         int
}

func TallyRows(graph *session.Graph) RowTally {
	return RowTally{
		Sessions: len(graph.Sessions), UserInputs: len(graph.UserInputs), ModelInvocations: len(graph.ModelInvocations),
		AssistantMessages: len(graph.AssistantMessages), ToolUses: len(graph.ToolUses), ToolResults: len(graph.ToolResults),
		FileTouches: len(graph.FileTouches), Attributes: len(graph.Attributes), TokenUsage: len(graph.TokenUsage), VendorIDs: len(graph.VendorIDs),
	}
}

// ReportRows is the exact row-count JSON shared by Codex and Cursor.
type ReportRows struct {
	UserInputs        int `json:"user_inputs"`
	ModelInvocations  int `json:"model_invocations"`
	AssistantMessages int `json:"assistant_messages"`
	ToolUses          int `json:"tool_uses"`
	ToolResults       int `json:"tool_results"`
	FileTouches       int `json:"file_touches"`
	Attributes        int `json:"session_attributes"`
	TokenUsage        int `json:"token_usage"`
	VendorIDs         int `json:"vendor_ids"`
}

func (t RowTally) ReportRows() ReportRows {
	return ReportRows{t.UserInputs, t.ModelInvocations, t.AssistantMessages, t.ToolUses, t.ToolResults, t.FileTouches, t.Attributes, t.TokenUsage, t.VendorIDs}
}

func AppendSessionRows(graph *session.Graph, sessionRow *session.Session, attributes []*session.SessionAttribute, inputs []*session.UserInput, invocations []*session.ModelInvocation, messages []*session.AssistantMessage, tools []*session.ToolUse, results []*session.ToolResult, touches []*session.FileTouch, usage []*session.TokenUsage, vendorIDs []session.VendorID) {
	graph.Sessions = append(graph.Sessions, *sessionRow)
	graph.Attributes = appendPointers(graph.Attributes, attributes)
	graph.UserInputs = appendPointers(graph.UserInputs, inputs)
	graph.ModelInvocations = appendPointers(graph.ModelInvocations, invocations)
	graph.AssistantMessages = appendPointers(graph.AssistantMessages, messages)
	graph.ToolUses = appendPointers(graph.ToolUses, tools)
	graph.ToolResults = appendPointers(graph.ToolResults, results)
	graph.FileTouches = appendPointers(graph.FileTouches, touches)
	graph.TokenUsage = appendPointers(graph.TokenUsage, usage)
	graph.VendorIDs = append(graph.VendorIDs, vendorIDs...)
}

func appendPointers[T any](dst []T, src []*T) []T {
	for _, row := range src {
		dst = append(dst, *row)
	}
	return dst
}
