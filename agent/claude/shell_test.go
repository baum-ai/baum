package claude

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/baum-ai/baum/agent"
)

// A system event may carry its message as a plain string (the sandbox's
// notices do); it reads as an empty message instead of ending the run.
func TestStreamMessageMayBeAString(t *testing.T) {
	var event struct {
		Message streamMessage `json:"message"`
	}
	if err := json.Unmarshal([]byte(`{"type":"system","message":"sandbox ready"}`), &event); err != nil || event.Message.ID != "" {
		t.Fatalf("a string message: %v %+v", err, event.Message)
	}
	if err := json.Unmarshal([]byte(`{"message":{"id":"m1","model":"x","stop_reason":"end_turn"}}`), &event); err != nil || event.Message.ID != "m1" || event.Message.StopReason != "end_turn" {
		t.Fatalf("an object message: %v %+v", err, event.Message)
	}
}

func TestReviewShellArgs(t *testing.T) {
	c := &Backend{Profile: User}
	plain := c.args(agent.Request{Schema: []byte(`{}`)})
	if i := slices.Index(plain, "--tools"); plain[i+1] != "Read,Glob,Grep" || !slices.Contains(plain, "--restricted") {
		t.Fatalf("a review without a shell: %v", plain)
	}
	shelled := c.args(agent.Request{Schema: []byte(`{}`), Shell: true, ReadDirs: []string{"/clone/.git"}})
	i := slices.Index(shelled, "--tools")
	if ok, _ := SandboxAvailable(); !ok {
		if shelled[i+1] != "Read,Glob,Grep" {
			t.Fatalf("a shell without a sandbox: %v", shelled)
		}
		return
	}
	settings := shelled[slices.Index(shelled, "--settings")+1]
	if shelled[i+1] != "Read,Glob,Grep,Bash" || !strings.Contains(settings, `"allowUnsandboxedCommands":false`) || !slices.Contains(shelled, "--restricted") {
		t.Fatalf("a review with a shell: %v", shelled)
	}
	if j := slices.Index(shelled, "--add-dir"); j < 0 || shelled[j+1] != "/clone/.git" {
		t.Fatalf("the read directory: %v", shelled)
	}
}
