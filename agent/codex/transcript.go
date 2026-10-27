package codex

import (
	"cmp"
	"strings"

	"github.com/baum-ai/baum/agent"
)

// itemFields are the parts of a Codex JSON item the transcript reads.
type itemFields struct {
	ID, Type, Status, Text, Command, Output, Message, Query, Server, Tool, Error string
	ExitCode                                                                     *int
	// Paths are a file_change's changes, each "kind path".
	Paths []string
}

// emitItem sends the transcript entry of an item event, if the item shows
// anything: the agent's messages and reasoning when completed, a tool when it
// starts and when it ends.
func emitItem(request agent.Request, eventType string, item itemFields) {
	finished := eventType == "item.completed"
	switch item.Type {
	case "agent_message", "reasoning":
		if finished && strings.TrimSpace(item.Text) != "" {
			kind := "message"
			if item.Type == "reasoning" {
				kind = "reasoning"
			}
			request.Emit(agent.TranscriptEntry{Kind: kind, Text: item.Text})
		}
	case "error":
		if finished {
			request.Emit(agent.TranscriptEntry{Kind: "error", Text: cmp.Or(item.Message, item.Text)})
		}
	case "command_execution", "file_change", "mcp_tool_call", "web_search":
		if eventType == "item.updated" {
			return
		}
		entry := agent.TranscriptEntry{Kind: "tool", ID: item.ID, Status: "running"}
		switch item.Type {
		case "command_execution":
			entry.Tool, entry.Title, entry.Text = "command", item.Command, item.Output
		case "file_change":
			entry.Tool, entry.Title = "file_change", strings.Join(item.Paths, ", ")
		case "mcp_tool_call":
			entry.Tool, entry.Title = "mcp_tool_call", strings.TrimSpace(item.Server+" "+item.Tool)
			entry.Text = item.Error
		case "web_search":
			entry.Tool, entry.Title = "web_search", item.Query
		}
		if !finished {
			entry.Text = ""
		} else {
			entry.Status = "completed"
			if item.Status == "failed" || (item.ExitCode != nil && *item.ExitCode != 0) || (item.Type == "mcp_tool_call" && item.Error != "") {
				entry.Status = "failed"
			}
		}
		request.Emit(entry)
	}
}
