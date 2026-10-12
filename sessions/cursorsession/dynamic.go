package cursorsession

import (
	"bytes"
	"encoding/json"
	"strings"

	"github.com/lastpersonlabs/baum/sessions/session"
	"github.com/lastpersonlabs/baum/sessions/traceprocessing"
)

const (
	dynamicWrapper   = "CallDynamicTool"
	builtinNamespace = "cursor"
)

// builtinDynamicTools are the Cursor tools seen behind CallDynamicTool in the
// builtin namespace. A name outside this set and the browser_ family keeps the
// wrapper.
var builtinDynamicTools = map[string]bool{
	"Task": true, "ReadLints": true, "AwaitShell": true, "Delete": true,
	"WebFetch": true, "WebSearch": true, "TodoWrite": true,
	"GetDynamicTools": true, "SwitchMode": true, "SearchConversations": true,
}

// dynamicCall is the input of a CallDynamicTool block: the real tool name, its
// namespace (builtin tools use "cursor", other tools the server that offers
// them) and the arguments it was called with.
type dynamicCall struct {
	Namespace string          `json:"namespace"`
	ToolName  string          `json:"toolName"`
	Arguments json.RawMessage `json:"arguments"`
}

// resolvedTool is a portable tool use after dynamic-tool resolution.
type resolvedTool struct {
	Name     string
	Input    json.RawMessage
	Verb     session.ToolVerb
	Metadata json.RawMessage
}

// resolvePortableTool maps a portable tool block to its stored name, input and
// verb. A CallDynamicTool block carrying a recognised inner tool is stored
// under that tool with its arguments as input. Any other dynamic tool keeps the
// wrapper name, verb and input, with the inner name in metadata.
func resolvePortableTool(name string, input json.RawMessage) resolvedTool {
	base := map[string]any{"fallback_store": "jsonl"}
	resolved := resolvedTool{Name: name, Input: input, Verb: portableVerb(name)}
	if name == dynamicWrapper {
		var call dynamicCall
		if err := json.Unmarshal(input, &call); err == nil && call.ToolName != "" {
			dynamic := map[string]string{"wrapper": dynamicWrapper, "toolName": call.ToolName}
			if call.Namespace != "" {
				dynamic["namespace"] = call.Namespace
			}
			base["dynamic_tool"] = dynamic
			if knownDynamicTool(call) {
				resolved.Name = storedDynamicName(call)
				resolved.Input = dynamicArguments(call.Arguments)
				resolved.Verb = dynamicVerb(call)
			}
		}
	}
	resolved.Metadata, _ = json.Marshal(base)
	return resolved
}

// storedDynamicName is the stored name of a recognised dynamic tool. A tool from
// another namespace that shares a builtin's name, or the name of a subagent tool
// the platform matches by name (Agent, Task, spawn_agent), is stored as
// mcp__<namespace>__<tool>, so it cannot be mistaken for the builtin.
func storedDynamicName(call dynamicCall) string {
	if call.Namespace == "" || call.Namespace == builtinNamespace {
		return call.ToolName
	}
	if builtinDynamicTools[call.ToolName] || call.ToolName == "Agent" || call.ToolName == "spawn_agent" ||
		traceprocessing.GenericToolVerb(call.ToolName) != session.VerbOther {
		return "mcp__" + call.Namespace + "__" + call.ToolName
	}
	return call.ToolName
}

func knownDynamicTool(call dynamicCall) bool {
	if call.Namespace != "" && call.Namespace != builtinNamespace {
		return true
	}
	return builtinDynamicTools[call.ToolName] || strings.HasPrefix(call.ToolName, "browser_")
}

func dynamicVerb(call dynamicCall) session.ToolVerb {
	if call.Namespace != "" && call.Namespace != builtinNamespace {
		return session.VerbOther
	}
	return traceprocessing.GenericToolVerb(call.ToolName)
}

func dynamicArguments(raw json.RawMessage) json.RawMessage {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return json.RawMessage(`{}`)
	}
	return append(json.RawMessage(nil), raw...)
}
