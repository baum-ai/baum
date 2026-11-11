package sessions

import (
	"encoding/json"
	"time"
	"unsafe"

	"github.com/baum-ai/baum/sessions/session"
)

// graphBytes estimates the memory a mapped graph keeps alive: its rows, and
// the ids, text and JSON they own. A reference to another row's id is taken
// to share that row's string, and allocator overhead is left out, so this is
// closer to a lower bound than to the heap profile. The mapping cache reports
// it beside the transcript bytes it accounts in, so the two can be compared
// with the daemon's heap.
func graphBytes(g *session.Graph) int64 {
	if g == nil {
		return 0
	}
	n := int64(unsafe.Sizeof(*g)) +
		sizeRows(g.Sessions) + sizeRows(g.Attributes) + sizeRows(g.UserInputs) +
		sizeRows(g.ModelInvocations) + sizeRows(g.AssistantMessages) + sizeRows(g.ToolUses) +
		sizeRows(g.ToolResults) + sizeRows(g.FileTouches) + sizeRows(g.TokenUsage) + sizeRows(g.VendorIDs)
	for _, s := range g.Sessions {
		n += sizeText(s.ID) + sizeTextPtr(s.Title) + sizeTextPtr(s.WorkingDir) + sizeTextPtr(s.GitBranch) +
			sizeTextPtr(s.RepoRoot) + sizeTextPtr(s.WorktreeRoot) + sizeTime(s.StartedAt) + sizeTime(s.EndedAt)
	}
	for _, a := range g.Attributes {
		n += sizeText(a.ID) + sizeText(a.Key) + sizeRaw(a.Value)
	}
	for _, u := range g.UserInputs {
		n += sizeText(u.ID) + sizeText(u.Text) + sizeRaw(u.Metadata) + sizeTime(u.TriggeredAt)
	}
	for _, m := range g.ModelInvocations {
		n += sizeText(m.ID) + sizeTextPtr(m.Model) + sizeTextPtr(m.Speed) + sizeTextPtr(m.StopReason) + sizeTime(m.StartedAt)
	}
	for _, m := range g.AssistantMessages {
		n += sizeText(m.ID) + sizeTextPtr(m.Text) + sizeRaw(m.Content) + sizeTime(m.Timestamp)
	}
	for _, u := range g.ToolUses {
		n += sizeText(u.ID) + sizeText(u.Name) + sizeRaw(u.Input) + sizeRaw(u.Metadata) + sizeTime(u.Timestamp)
	}
	for _, r := range g.ToolResults {
		n += sizeText(r.ID) + sizeTextPtr(r.OutputText) + sizeRaw(r.Output) + sizeTextPtr(r.ErrorText) + sizeTime(r.Timestamp)
	}
	for _, f := range g.FileTouches {
		n += sizeText(f.ID) + sizeText(f.Path) + sizeRows(f.Hunks)
		for _, h := range f.Hunks {
			n += sizeRows(h.Lines)
			for _, line := range h.Lines {
				n += sizeText(line)
			}
		}
	}
	for _, t := range g.TokenUsage {
		n += sizeText(t.ID) + sizeTextPtr(t.Model) + sizeTextPtr(t.Speed)
	}
	for _, v := range g.VendorIDs {
		n += sizeText(v.VendorIDKind) + sizeText(v.VendorID) + sizeTextPtr(v.SourceRef)
	}
	return n
}

func sizeRows[T any](rows []T) int64 {
	var row T
	return int64(cap(rows)) * int64(unsafe.Sizeof(row))
}

func sizeText(s string) int64 { return int64(len(s)) }

func sizeTextPtr(s *string) int64 {
	if s == nil {
		return 0
	}
	return int64(unsafe.Sizeof(*s)) + int64(len(*s))
}

func sizeRaw(r json.RawMessage) int64 { return int64(len(r)) }

func sizeTime(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return int64(unsafe.Sizeof(*t))
}
